package dbmove

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/wsarchive"
)

// Something written late (D352–D358, docs/database-move.md): the move marks
// the old database moved only after its last look under the freeze, but a
// write a computer sent just before it slept for longer than MetaRush trusts
// a look at the marker can still land in the old database after that (closing
// that for good would take a transaction around every write). So the move
// keeps fingerprints of every record as its last look read them
// (RecheckPrinting), and later looks compare the old database with them
// (Look): what is new there since, what changed and what is gone. A look only
// reads the old database — the server's own hash of each kind of record
// (dbHash: nothing is sent over the network for a kind that didn't change),
// then, for a kind that did, its records in _id ranges of the fingerprints,
// with a count to be sure no record fell between the ranges — and never
// writes to it.

// PrintsFormat names a file of a move's fingerprints.
const PrintsFormat = "metarush.move-prints/1"

// Kinds of record a look finds.
const (
	LateNew     = "new"     // in the old database, not in the copy when the workspace moved
	LateChanged = "changed" // in both, different in the old database now
	LateGone    = "gone"    // in the copy, gone from the old database since
)

// PrintsHeader is what a file of fingerprints says before its records.
type PrintsHeader struct {
	Format string `json:"format"`
	Move   string `json:"move"`
	// Database is the old database's name (never its address).
	Database string `json:"database"`
	// At is the old database server's time at the move's last look: records
	// that end by themselves were compared as of then.
	At    time.Time   `json:"at"`
	Kinds []PrintKind `json:"kinds"`
}

// PrintKind is one kind of record fingerprinted: the server's own hash of it
// at the move ("" when the server didn't say) and, for records that end by
// themselves, the time limits they were compared by.
type PrintKind struct {
	Name       string        `json:"name"`
	ServerHash string        `json:"serverHash,omitempty"`
	Ending     []PrintEnding `json:"ending,omitempty"`
}

// PrintEnding is a time limit (a TTL index): its field and how long after it
// a record goes.
type PrintEnding struct {
	Field string `json:"field"`
	After int64  `json:"after"`
}

func (k PrintKind) limits() []timeLimit {
	out := make([]timeLimit, 0, len(k.Ending))
	for _, e := range k.Ending {
		out = append(out, timeLimit{field: e.Field, after: e.After})
	}
	return out
}

// The file: gzip of the header's JSON (its length first), then per kind a
// 'K' and its name, per record an 'R', its _id (the BSON type and value) and
// the first 16 bytes of the SHA-256 of its line (the export's canonical
// Extended JSON), and 'Z' at the end (a file cut short is never trusted).
const (
	tagKind   = 'K'
	tagRecord = 'R'
	tagEnd    = 'Z'
	printLen  = 16
)

// PrintOf is a record's fingerprint, as a move and a look take it.
func PrintOf(doc bson.Raw) (string, error) {
	line, err := wsarchive.Line(doc)
	if err != nil {
		return "", err
	}
	return printOfLine(line), nil
}

func printOfLine(line []byte) string {
	s := sha256.Sum256(line)
	return hex.EncodeToString(s[:printLen])
}

// idKey is a record's _id as bytes: its BSON type, then its value.
func idKey(doc bson.Raw) ([]byte, error) {
	v, err := doc.LookupErr("_id")
	if err != nil {
		return nil, err
	}
	return append([]byte{byte(v.Type)}, v.Value...), nil
}

// IDOf turns an _id kept as bytes back into its value.
func IDOf(key []byte) bson.RawValue {
	if len(key) == 0 {
		return bson.RawValue{}
	}
	return bson.RawValue{Type: bson.Type(key[0]), Value: key[1:]}
}

type printWriter struct {
	f  io.Writer
	gz *gzip.Writer
	bw *bufio.Writer
}

func newPrintWriter(w io.Writer, h PrintsHeader) (*printWriter, error) {
	gz := gzip.NewWriter(w)
	p := &printWriter{f: w, gz: gz, bw: bufio.NewWriterSize(gz, 1<<20)}
	b, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	if err := p.uvarint(uint64(len(b))); err != nil {
		return nil, err
	}
	_, err = p.bw.Write(b)
	return p, err
}

func (p *printWriter) uvarint(n uint64) error {
	var buf [binary.MaxVarintLen64]byte
	_, err := p.bw.Write(buf[:binary.PutUvarint(buf[:], n)])
	return err
}

func (p *printWriter) kind(name string) error {
	if err := p.bw.WriteByte(tagKind); err != nil {
		return err
	}
	if err := p.uvarint(uint64(len(name))); err != nil {
		return err
	}
	_, err := p.bw.WriteString(name)
	return err
}

func (p *printWriter) record(doc bson.Raw, line []byte) error {
	key, err := idKey(doc)
	if err != nil {
		return err
	}
	if err := p.bw.WriteByte(tagRecord); err != nil {
		return err
	}
	if err := p.uvarint(uint64(len(key))); err != nil {
		return err
	}
	if _, err := p.bw.Write(key); err != nil {
		return err
	}
	s := sha256.Sum256(line)
	_, err = p.bw.Write(s[:printLen])
	return err
}

func (p *printWriter) close() error {
	if err := p.bw.WriteByte(tagEnd); err != nil {
		return err
	}
	if err := p.bw.Flush(); err != nil {
		return err
	}
	return p.gz.Close()
}

// printReader reads a file of fingerprints, one entry at a time.
type printReader struct {
	f   *os.File
	gz  *gzip.Reader
	br  *bufio.Reader
	h   PrintsHeader
	put *printEntry // one entry pushed back
}

type printEntry struct {
	tag   byte
	name  string // tagKind
	key   []byte // tagRecord
	print string
}

// ErrPrints is a file of fingerprints that can't be read (cut short, or not one).
var ErrPrints = errors.New("the move's fingerprints can't be read")

func openPrints(path string) (*printReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %v", ErrPrints, err)
	}
	r := &printReader{f: f, gz: gz, br: bufio.NewReaderSize(gz, 1<<20)}
	n, err := binary.ReadUvarint(r.br)
	if err != nil || n > 64<<20 {
		r.close()
		return nil, ErrPrints
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r.br, b); err != nil || json.Unmarshal(b, &r.h) != nil || r.h.Format != PrintsFormat {
		r.close()
		return nil, ErrPrints
	}
	return r, nil
}

func (r *printReader) close() {
	_ = r.gz.Close()
	_ = r.f.Close()
}

func (r *printReader) next() (*printEntry, error) {
	if r.put != nil {
		e := r.put
		r.put = nil
		return e, nil
	}
	tag, err := r.br.ReadByte()
	if err != nil {
		return nil, ErrPrints // the end comes as 'Z', never as the file's end
	}
	e := &printEntry{tag: tag}
	switch tag {
	case tagEnd:
		return e, nil
	case tagKind, tagRecord:
		n, err := binary.ReadUvarint(r.br)
		if err != nil || n > 16<<20 {
			return nil, ErrPrints
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(r.br, b); err != nil {
			return nil, ErrPrints
		}
		if tag == tagKind {
			e.name = string(b)
			return e, nil
		}
		var p [printLen]byte
		if _, err := io.ReadFull(r.br, p[:]); err != nil {
			return nil, ErrPrints
		}
		e.key, e.print = b, hex.EncodeToString(p[:])
		return e, nil
	}
	return nil, ErrPrints
}

// ReadPrintsHeader reads what a file of fingerprints is about.
func ReadPrintsHeader(path string) (*PrintsHeader, error) {
	r, err := openPrints(path)
	if err != nil {
		return nil, err
	}
	defer r.close()
	h := r.h
	return &h, nil
}

// serverHashes asks the server for its own hash of each collection (dbHash:
// computed there, nothing is sent; a read-only sign-in may ask). nil when it
// doesn't answer.
func serverHashes(ctx context.Context, db *store.DB, names []string) map[string]string {
	if len(names) == 0 {
		return map[string]string{}
	}
	var out struct {
		Collections map[string]string `bson:"collections"`
	}
	if err := db.DB.RunCommand(ctx, bson.D{{Key: "dbHash", Value: 1}, {Key: "collections", Value: names}}).Decode(&out); err != nil || out.Collections == nil {
		return nil
	}
	return out.Collections
}

// RecheckPrinting is Recheck that also keeps the old database's fingerprints
// as the same read goes (w; nil keeps none): every record's _id and print, and
// each kind's own hash from the server, for the looks after the move.
func RecheckPrinting(ctx context.Context, from, to *store.DB, res *Result, move string, w io.Writer) error {
	return recheck(ctx, from, to, res, move, w)
}

/* ---------- a look ---------- */

// Late is one record a look found written into the old database after the
// move.
type Late struct {
	Collection string
	// ID is the record's _id (Key the same as bytes: its type, then its value).
	ID  bson.RawValue
	Key []byte
	// Kind: LateNew, LateChanged or LateGone.
	Kind string
	// Doc is the old database's record now (nil when gone); Print its
	// fingerprint, MovePrint the one it had when the workspace moved ("" for
	// one that is new).
	Doc       bson.Raw
	Print     string
	MovePrint string
}

// LookResult is what a look found.
type LookResult struct {
	// At is the old database server's time at the move's last look.
	At time.Time
	// Kinds is how many kinds of record were looked at; Read names those
	// read again (their hash changed, the server didn't say it, or their
	// records end by themselves).
	Kinds int
	Read  []string
	Found []Late
	// More counts what was found beyond MaxFound (not kept).
	More int
}

// MaxFound is the most records one look keeps (the rest are counted).
var MaxFound = 500

// printChunk is how many fingerprints a look compares in one range of _ids.
var printChunk = 1000

// Look compares the old database with the fingerprints the move kept at
// path: what is new there, changed or gone. It only reads the old database.
func Look(ctx context.Context, old *store.DB, path string) (*LookResult, error) {
	r, err := openPrints(path)
	if err != nil {
		return nil, err
	}
	defer r.close()
	h := r.h
	kinds := map[string]PrintKind{}
	names := make([]string, 0, len(h.Kinds))
	for _, k := range h.Kinds {
		kinds[k.Name], names = k, append(names, k.Name)
	}
	infos, err := wsarchive.ListCollections(ctx, old)
	if err != nil {
		return nil, err
	}
	var late []string
	for _, info := range infos {
		if _, known := kinds[info.Name]; !known && Carried(info.Name) && (info.Type == "" || info.Type == "collection") {
			late = append(late, info.Name)
		}
	}
	hashes := serverHashes(ctx, old, append(append([]string{}, names...), late...))
	res := &LookResult{At: h.At, Kinds: len(h.Kinds) + len(late), Found: []Late{}}
	l := &looker{ctx: ctx, old: old, path: path, at: h.At, res: res}
	for {
		e, err := r.next()
		if err != nil {
			return nil, err
		}
		if e.tag == tagEnd {
			break
		}
		if e.tag != tagKind {
			return nil, ErrPrints
		}
		k, ok := kinds[e.name]
		if !ok {
			return nil, ErrPrints
		}
		if len(k.Ending) == 0 && k.ServerHash != "" && hashes != nil && hashes[k.Name] == k.ServerHash {
			if err := skipKind(r); err != nil { // the server says it is as it was: nothing to read
				return nil, err
			}
			continue
		}
		res.Read = append(res.Read, k.Name)
		if err := l.kind(k, r); err != nil {
			return nil, err
		}
	}
	// A kind of record made in the old database since: every record is new.
	for _, name := range late {
		if n, err := old.C(name).CountDocuments(ctx, bson.M{}, options.Count().SetLimit(1)); err != nil {
			return nil, err
		} else if n == 0 {
			continue
		}
		res.Read = append(res.Read, name)
		if err := l.kind(PrintKind{Name: name}, nil); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// skipKind reads past one kind's records.
func skipKind(r *printReader) error {
	for {
		e, err := r.next()
		if err != nil {
			return err
		}
		if e.tag != tagRecord {
			r.put = e
			return nil
		}
	}
}

type looker struct {
	ctx  context.Context
	old  *store.DB
	path string
	at   time.Time
	res  *LookResult
}

func (l *looker) add(x Late) {
	if len(l.res.Found) >= MaxFound {
		l.res.More++
		return
	}
	l.res.Found = append(l.res.Found, x)
}

// idClass is what MongoDB compares _ids within (query ranges match one type
// only: every number is one, strings another, every other type its own).
func idClass(t bson.Type) byte {
	switch t {
	case bson.TypeDouble, bson.TypeInt32, bson.TypeInt64, bson.TypeDecimal128:
		return 'n'
	case bson.TypeString, bson.TypeSymbol:
		return 's'
	}
	return byte(t)
}

// classTypes are the $type codes of a class.
func classTypes(c byte) []int {
	switch c {
	case 'n':
		return []int{1, 16, 18, 19}
	case 's':
		return []int{2, 14}
	}
	return []int{int(c)}
}

// kind compares one kind of record: its fingerprints (from r, up to the next
// kind; none for a kind made since) in ranges of _ids, then the records
// beyond them; and a count to be sure every record was seen, else the whole
// kind is read once against all its fingerprints.
func (l *looker) kind(k PrintKind, r *printReader) error {
	start, more := len(l.res.Found), l.res.More
	base := bson.M{}
	ttl := len(k.Ending) > 0
	if ttl {
		base = stillLive(k.limits(), l.at)
	}
	var (
		chunk     = map[string]string{}
		chunkLast []byte
		chunkCls  byte
		last      = map[byte][]byte{} // the last _id of each class read so far
		order     []byte
		seen      int64 // records of the old database accounted for
	)
	flush := func() error {
		if len(chunk) == 0 {
			return nil
		}
		rng := bson.M{"$lte": IDOf(chunkLast)}
		if prev := last[chunkCls]; prev != nil {
			rng["$gt"] = IDOf(prev)
		}
		n, err := l.compare(k.Name, and(base, bson.M{"_id": rng}), chunk, ttl)
		if err != nil {
			return err
		}
		seen += n
		last[chunkCls] = chunkLast
		chunk = map[string]string{}
		return nil
	}
	for r != nil {
		e, err := r.next()
		if err != nil {
			return err
		}
		if e.tag != tagRecord {
			r.put = e
			break
		}
		cls := idClass(bson.Type(e.key[0]))
		if len(chunk) > 0 && (cls != chunkCls || len(chunk) >= printChunk) {
			if err := flush(); err != nil {
				return err
			}
		}
		if _, ok := last[cls]; !ok {
			last[cls], order = nil, append(order, cls)
		}
		chunk[string(e.key)], chunkLast, chunkCls = e.print, e.key, cls
	}
	if err := flush(); err != nil {
		return err
	}
	// Beyond the last fingerprint of each class, and of a type no fingerprint
	// has: new records.
	var types bson.A
	for _, cls := range order {
		n, err := l.compare(k.Name, and(base, bson.M{"_id": bson.M{"$gt": IDOf(last[cls])}}), nil, ttl)
		if err != nil {
			return err
		}
		seen += n
		for _, t := range classTypes(cls) {
			types = append(types, t)
		}
	}
	other := base
	if len(types) > 0 {
		other = and(base, bson.M{"_id": bson.M{"$not": bson.M{"$type": types}}})
	}
	n, err := l.compare(k.Name, other, nil, ttl)
	if err != nil {
		return err
	}
	seen += n
	// Every record of the old database seen once? (Something written during
	// the look itself, or an _id MongoDB orders unlike these ranges, says no:
	// then the whole kind against every fingerprint.)
	total, err := l.old.C(k.Name).CountDocuments(l.ctx, base)
	if err != nil {
		return err
	}
	if total == seen || r == nil {
		return nil
	}
	l.res.Found, l.res.More = l.res.Found[:start], more
	all, err := l.kindPrints(k.Name)
	if err != nil {
		return err
	}
	_, err = l.compare(k.Name, base, all, ttl)
	return err
}

// kindPrints reads one kind's fingerprints again, all of them.
func (l *looker) kindPrints(name string) (map[string]string, error) {
	r, err := openPrints(l.path)
	if err != nil {
		return nil, err
	}
	defer r.close()
	out := map[string]string{}
	in := false
	for {
		e, err := r.next()
		if err != nil {
			return nil, err
		}
		switch e.tag {
		case tagEnd:
			return out, nil
		case tagKind:
			if in {
				return out, nil
			}
			in = e.name == name
		case tagRecord:
			if in {
				out[string(e.key)] = e.print
			}
		}
	}
}

// compare reads the old database's records matching filter and compares each
// with prints (taken from it as they are seen): new when it has none,
// changed when it differs; what is left of prints is gone (never said of
// records that end by themselves: they may have). It says how many records
// it read.
func (l *looker) compare(coll string, filter bson.M, prints map[string]string, ttl bool) (int64, error) {
	cur, err := l.old.C(coll).Find(l.ctx, filter, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetBatchSize(wsarchive.FindBatch))
	if err != nil {
		return 0, err
	}
	defer cur.Close(l.ctx)
	var n int64
	for cur.Next(l.ctx) {
		n++
		doc := cur.Current
		key, err := idKey(doc)
		if err != nil {
			return n, err
		}
		line, err := wsarchive.Line(doc)
		if err != nil {
			return n, err
		}
		now := printOfLine(line)
		was, known := prints[string(key)]
		if known {
			delete(prints, string(key))
			if was == now {
				continue
			}
		}
		x := Late{Collection: coll, ID: IDOf(key), Key: key, Kind: LateNew, Doc: bytes.Clone(doc), Print: now}
		if known {
			x.Kind, x.MovePrint = LateChanged, was
		}
		l.add(x)
	}
	if err := cur.Err(); err != nil {
		return n, err
	}
	if !ttl {
		gone := make([]string, 0, len(prints))
		for key := range prints {
			gone = append(gone, key)
		}
		sort.Strings(gone)
		for _, key := range gone {
			k := []byte(key)
			l.add(Late{Collection: coll, ID: IDOf(k), Key: k, Kind: LateGone, MovePrint: prints[key]})
		}
	}
	return n, nil
}

// and joins two filters (either may be empty).
func and(a, b bson.M) bson.M {
	switch {
	case len(a) == 0:
		return b
	case len(b) == 0:
		return a
	}
	return bson.M{"$and": bson.A{a, b}}
}
