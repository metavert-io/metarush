package transcripts

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// A saved version of a cut renders the same words forever (D146): when it is
// saved, each clip's transcript as captions and cuts read it (the engine
// view: lines, words, times, who speaks) is hashed and kept once as a
// snapshot, and the version records the hash with the clip's transcript
// revision (models.TranscriptPin). Planning a saved version reads its
// snapshots; an unsaved change reads the transcripts as they are. A version
// saved before pins existed (or before a clip had a transcript) reads the
// clip's machine transcript as it was before the first fix, which is the
// transcript as it is until someone fixes it.

// Engine turns stored lines into what captions and cuts read: each line's
// words with their times and who says it (the name someone gave the voice, or
// "Speaker 2" when several voices are not named yet).
func Engine(segs []models.TranscriptSegment) []editspec.TranscriptSegment {
	out := make([]editspec.TranscriptSegment, 0, len(segs))
	voices := map[string]bool{}
	for _, s := range segs {
		if s.Speaker != "" {
			voices[s.Speaker] = true
		}
	}
	for _, s := range segs {
		who := s.SpeakerName
		if who == "" && len(voices) > 1 {
			who = models.SpeakerDisplayLabel(s.Speaker)
		}
		ts := editspec.TranscriptSegment{From: s.From, To: s.To, Text: s.Text, Speaker: who}
		for _, w := range s.Words {
			ts.Words = append(ts.Words, editspec.TranscriptWord{Text: w.Text, From: w.From, To: w.To})
		}
		out = append(out, ts)
	}
	return out
}

// snapshot is the canonical form of an engine view: what is hashed and kept.
type snapshot struct {
	Precision string    `json:"p"`
	Lines     []snapSeg `json:"s"`
}

type snapSeg struct {
	F float64    `json:"f"`
	T float64    `json:"t"`
	X string     `json:"x"`
	S string     `json:"s,omitempty"`
	W []snapWord `json:"w,omitempty"`
}

type snapWord struct {
	W string  `json:"w"`
	F float64 `json:"f"`
	T float64 `json:"t"`
}

func toSnapshot(segs []editspec.TranscriptSegment, precision string) snapshot {
	s := snapshot{Precision: precision, Lines: make([]snapSeg, len(segs))}
	for i, g := range segs {
		l := snapSeg{F: g.From, T: g.To, X: g.Text, S: g.Speaker}
		for _, w := range g.Words {
			l.W = append(l.W, snapWord{W: w.Text, F: w.From, T: w.To})
		}
		s.Lines[i] = l
	}
	return s
}

func (s snapshot) engine() []editspec.TranscriptSegment {
	out := make([]editspec.TranscriptSegment, len(s.Lines))
	for i, l := range s.Lines {
		g := editspec.TranscriptSegment{From: l.F, To: l.T, Text: l.X, Speaker: l.S}
		for _, w := range l.W {
			g.Words = append(g.Words, editspec.TranscriptWord{Text: w.W, From: w.F, To: w.T})
		}
		out[i] = g
	}
	return out
}

// Hash is the content hash of a clip's engine view (with its precision).
func Hash(segs []editspec.TranscriptSegment, precision string) string {
	h, _, _ := snapshotHash(segs, precision)
	return h
}

// snapDoc is a kept engine view (store.CTranscriptSnapshots).
type snapDoc struct {
	ID          string    `bson:"_id"`
	WorkspaceID string    `bson:"workspaceId"`
	AssetID     string    `bson:"assetId"`
	Hash        string    `bson:"hash"`
	Precision   string    `bson:"precision,omitempty"`
	Lines       int       `bson:"lines"`
	Words       int       `bson:"words"`
	Data        []byte    `bson:"data"` // gzip'd canonical JSON
	CreatedAt   time.Time `bson:"createdAt"`
}

// keptDoc is a machine transcript kept from before the first fix to it
// (store.CTranscriptOriginals).
type keptDoc struct {
	ID          string    `bson:"_id"`
	WorkspaceID string    `bson:"workspaceId"`
	AssetID     string    `bson:"assetId"`
	Base        int       `bson:"base"`
	Provider    string    `bson:"provider,omitempty"`
	Model       string    `bson:"model,omitempty"`
	Language    string    `bson:"language,omitempty"`
	Precision   string    `bson:"precision,omitempty"`
	Lines       int       `bson:"lines"`
	Words       int       `bson:"words"`
	Data        []byte    `bson:"data"` // gzip'd BSON {segs: […]}
	KeptAt      time.Time `bson:"keptAt"`
}

type keptSegs struct {
	Segs []models.TranscriptSegment `bson:"segs"`
}

func gz(b []byte) []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return buf.Bytes()
}

func gunzip(b []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(io.LimitReader(zr, 512<<20))
}

func countWords(segs []models.TranscriptSegment) int {
	n := 0
	for _, s := range segs {
		n += len(s.Words)
	}
	return n
}

// keepOriginal keeps a clip's machine transcript (once per machine transcript).
func keepOriginal(ctx context.Context, a *app.App, as *models.Asset, base int, segs []models.TranscriptSegment) error {
	if n, err := a.DB.C(store.CTranscriptOriginals).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": as.ID, "base": base}); err != nil || n > 0 {
		return err
	}
	raw, err := bson.Marshal(keptSegs{Segs: segs})
	if err != nil {
		return err
	}
	d := keptDoc{ID: ids.New(ids.TranscriptOriginal), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Base: base, Lines: len(segs), Words: countWords(segs),
		Data: gz(raw), KeptAt: time.Now().UTC()}
	if st := as.Transcript; st != nil {
		d.Provider, d.Model, d.Language, d.Precision = st.Provider, st.Model, st.Language, st.Precision
	}
	if err := a.DB.Insert(ctx, store.CTranscriptOriginals, d); err != nil && !store.IsDuplicateKey(err) {
		return err
	}
	return nil
}

// Original is a clip's machine transcript as it was before anyone fixed it.
type Original struct {
	Base      int
	Provider  string
	Model     string
	Precision string
	KeptAt    time.Time
	Segments  []models.TranscriptSegment
}

// LoadOriginal returns a kept machine transcript (store.ErrNotFound when the
// clip's transcript was never changed: it is then the transcript as it is).
func LoadOriginal(ctx context.Context, a *app.App, assetID string, base int) (*Original, error) {
	var d keptDoc
	if err := a.DB.C(store.CTranscriptOriginals).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": assetID, "base": base}).Decode(&d); err != nil {
		return nil, notFound(err)
	}
	raw, err := gunzip(d.Data)
	if err != nil {
		return nil, err
	}
	var k keptSegs
	if err := bson.Unmarshal(raw, &k); err != nil {
		return nil, err
	}
	return &Original{Base: d.Base, Provider: d.Provider, Model: d.Model, Precision: d.Precision, KeptAt: d.KeptAt, Segments: k.Segs}, nil
}

func notFound(err error) error {
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, mongo.ErrNoDocuments) {
		return store.ErrNotFound
	}
	return err
}

// Pins are the transcripts a saved version of a cut was made with.
type Pins struct {
	by map[string]models.TranscriptPin
}

// PinsOf returns a saved version's pins. A version saved before pins existed
// has none: each of its clips reads its machine transcript as it was before
// the first fix.
func PinsOf(r *models.Revision) *Pins {
	p := &Pins{by: map[string]models.TranscriptPin{}}
	if r == nil {
		return p
	}
	for _, t := range r.Transcripts {
		p.by[t.AssetID] = t
	}
	return p
}

// Pin returns the pin for a clip.
func (p *Pins) Pin(assetID string) (models.TranscriptPin, bool) {
	if p == nil {
		return models.TranscriptPin{}, false
	}
	t, ok := p.by[assetID]
	return t, ok
}

// For returns a clip's words for planning: the saved version's words when
// pins are given (its snapshot, or the machine transcript from before the
// first fix when it has no pin for the clip), else the transcript as it is
// now. ok is false when there are no words to give (no transcript yet).
func For(ctx context.Context, a *app.App, as *models.Asset, pins *Pins) (segs []editspec.TranscriptSegment, precision string, ok bool, err error) {
	if pins != nil {
		if p, has := pins.by[as.ID]; has {
			s, err := loadSnapshot(ctx, a, p.Hash)
			if err == nil {
				return s.engine(), s.Precision, true, nil
			}
			if !errors.Is(err, store.ErrNotFound) {
				return nil, "", false, err
			}
		} else {
			o, err := LoadOriginal(ctx, a, as.ID, 1)
			if err == nil {
				prec := o.Precision
				if prec == "" && len(o.Segments) > 0 {
					prec = "segment"
					if countWords(o.Segments) > 0 {
						prec = "word"
					}
				}
				return Engine(o.Segments), prec, true, nil
			}
			if !errors.Is(err, store.ErrNotFound) {
				return nil, "", false, err
			}
		}
	}
	if as.Transcript == nil || as.Transcript.State != "available" {
		return nil, "", false, nil
	}
	stored, err := LoadSegments(ctx, a, as.ID)
	if err != nil {
		return nil, "", false, err
	}
	return Engine(stored), as.Transcript.Precision, true, nil
}

func loadSnapshot(ctx context.Context, a *app.App, hash string) (*snapshot, error) {
	var d snapDoc
	if err := a.DB.C(store.CTranscriptSnapshots).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "hash": hash}).Decode(&d); err != nil {
		return nil, notFound(err)
	}
	raw, err := gunzip(d.Data)
	if err != nil {
		return nil, err
	}
	var s snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Pin keeps the words a version is being saved with: one pin per clip that
// has a transcript (tx: the clips' engine views as the save checked them;
// precision and rev: each clip's), keeping each distinct set of words once.
func Pin(ctx context.Context, a *app.App, tx map[string][]editspec.TranscriptSegment, precision map[string]string, rev map[string]int) ([]models.TranscriptPin, error) {
	keys := make([]string, 0, len(tx))
	for id := range tx {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	out := make([]models.TranscriptPin, 0, len(keys))
	for _, id := range keys {
		segs := tx[id]
		h, b, err := snapshotHash(segs, precision[id])
		if err != nil {
			return nil, err
		}
		n, err := a.DB.C(store.CTranscriptSnapshots).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "hash": h})
		if err != nil {
			return nil, err
		}
		if n == 0 {
			words := 0
			for _, g := range segs {
				words += len(g.Words)
			}
			d := snapDoc{ID: ids.New(ids.TranscriptSnapshot), WorkspaceID: a.WorkspaceID(), AssetID: id, Hash: h, Precision: precision[id], Lines: len(segs), Words: words,
				Data: gz(b), CreatedAt: time.Now().UTC()}
			if err := a.DB.Insert(ctx, store.CTranscriptSnapshots, d); err != nil && !store.IsDuplicateKey(err) {
				return nil, err
			}
		}
		r, err := settleRev(ctx, a, id, rev[id], h, precision[id])
		if err != nil {
			return nil, err
		}
		out = append(out, models.TranscriptPin{AssetID: id, Rev: r, Hash: h})
	}
	return out, nil
}

// snapshotHash is the hash a clip's words are kept under, with their canonical form.
func snapshotHash(segs []editspec.TranscriptSegment, precision string) (string, []byte, error) {
	b, err := json.Marshal(toSnapshot(segs, precision))
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), b, nil
}

// settleWait is how long a version being saved waits for a fix under way on
// one of its clips before it keeps the revision it read.
var settleWait = 5 * time.Second

// textNow is a clip's transcript revision and whether a fix is changing it.
type textNow struct {
	rev  int
	busy bool
}

func textState(ctx context.Context, a *app.App, assetID string) (textNow, error) {
	var d struct {
		TextFixes *models.TranscriptState `bson:"textFixes"`
	}
	if err := a.DB.C(store.CAssets).FindOne(ctx, bson.M{"_id": assetID, "workspaceId": a.WorkspaceID()},
		options.FindOne().SetProjection(bson.M{"textFixes.rev": 1, "textFixes.lock": 1})).Decode(&d); err != nil {
		return textNow{}, notFound(err)
	}
	if d.TextFixes == nil {
		return textNow{}, nil
	}
	l := d.TextFixes.Lock
	if l == nil {
		return textNow{rev: d.TextFixes.Rev}, nil
	}
	// The lease ends by the database server's clock (lock): another
	// computer's fix is under way until then, whatever this clock says.
	now, err := a.DB.Now(ctx)
	if err != nil {
		return textNow{}, err
	}
	return textNow{rev: d.TextFixes.Rev, busy: l.Until.After(now)}, nil
}

// settleRev says which transcript revision the words a version is saved with
// belong to (D145, D146). read was read before the words, and a fix writes a
// clip's lines before it moves the clip's revision (Clip.commit), so words
// read while a fix lands can be newer than read. While the clip is still at
// read with nothing changing it, read is right. Otherwise its words are read
// again once nothing is changing them: the same words belong to the newer
// revision; different words keep read, so the fixes since stay offered for a
// new version (PendingFor) rather than being said to be in this one.
func settleRev(ctx context.Context, a *app.App, assetID string, read int, hash, precision string) (int, error) {
	deadline := time.Now().Add(settleWait)
	for first := true; ; first = false {
		before, err := textState(ctx, a, assetID)
		if errors.Is(err, store.ErrNotFound) {
			return read, nil
		}
		if err != nil {
			return 0, err
		}
		if first && before.rev == read && !before.busy {
			return read, nil
		}
		if !before.busy {
			segs, err := LoadSegments(ctx, a, assetID)
			if err != nil {
				return 0, err
			}
			after, err := textState(ctx, a, assetID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return 0, err
			}
			if after == before {
				if Hash(Engine(segs), precision) == hash {
					return before.rev, nil
				}
				return read, nil
			}
		}
		if time.Now().After(deadline) {
			return read, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
