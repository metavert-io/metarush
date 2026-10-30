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
	"math/big"
	"os"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/wsarchive"
)

// Something written late (D352–D359, D370, docs/database-move.md): the move marks
// the old database moved only after its last look under the freeze, but a
// write a computer sent just before it slept for longer than MetaRush trusts
// a look at the marker can still land in the old database after that (closing
// that for good would take a transaction around every write). So the move
// keeps fingerprints of every record as its last look read them
// (RecheckPrinting), and later looks compare the old database with them
// (Look): what is new there since, what changed and what is gone. A look only
// reads the old database — the server's own hash of each kind of record
// (dbHash, where the server offers it), then the kinds that changed, in _id
// order beside their fingerprints — within bounds, going on next time from
// where it stopped, and never writes to it.

// PrintsFormat names a file of a move's fingerprints.
const PrintsFormat = "metarush.move-prints/1"

// Kinds of record a look finds.
const (
	LateNew     = "new"     // in the old database, not in the copy when the workspace moved
	LateChanged = "changed" // in both, different in the old database now
	LateGone    = "gone"    // in the copy, gone from the old database since
	// A whole kind of record gone from the old database (one finding, Count
	// records), and the whole database gone or empty.
	LateKindGone = "kind-gone"
	LateEmptied  = "emptied"
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
	if noServerHashes {
		return nil
	}
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

// noServerHashes plays a server that doesn't say its own hashes (tests:
// Atlas's free and Flex clusters).
var noServerHashes bool

// RecheckPrinting is Recheck that also keeps the old database's fingerprints
// as the same read goes (w; nil keeps none): every record's _id and print, and
// each kind's own hash from the server, for the looks after the move.
func RecheckPrinting(ctx context.Context, from, to *store.DB, res *Result, move string, w io.Writer) error {
	return recheck(ctx, from, to, res, move, w)
}

/* ---------- a look ---------- */

// Late is one thing a look found written into the old database after the
// move: one record, a whole kind of record gone (LateKindGone), or the whole
// database gone or empty (LateEmptied).
type Late struct {
	Collection string
	// ID is the record's _id (Key the same as bytes: its type, then its value).
	ID  bson.RawValue
	Key []byte
	// Kind: LateNew, LateChanged, LateGone, LateKindGone or LateEmptied.
	Kind string
	// Doc is the old database's record now (nil when gone); Print its
	// fingerprint, MovePrint the one it had when the workspace moved ("" for
	// one that is new).
	Doc       bson.Raw
	Print     string
	MovePrint string
	// Count is how many records a whole kind gone held at the move.
	Count int64
}

// LookPosition is where a look stopped, for the next one to go on from: the
// kind it was reading and the last record of it dealt with.
type LookPosition struct {
	Kind string `bson:"kind"`
	// Key is that record's _id (its type, then its value).
	Key []byte `bson:"key,omitempty"`
	// InOther: it was among the records whose _id is of a type no fingerprint
	// has, Other of them dealt with.
	InOther bool  `bson:"inOther,omitempty"`
	Other   int64 `bson:"other,omitempty"`
}

// LookOptions say how a look goes.
type LookOptions struct {
	// Each gets every record found as it is found (nil: they are collected
	// in the result, at most MaxFound of them).
	Each func(Late) error
	// MaxBytes and MaxTime bound how much of the old database one look reads
	// again and for how long (0: no bound); past either it stops and says
	// so (Partial). Pace is a pause after every PaceEvery records read, so a
	// look never runs the old database's server flat out.
	MaxBytes int64
	MaxTime  time.Duration
	Pace     time.Duration
	// From goes on where an earlier look stopped (nil: from the start).
	From *LookPosition
}

// PaceEvery is how many records a look reads between two pauses.
const PaceEvery = 1000

// removedCost is what a record gone counts for against a look's bounds:
// nothing is read for it, but something is kept.
const removedCost = 1024

// KindGoneFrom is how many records a kind must have held for its being
// wholly gone to be one finding, rather than one per record.
var KindGoneFrom = 50

// LookResult is what a look found.
type LookResult struct {
	// At is the old database server's time at the move's last look.
	At time.Time
	// Names are all the kinds of record there are to look at, in the order a
	// look goes through them; Done those this look finished with. Read names
	// those it read again (their hash changed, the server didn't say it, or
	// their records end by themselves), Skipped those the server's own hash
	// said are as they were.
	Names   []string
	Done    []string
	Read    []string
	Skipped []string
	Kinds   int
	// ServerHashes: the server said its own hash of each kind (else every
	// kind is read again, every look).
	ServerHashes bool
	// BytesRead is how much of the old database the look read again.
	BytesRead int64
	// Partial: it stopped before it finished (Stopped says why, in words);
	// what it found so far stands, but nothing can be said of what it didn't
	// read. Position is where to go on from.
	Partial  bool
	Stopped  string
	Position *LookPosition
	// Emptied: the old database holds no record of any kind any more (it was
	// removed or emptied). Nothing else was looked at.
	Emptied bool
	// Found and More: without Each, the records found (at most MaxFound)
	// and how many more there were.
	Found []Late
	More  int
}

// MaxFound is the most records a look collects when no Each takes them.
var MaxFound = 500

// errStop stops a look past its bounds.
var errStop = errors.New("the look reached its bounds")

// Look compares the old database with the fingerprints the move kept at
// path: what is new there, changed or gone. It only reads the old database.
func Look(ctx context.Context, old *store.DB, path string, o LookOptions) (*LookResult, error) {
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
	emptied := true
	for _, info := range infos {
		if !Carried(info.Name) || (info.Type != "" && info.Type != "collection") {
			continue
		}
		if emptied {
			n, err := old.C(info.Name).CountDocuments(ctx, bson.M{}, options.Count().SetLimit(1))
			if err != nil {
				return nil, err
			}
			emptied = n == 0
		}
		if _, known := kinds[info.Name]; !known {
			late = append(late, info.Name)
		}
	}
	sort.Strings(late)
	res := &LookResult{At: h.At, Names: append(append([]string{}, names...), late...), Kinds: len(h.Kinds) + len(late), Found: []Late{}, Done: []string{}}
	if emptied {
		res.Emptied = true
		return res, nil
	}
	// Going on: every kind before where the last look stopped is done with.
	from := o.From
	fromPrint := false
	if from != nil {
		_, fromPrint = kinds[from.Kind]
	}
	reached := from == nil
	wanted := names
	if from != nil {
		wanted = nil
		if fromPrint {
			for i, n := range names {
				if n == from.Kind {
					wanted = names[i:]
				}
			}
		}
	}
	hashes := serverHashes(ctx, old, wanted)
	res.ServerHashes = hashes != nil
	l := &looker{ctx: ctx, old: old, at: h.At, res: res, o: o, started: time.Now()}
	done := func(err error) (*LookResult, error) {
		if errors.Is(err, errStop) {
			res.Partial = true
			pos := l.pos
			res.Position = &pos
			return res, nil
		}
		return nil, err
	}
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
		var at *LookPosition
		if !reached {
			if !fromPrint || e.name != from.Kind {
				if err := skipKind(r); err != nil {
					return nil, err
				}
				continue
			}
			reached, at = true, from
		}
		if len(k.Ending) == 0 && k.ServerHash != "" && hashes != nil && hashes[k.Name] == k.ServerHash {
			res.Skipped = append(res.Skipped, k.Name)
			res.Done = append(res.Done, k.Name)
			if err := skipKind(r); err != nil { // the server says it is as it was: nothing to read
				return nil, err
			}
			continue
		}
		res.Read = append(res.Read, k.Name)
		if err := l.kind(k, r, at); err != nil {
			return done(err)
		}
		res.Done = append(res.Done, k.Name)
	}
	// A kind of record made in the old database since: every record is new.
	for _, name := range late {
		var at *LookPosition
		if from != nil && !fromPrint {
			if name < from.Kind {
				continue
			}
			if name == from.Kind {
				at = from
			}
		}
		if n, err := old.C(name).CountDocuments(ctx, bson.M{}, options.Count().SetLimit(1)); err != nil {
			return nil, err
		} else if n == 0 {
			res.Done = append(res.Done, name)
			continue
		}
		res.Read = append(res.Read, name)
		if err := l.kind(PrintKind{Name: name}, nil, at); err != nil {
			return done(err)
		}
		res.Done = append(res.Done, name)
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
	ctx     context.Context
	old     *store.DB
	at      time.Time
	res     *LookResult
	o       LookOptions
	started time.Time
	read    int
	pos     LookPosition
}

func (l *looker) add(x Late) error {
	if l.o.Each != nil {
		return l.o.Each(x)
	}
	if len(l.res.Found) >= MaxFound {
		l.res.More++
		return nil
	}
	l.res.Found = append(l.res.Found, x)
	return nil
}

// account counts what a record cost against the look's bounds, once it was
// dealt with (so every look gets at least one record further), and paces.
func (l *looker) account(size int) error {
	l.res.BytesRead += int64(size)
	l.read++
	if l.o.MaxBytes > 0 && l.res.BytesRead > l.o.MaxBytes {
		l.res.Stopped = fmt.Sprintf("after reading %s of the old database, the most one look reads", wsarchive.HumanBytes(l.res.BytesRead))
		return errStop
	}
	if l.o.MaxTime > 0 && time.Since(l.started) > l.o.MaxTime {
		l.res.Stopped = fmt.Sprintf("after %d minutes, the longest one look takes", int(l.o.MaxTime.Minutes()+0.5))
		return errStop
	}
	if l.o.Pace > 0 && l.read%PaceEvery == 0 {
		select {
		case <-l.ctx.Done():
			return l.ctx.Err()
		case <-time.After(l.o.Pace):
		}
	}
	return nil
}

// gone reports one record gone from the old database.
func (l *looker) gone(coll string, e *printEntry) error {
	k := e.key
	if err := l.add(Late{Collection: coll, ID: IDOf(k), Key: k, Kind: LateGone, MovePrint: e.print}); err != nil {
		return err
	}
	l.pos.Key = k
	return l.account(removedCost)
}

// idClass is what MongoDB compares _ids within (every number is one, strings
// another, every other type its own).
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
func classTypes(c byte) bson.A {
	switch c {
	case 'n':
		return bson.A{1, 16, 18, 19}
	case 's':
		return bson.A{2, 14}
	}
	return bson.A{int(c)}
}

// kind compares one kind of record with its fingerprints (from r, up to the
// next kind; none for a kind made since), from where an earlier look stopped
// (from; nil: from its start): per class of _id, the old database's records
// and the fingerprints are both in MongoDB's _id order, so one pass over each
// — a merge — says what is new, changed and gone, holding one record at a
// time. Records whose _id is of a type no fingerprint has are new. A kind
// wholly gone that held many records is one finding.
func (l *looker) kind(k PrintKind, r *printReader, from *LookPosition) error {
	base := bson.M{}
	ttl := len(k.Ending) > 0
	if ttl {
		base = stillLive(k.limits(), l.at)
	}
	l.pos = LookPosition{Kind: k.Name}
	var types bson.A
	var e *printEntry
	next := func() error {
		e = nil
		if r == nil {
			return nil
		}
		x, err := r.next()
		if err != nil {
			return err
		}
		if x.tag != tagRecord {
			r.put = x
			return nil
		}
		e = x
		return nil
	}
	if err := next(); err != nil {
		return err
	}
	cls := func(x *printEntry) byte { return idClass(bson.Type(x.key[0])) }
	// runClass walks one class of _id from after a record (nil: its start).
	runClass := func(c byte, after []byte) error {
		id := bson.M{"$type": classTypes(c)}
		if after != nil && ordered(c) {
			id["$gt"] = IDOf(after)
		}
		cur, err := l.old.C(k.Name).Find(l.ctx, and(base, bson.M{"_id": id}),
			options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetBatchSize(wsarchive.FindBatch))
		if err != nil {
			return err
		}
		defer cur.Close(l.ctx)
		if ordered(c) {
			return l.merge(k.Name, c, cur, &e, next, ttl)
		}
		return l.unordered(k.Name, c, cur, &e, next, ttl)
	}
	otherSkip := int64(0)
	switch {
	case from != nil && from.InOther:
		for e != nil {
			c := cls(e)
			types = append(types, classTypes(c)...)
			for e != nil && cls(e) == c {
				if err := next(); err != nil {
					return err
				}
			}
		}
		otherSkip = from.Other
		l.pos.InOther, l.pos.Other = true, from.Other
	case from != nil && len(from.Key) > 0:
		c := idClass(bson.Type(from.Key[0]))
		for e != nil && cls(e) != c {
			p := cls(e)
			types = append(types, classTypes(p)...)
			for e != nil && cls(e) == p {
				if err := next(); err != nil {
					return err
				}
			}
		}
		types = append(types, classTypes(c)...)
		l.pos.Key = from.Key
		if ordered(c) {
			for e != nil && cls(e) == c {
				n, err := compareIDs(e.key, from.Key)
				if err != nil {
					return err
				}
				if n > 0 {
					break
				}
				if err := next(); err != nil {
					return err
				}
			}
		}
		if err := runClass(c, from.Key); err != nil {
			return err
		}
	case from == nil && e != nil && !ttl:
		// Wholly gone, and it held many: one finding rather than one per record.
		n, err := l.old.C(k.Name).CountDocuments(l.ctx, bson.M{}, options.Count().SetLimit(1))
		if err != nil {
			return err
		}
		if n == 0 {
			var held []*printEntry
			for e != nil && len(held) < KindGoneFrom {
				held = append(held, e)
				if err := next(); err != nil {
					return err
				}
			}
			if e != nil {
				count := int64(len(held))
				for e != nil {
					count++
					if err := next(); err != nil {
						return err
					}
				}
				// One finding, and the kind is finished with (never a stop here: a
				// look going on from its start would report every record).
				l.res.BytesRead += removedCost
				return l.add(Late{Collection: k.Name, Kind: LateKindGone, Count: count})
			}
			for _, x := range held {
				if err := l.gone(k.Name, x); err != nil {
					return err
				}
			}
			return nil
		}
	}
	for e != nil {
		c := cls(e)
		types = append(types, classTypes(c)...)
		if err := runClass(c, nil); err != nil {
			return err
		}
	}
	other := base
	if len(types) > 0 {
		other = and(base, bson.M{"_id": bson.M{"$not": bson.M{"$type": types}}})
	}
	l.pos.InOther, l.pos.Key = true, nil
	l.pos.Other = otherSkip
	cur, err := l.old.C(k.Name).Find(l.ctx, other, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetSkip(otherSkip).SetBatchSize(wsarchive.FindBatch))
	if err != nil {
		return err
	}
	defer cur.Close(l.ctx)
	for cur.Next(l.ctx) {
		if err := l.found(k.Name, cur.Current, "", false); err != nil {
			return err
		}
		l.pos.Other++
		if err := l.account(len(cur.Current)); err != nil {
			return err
		}
	}
	return cur.Err()
}

// merge walks one class of _id on both sides, in order.
func (l *looker) merge(coll string, cls byte, cur *mongo.Cursor, e **printEntry, next func() error, ttl bool) error {
	have := cur.Next(l.ctx)
	for {
		inClass := *e != nil && idClass(bson.Type((*e).key[0])) == cls
		if !have && !inClass {
			return cur.Err()
		}
		var c int
		var doc bson.Raw
		var key []byte
		if have {
			doc = cur.Current
			k, err := idKey(doc)
			if err != nil {
				return err
			}
			key = k
		}
		switch {
		case !have:
			c = -1 // only a fingerprint left: gone
		case !inClass:
			c = 1 // only the old database's record: new
		default:
			n, err := compareIDs((*e).key, key)
			if err != nil {
				return err
			}
			c = n
		}
		switch {
		case c < 0:
			x := *e
			if err := next(); err != nil {
				return err
			}
			if !ttl {
				if err := l.gone(coll, x); err != nil {
					return err
				}
			}
		case c > 0:
			if err := l.found(coll, doc, "", false); err != nil {
				return err
			}
			l.pos.Key = key
			size := len(doc)
			have = cur.Next(l.ctx)
			if err := l.account(size); err != nil {
				return err
			}
		default:
			if err := l.found(coll, doc, (*e).print, true); err != nil {
				return err
			}
			l.pos.Key = key
			size := len(doc)
			if err := next(); err != nil {
				return err
			}
			have = cur.Next(l.ctx)
			if err := l.account(size); err != nil {
				return err
			}
		}
	}
}

// ordered reports a class of _id a look puts in MongoDB's order by itself
// (all but documents, arrays, patterns and code as _ids, which MetaRush never
// makes).
func ordered(cls byte) bool {
	switch cls {
	case 'n', 's', byte(bson.TypeObjectID), byte(bson.TypeBoolean), byte(bson.TypeDateTime), byte(bson.TypeTimestamp), byte(bson.TypeBinary),
		byte(bson.TypeNull), byte(bson.TypeMinKey), byte(bson.TypeMaxKey), byte(bson.TypeUndefined):
		return true
	}
	return false
}

// MaxUnordered is how many fingerprints of a class of _id a look can't order
// it holds at once; past it the look stops and says so.
var MaxUnordered = 100_000

// unordered compares a class of _id a look can't order by itself: its
// fingerprints held (at most MaxUnordered), the old database's records read
// once against them. A look that stops here starts this class again.
func (l *looker) unordered(coll string, cls byte, cur *mongo.Cursor, e **printEntry, next func() error, ttl bool) error {
	prints := map[string]string{}
	for *e != nil && idClass(bson.Type((*e).key[0])) == cls {
		if len(prints) >= MaxUnordered {
			l.res.Stopped = "at a kind of record with unusual IDs, too many to compare"
			return errStop
		}
		prints[string((*e).key)] = (*e).print
		if err := next(); err != nil {
			return err
		}
	}
	for cur.Next(l.ctx) {
		key, err := idKey(cur.Current)
		if err != nil {
			return err
		}
		was, known := prints[string(key)]
		delete(prints, string(key))
		if err := l.found(coll, cur.Current, was, known); err != nil {
			return err
		}
		if err := l.account(len(cur.Current)); err != nil {
			return err
		}
	}
	if err := cur.Err(); err != nil || ttl {
		return err
	}
	keys := make([]string, 0, len(prints))
	for k := range prints {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		kb := []byte(k)
		if err := l.add(Late{Collection: coll, ID: IDOf(kb), Key: kb, Kind: LateGone, MovePrint: prints[k]}); err != nil {
			return err
		}
		if err := l.account(removedCost); err != nil {
			return err
		}
	}
	return nil
}

// found compares one record of the old database with its fingerprint (known
// false: it has none).
func (l *looker) found(coll string, doc bson.Raw, was string, known bool) error {
	line, err := wsarchive.Line(doc)
	if err != nil {
		return err
	}
	now := printOfLine(line)
	if known && was == now {
		return nil
	}
	key, err := idKey(doc)
	if err != nil {
		return err
	}
	x := Late{Collection: coll, ID: IDOf(key), Key: key, Kind: LateNew, Doc: bytes.Clone(doc), Print: now}
	if known {
		x.Kind, x.MovePrint = LateChanged, was
	}
	return l.add(x)
}

// ErrIDOrder is an _id MetaRush can't put in MongoDB's order by itself (a
// document or an array as an _id).
var ErrIDOrder = errors.New("a record's _id is of a kind a look can't order")

// compareIDs orders two _ids of one class the way MongoDB sorts them.
func compareIDs(a, b []byte) (int, error) {
	va, vb := IDOf(a), IDOf(b)
	switch idClass(va.Type) {
	case 'n':
		return compareNumbers(va, vb)
	case 's':
		return strings.Compare(rawString(va), rawString(vb)), nil
	}
	switch va.Type {
	case bson.TypeObjectID, bson.TypeBoolean:
		return bytes.Compare(va.Value, vb.Value), nil
	case bson.TypeDateTime:
		return cmpInt(va.DateTime(), vb.DateTime()), nil
	case bson.TypeTimestamp:
		ta, ia := va.Timestamp()
		tb, ib := vb.Timestamp()
		if ta != tb {
			return cmpInt(int64(ta), int64(tb)), nil
		}
		return cmpInt(int64(ia), int64(ib)), nil
	case bson.TypeBinary:
		sa, ba := va.Binary()
		sb, bb := vb.Binary()
		if len(ba) != len(bb) {
			return cmpInt(int64(len(ba)), int64(len(bb))), nil
		}
		if sa != sb {
			return cmpInt(int64(sa), int64(sb)), nil
		}
		return bytes.Compare(ba, bb), nil
	case bson.TypeNull, bson.TypeMinKey, bson.TypeMaxKey, bson.TypeUndefined:
		return 0, nil
	}
	return 0, ErrIDOrder
}

func rawString(v bson.RawValue) string {
	if s, ok := v.StringValueOK(); ok {
		return s
	}
	if s, ok := v.SymbolOK(); ok {
		return s
	}
	return ""
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// compareNumbers orders two numbers by value, whatever their BSON type.
func compareNumbers(a, b bson.RawValue) (int, error) {
	ia, aInt := intOf(a)
	ib, bInt := intOf(b)
	if aInt && bInt {
		return cmpInt(ia, ib), nil
	}
	fa, err := floatOf(a)
	if err != nil {
		return 0, err
	}
	fb, err := floatOf(b)
	if err != nil {
		return 0, err
	}
	return fa.Cmp(fb), nil
}

func intOf(v bson.RawValue) (int64, bool) {
	switch v.Type {
	case bson.TypeInt32:
		return int64(v.Int32()), true
	case bson.TypeInt64:
		return v.Int64(), true
	}
	return 0, false
}

func floatOf(v bson.RawValue) (*big.Float, error) {
	switch v.Type {
	case bson.TypeInt32, bson.TypeInt64:
		i, _ := intOf(v)
		return new(big.Float).SetInt64(i), nil
	case bson.TypeDouble:
		return big.NewFloat(v.Double()), nil
	case bson.TypeDecimal128:
		f, _, err := big.ParseFloat(v.Decimal128().String(), 10, 113, big.ToNearestEven)
		if err != nil {
			return nil, ErrIDOrder
		}
		return f, nil
	}
	return nil, ErrIDOrder
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

// ServerHashesWork reports whether a database's server says its own hash of
// a kind of record (dbHash; Atlas's free and Flex clusters don't): when it
// doesn't, every look after a move reads the whole old database again. It
// asks about a kind that doesn't exist, so the server reads nothing.
func ServerHashesWork(ctx context.Context, db *store.DB) bool {
	return serverHashes(ctx, db, []string{"metarush_no_such_kind"}) != nil
}
