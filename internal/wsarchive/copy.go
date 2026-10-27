package wsarchive

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/store"
)

// The copy engine: a collection's documents stream one at a time, as
// canonical MongoDB Extended JSON v2 lines, in _id order — out of a database
// into a file (export), out of a file into a database (restore), and out of
// a database into a hash (the check that a copy holds exactly what was
// copied). Nothing ever holds a whole collection in memory: a cursor reads
// in batches, a batch of inserts holds at most InsertBatch documents or
// InsertBatchBytes of them.

// Limits of the stream.
const (
	// FindBatch is how many documents a cursor reads at once.
	FindBatch = 256
	// InsertBatch and InsertBatchBytes cap one batch of inserts.
	InsertBatch      = 500
	InsertBatchBytes = 8 << 20
	// MaxLine is the longest line a file of documents may have: a document
	// is at most 16 MB as BSON and Extended JSON can be up to about four
	// times longer (binary data in base64, numbers spelled out).
	MaxLine = 80 << 20
)

// EachDoc calls fn for every document of a workspace in a collection, in _id
// order, reading in batches (the _id index gives the order, so the server
// never sorts in memory).
func EachDoc(ctx context.Context, db *store.DB, coll, ws string, fn func(bson.Raw) error) error {
	return EachDocWhere(ctx, db, coll, workspaceFilter(ws), fn)
}

// EachDocWhere calls fn for every document matching filter, in _id order:
// with an empty filter, every document of the collection (a move of the
// whole database, D334).
func EachDocWhere(ctx context.Context, db *store.DB, coll string, filter bson.M, fn func(bson.Raw) error) error {
	if filter == nil {
		filter = bson.M{}
	}
	cur, err := db.C(coll).Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).
		SetHint(bson.D{{Key: "_id", Value: 1}}).SetBatchSize(FindBatch))
	if err != nil {
		return err
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		if err := fn(cur.Current); err != nil {
			return err
		}
	}
	return cur.Err()
}

// Line is a document as one line of a file: canonical Extended JSON v2 and a
// newline.
func Line(doc bson.Raw) ([]byte, error) {
	b, err := bson.MarshalExtJSON(doc, true, false)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// FromLine reads a line back into the document's BSON bytes.
func FromLine(line []byte) (bson.Raw, error) {
	line = bytes.TrimRight(line, "\r\n")
	var d bson.Raw
	if err := bson.UnmarshalExtJSON(line, true, &d); err != nil {
		return nil, err
	}
	return d, nil
}

// LineWriter writes documents as lines and keeps their count, size and SHA-256.
type LineWriter struct {
	w     *bufio.Writer
	h     hash.Hash
	Count int64
	Bytes int64
}

// NewLineWriter writes lines to w.
func NewLineWriter(w io.Writer) *LineWriter {
	h := sha256.New()
	return &LineWriter{w: bufio.NewWriterSize(io.MultiWriter(w, h), 1<<20), h: h}
}

// Write adds one document.
func (l *LineWriter) Write(doc bson.Raw) error {
	b, err := Line(doc)
	if err != nil {
		return err
	}
	n, err := l.w.Write(b)
	l.Bytes += int64(n)
	if err == nil {
		l.Count++
	}
	return err
}

// Close flushes and returns the SHA-256 of everything written.
func (l *LineWriter) Close() (string, error) {
	if err := l.w.Flush(); err != nil {
		return "", err
	}
	return hex.EncodeToString(l.h.Sum(nil)), nil
}

// Digest is what a collection's documents add up to: their count and the
// SHA-256 and size of them as lines.
type Digest struct {
	Count  int64  `json:"count"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// HashCollection reads a workspace's documents in a collection and digests
// them as lines (what an export writes, what a copy must hold).
func HashCollection(ctx context.Context, db *store.DB, coll, ws string) (Digest, error) {
	return HashWhere(ctx, db, coll, workspaceFilter(ws))
}

// HashWhere digests the documents matching filter (empty: all of them) as lines.
func HashWhere(ctx context.Context, db *store.DB, coll string, filter bson.M) (Digest, error) {
	lw := NewLineWriter(io.Discard)
	if err := EachDocWhere(ctx, db, coll, filter, lw.Write); err != nil {
		return Digest{}, err
	}
	sum, err := lw.Close()
	return Digest{Count: lw.Count, Bytes: lw.Bytes, SHA256: sum}, err
}

// Inserter puts documents into a collection in ordered batches of at most
// InsertBatch documents or InsertBatchBytes (what InsertLines and a move of
// the database share). Ordered: when a batch stops, the documents before the
// one that failed are in, so the collection holds a prefix of what was given.
type Inserter struct {
	ctx   context.Context
	db    *store.DB
	coll  string
	batch []any
	size  int
	// Count is how many documents went in.
	Count int64
}

// NewInserter inserts into coll.
func NewInserter(ctx context.Context, db *store.DB, coll string) *Inserter {
	return &Inserter{ctx: ctx, db: db, coll: coll}
}

// Add queues one document (a copy of it: cursors reuse their buffers),
// inserting a batch once it is full.
func (in *Inserter) Add(doc bson.Raw) error {
	in.batch = append(in.batch, bson.Raw(append([]byte(nil), doc...)))
	in.size += len(doc)
	if len(in.batch) >= InsertBatch || in.size >= InsertBatchBytes {
		return in.Flush()
	}
	return nil
}

// Flush inserts what is queued.
func (in *Inserter) Flush() error {
	if len(in.batch) == 0 {
		return nil
	}
	res, err := in.db.C(in.coll).InsertMany(in.ctx, in.batch, options.InsertMany().SetOrdered(true))
	if res != nil {
		in.Count += int64(len(res.InsertedIDs))
	}
	in.batch, in.size = in.batch[:0], 0
	return err
}

// InsertLines streams documents from lines into a collection, in batches.
// It returns how many it inserted.
func InsertLines(ctx context.Context, db *store.DB, coll string, r io.Reader) (int64, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	in := NewInserter(ctx, db, coll)
	for lineNo := 1; ; lineNo++ {
		line, err := readLine(br)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return in.Count, fmt.Errorf("%s line %d: %w", coll, lineNo, err)
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		doc, err := FromLine(line)
		if err != nil {
			return in.Count, fmt.Errorf("%s line %d: %w", coll, lineNo, err)
		}
		if err := in.Add(doc); err != nil {
			return in.Count, err
		}
	}
	err := in.Flush()
	return in.Count, err
}

// readLine reads one line, refusing one longer than MaxLine.
func readLine(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > MaxLine {
			return nil, fmt.Errorf("a line is longer than any document can be")
		}
		switch {
		case err == nil:
			return buf, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(buf) > 0 {
				return buf, nil
			}
			return nil, io.EOF
		default:
			return nil, err
		}
	}
}

// Spec is how a collection was made: its type, options and indexes (but
// _id's), as the source database lists them.
type Spec struct {
	Name    string
	Type    string
	Options bson.Raw
	Indexes []bson.Raw
}

// ReadSpec reads how a collection was made (read only).
func ReadSpec(ctx context.Context, db *store.DB, info CollectionInfo) (Spec, error) {
	s := Spec{Name: info.Name, Type: info.Type, Options: info.Options}
	if s.Type == "" {
		s.Type = "collection"
	}
	if s.Type != "collection" {
		return s, nil
	}
	ix, err := ListIndexes(ctx, db, info.Name)
	if err != nil {
		return s, fmt.Errorf("%s indexes: %w", info.Name, err)
	}
	s.Indexes = ix
	return s, nil
}

// Make creates the collection in an empty database the way it was made:
// with its options first, then all its indexes in one command (before the
// documents, so a unique index refuses a duplicate as it comes in).
func (s Spec) Make(ctx context.Context, db *store.DB) error {
	if s.Type != "" && s.Type != "collection" {
		return fmt.Errorf("%s is a %s, which an archive doesn't carry", s.Name, s.Type)
	}
	// Never into a collection that is there already (MongoDB's create says
	// yes to one made the same way): a copy is never merged.
	if names, err := db.DB.ListCollectionNames(ctx, bson.D{{Key: "name", Value: s.Name}}); err != nil {
		return err
	} else if len(names) > 0 {
		return fmt.Errorf("%s is in the database already", s.Name)
	}
	cmd := bson.D{{Key: "create", Value: s.Name}}
	if len(s.Options) > 0 {
		elems, err := s.Options.Elements()
		if err != nil {
			return err
		}
		for _, e := range elems {
			cmd = append(cmd, bson.E{Key: e.Key(), Value: e.Value()})
		}
	}
	if err := db.DB.RunCommand(ctx, cmd).Err(); err != nil {
		return fmt.Errorf("%s: %w", s.Name, err)
	}
	if len(s.Indexes) == 0 {
		return nil
	}
	ixs := make(bson.A, 0, len(s.Indexes))
	for _, ix := range s.Indexes {
		ixs = append(ixs, withoutNS(ix))
	}
	if err := db.DB.RunCommand(ctx, bson.D{{Key: "createIndexes", Value: s.Name}, {Key: "indexes", Value: ixs}}).Err(); err != nil {
		return fmt.Errorf("%s indexes: %w", s.Name, err)
	}
	return nil
}

// withoutNS drops the "ns" field older servers list with an index (newer
// ones refuse it in createIndexes).
func withoutNS(ix bson.Raw) bson.Raw {
	if _, err := ix.LookupErr("ns"); err != nil {
		return ix
	}
	var d bson.D
	if bson.Unmarshal(ix, &d) != nil {
		return ix
	}
	d, _ = unset(d, "ns")
	b, err := bson.Marshal(d)
	if err != nil {
		return ix
	}
	return b
}

// ejson is a value as canonical Extended JSON (for the manifest).
func ejson(doc bson.Raw) (json.RawMessage, error) {
	if len(doc) == 0 {
		return nil, nil
	}
	b, err := bson.MarshalExtJSON(doc, true, false)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// fromEJSON is the reverse of ejson.
func fromEJSON(b json.RawMessage) (bson.Raw, error) {
	if len(bytes.TrimSpace(b)) == 0 || string(bytes.TrimSpace(b)) == "null" {
		return nil, nil
	}
	return FromLine(b)
}
