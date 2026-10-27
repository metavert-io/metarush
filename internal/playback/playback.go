// Package playback remembers what the dashboard's media endpoints need to
// stream each clip's files and each render — where the bytes are stored, how
// many there are, their type and SHA-256, and the record's version — so
// playback keeps going through a short outage of the database (D180).
//
// The database stays the authority. Every lookup asks it first, with a short
// deadline (from any replica-set member while there is no primary), and its
// answer replaces what is remembered. The memory answers only when the
// database couldn't — it failed, it took too long, or it failed a moment ago —
// and only with what the database last said:
//
//   - "not found" forgets at once, and a clip in the Trash is never remembered;
//   - a change to the record (the dashboard's change stream or poller calls
//     Changed) drops it, and it is read again in the background;
//   - what the database said longer ago than MaxAge is not used, an answer
//     that was on its way while the record changed is not kept, and an answer
//     older than the remembered version (a member that lags) doesn't replace it.
//
// Who may ask is decided before a lookup: the dashboard's Host check and
// sign-in run first, and the endpoints apply the same rules to a remembered
// record as to a fresh one (the clip is ready, the copy exists). Memories live
// in this process only, one per workspace, bounded (Capacity, least recently
// used out).
package playback

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

const (
	// Capacity is how many clips and renders a workspace's memory holds.
	Capacity = 2048
	// MaxAge: what the database said longer ago than this is not used.
	MaxAge = time.Hour
	// Wait is how long a lookup waits for the database when nothing is
	// remembered (only the database can answer it). Before D180 a lookup
	// waited for the driver's server selection (8 s) and then said the clip
	// didn't exist.
	Wait = 3 * time.Second
	// QuickWait is how long a lookup waits when the memory could answer instead.
	QuickWait = 750 * time.Millisecond
	// DownWait is how long a lookup with nothing remembered waits while the
	// database is failing.
	DownWait = time.Second
	// Quiet: after the database failed, lookups the memory can answer don't
	// ask it for this long; then one of them asks again while the others keep
	// using the memory.
	Quiet = 3 * time.Second
)

// ErrNotFound means the database says the workspace has no such clip or render.
var ErrNotFound = store.ErrNotFound

// From says where an answer came from.
type From int

const (
	// FromDatabase: the database answered.
	FromDatabase From = iota
	// FromMemory: the database didn't, and the memory did.
	FromMemory
)

func (f From) String() string {
	if f == FromMemory {
		return "memory"
	}
	return "database"
}

// Object is one stored file.
type Object struct {
	Key    string
	SHA256 string
	MIME   string
	Size   int64
	// Kind is a copy's kind (proxy, thumbnail…; "" for the original): the
	// file's name for a download ends with it.
	Kind string
}

// Asset is what playing a clip needs from its record.
type Asset struct {
	ID, Title, Name string
	// State is the clip's state; its own file plays once it is models.AssetAvailable.
	State              string
	HasVideo, HasAudio bool
	Duration           float64
	Original           Object
	// Variants are the copies made from the media the clip uses now, by kind.
	Variants map[string]Object
	// Copy is the fixed copy the clip plays now (D152; nil while it plays its
	// original), and OriginalVariants the original's own copies, set aside
	// while a fixed copy is current (?media=original serves them).
	Copy             *Object
	OriginalVariants map[string]Object
	// Version is when the record last changed (its updatedAt).
	Version time.Time
}

// Ready reports whether the clip's own file can be played.
func (a *Asset) Ready() bool { return a.State == models.AssetAvailable }

// Render is what playing a render needs from its record.
type Render struct {
	ID, Preset     string
	RevisionNumber int
	// Output is the rendered video (nil until the render is ready).
	Output *Object
	// ThumbnailKey is its picture's storage key ("" when it has none).
	ThumbnailKey string
	// Version is when it was finished.
	Version time.Time
}

// AssetFor looks up what playing a clip needs (see the package comment for
// when the memory answers instead of the database). Errors: ErrNotFound, an
// E_DATABASE error in plain words when the database didn't answer and nothing
// is remembered, or ctx's error when the one asking went away.
func AssetFor(ctx context.Context, a *app.App, id string) (*Asset, From, error) {
	k := key{kindAsset, id}
	rec, from, err := memoryFor(a.WorkspaceID()).lookup(ctx, k, readerFor(a, k))
	if err != nil {
		return nil, from, err
	}
	return rec.(*Asset), from, nil
}

// RenderFor looks up what playing a render needs, like AssetFor.
func RenderFor(ctx context.Context, a *app.App, id string) (*Render, From, error) {
	k := key{kindRender, id}
	rec, from, err := memoryFor(a.WorkspaceID()).lookup(ctx, k, readerFor(a, k))
	if err != nil {
		return nil, from, err
	}
	return rec.(*Render), from, nil
}

// CopyFor reads one fixed copy of a clip (D152), for a version that pins a
// copy the clip no longer plays. Copies aren't remembered; the database is
// asked with the deadline of a lookup with nothing remembered (DownWait while
// it is failing). Errors: ErrNotFound, an E_DATABASE error in plain words when
// the database didn't answer, or ctx's error when the one asking went away.
func CopyFor(ctx context.Context, a *app.App, assetID, sha string) (*models.SourceCopy, error) {
	m := memoryFor(a.WorkspaceID())
	_, wait, done := m.plan(false)
	defer done()
	dctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	var c models.SourceCopy
	err := a.DB.FindOneAnyMember(dctx, store.CSourceCopies, a.WorkspaceID(), bson.M{"assetId": assetID, "sha256": sha}, &c)
	switch {
	case err == nil:
		m.answered()
		return &c, nil
	case errors.Is(err, store.ErrNotFound):
		m.answered()
		return nil, ErrNotFound
	case ctx.Err() != nil:
		return nil, ctx.Err()
	}
	m.failed()
	return nil, unreachable(err)
}

// Remember keeps clips' records just read from the database (readAt: when
// that read began), so they play through an outage before anyone asked for
// them: preview.prepare does this for the cut it gets ready. Clips in the
// Trash are not kept.
func Remember(a *app.App, readAt time.Time, assets ...models.Asset) {
	m := memoryFor(a.WorkspaceID())
	for i := range assets {
		as := &assets[i]
		k := key{kindAsset, as.ID}
		if as.Trash != nil {
			m.forget(k)
			continue
		}
		m.keep(k, assetRecord(as), as.UpdatedAt, readAt)
	}
}

// Changed tells the memory that a document changed (coll is its collection:
// assets or renders; others are ignored): what it remembers about it is
// dropped at once and, if it was remembered, read again in the background,
// so it stays what the database says (a deleted record, or a clip moved to
// the Trash, stays forgotten).
func Changed(a *app.App, coll, id string) {
	var k key
	switch coll {
	case store.CAssets:
		k = key{kindAsset, id}
	case store.CRenders:
		k = key{kindRender, id}
	default:
		return
	}
	if id == "" {
		return
	}
	m := memoryFor(a.WorkspaceID())
	if had := m.forget(k); had || m.rereading(k) {
		m.rereadSoon(k, readerFor(a, k))
	}
}

// Remembered reports how many clips and renders a workspace's memory holds.
func Remembered(a *app.App) int {
	m := memoryFor(a.WorkspaceID())
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

func unreachable(cause error) error {
	return apperr.Wrap(apperr.Database, cause, "MetaRush can't reach its database right now, so it can't start playing this yet.").
		WithHint("The dashboard keeps trying and plays it as soon as the database answers again.").
		WithDetails(map[string]any{"reason": "database_unreachable"})
}

func assetRecord(as *models.Asset) *Asset {
	r := &Asset{ID: as.ID, Title: as.DisplayTitle(), Name: as.Name, State: as.State, HasVideo: as.Media.HasVideo, HasAudio: as.Media.HasAudio,
		Duration: as.Media.Duration, Original: Object{Key: as.StorageKey, SHA256: as.SHA256, MIME: as.MIME, Size: as.Size},
		Variants: Objects(as.Variants), Version: as.UpdatedAt}
	if as.Fixed() {
		c := as.Fixes.Copy
		r.Copy = &Object{Key: c.StorageKey, SHA256: c.SHA256, MIME: c.MIME, Size: c.Size}
		r.OriginalVariants = Objects(as.Fixes.OriginalVariants)
	}
	return r
}

// Objects are a clip's stored copies (variants) as playing needs them.
func Objects(vs map[string]models.Variant) map[string]Object {
	out := make(map[string]Object, len(vs))
	for kind, v := range vs {
		out[kind] = Object{Key: v.StorageKey, SHA256: v.SHA256, MIME: v.MIME, Size: v.Size, Kind: v.Kind}
	}
	return out
}

func renderRecord(rd *models.Render) *Render {
	r := &Render{ID: rd.ID, Preset: rd.Preset, RevisionNumber: rd.RevisionNumber, Version: rd.CreatedAt}
	if rd.CompletedAt != nil {
		r.Version = *rd.CompletedAt
	}
	if o := rd.Output; o != nil {
		r.Output = &Object{Key: o.StorageKey, SHA256: o.SHA256, MIME: "video/mp4", Size: o.Size}
		r.ThumbnailKey = o.ThumbnailKey
	}
	return r
}

// reader asks the database: the record, its version and whether it may be
// remembered.
type reader func(ctx context.Context) (rec any, version time.Time, keep bool, err error)

func readerFor(a *app.App, k key) reader {
	return func(ctx context.Context) (any, time.Time, bool, error) {
		if k.kind == kindRender {
			var rd models.Render
			if err := a.DB.GetByIDAnyMember(ctx, store.CRenders, a.WorkspaceID(), k.id, &rd); err != nil {
				return nil, time.Time{}, false, err
			}
			r := renderRecord(&rd)
			return r, r.Version, r.Output != nil, nil // a render being made changes soon: nothing to remember yet
		}
		var as models.Asset
		if err := a.DB.GetByIDAnyMember(ctx, store.CAssets, a.WorkspaceID(), k.id, &as); err != nil {
			return nil, time.Time{}, false, err
		}
		return assetRecord(&as), as.UpdatedAt, as.Trash == nil, nil
	}
}

/* ---------- the memory ---------- */

type kind byte

const (
	kindAsset  kind = 'a'
	kindRender kind = 'r'
)

type key struct {
	kind kind
	id   string
}

type entry struct {
	k       key
	rec     any // *Asset or *Render
	version time.Time
	at      time.Time // when the database said so
	el      *list.Element
}

type memory struct {
	mu      sync.Mutex
	entries map[key]*entry
	lru     *list.List // front: used most recently
	// changed is when each record last changed: answers read before that are not kept.
	changed map[key]time.Time
	// again marks records being read again after a change (true: read once more, it changed meanwhile).
	again map[key]bool
	// failedAt is the database's latest failure (zero: it answered last).
	failedAt time.Time
	// asking: a lookup is asking the database on everyone's behalf while it fails.
	asking bool

	capacity                                      int
	maxAge, wait, quickWait, downWait, quiet, lag time.Duration
	now                                           func() time.Time
}

var (
	memoriesMu sync.Mutex
	memories   = map[string]*memory{}
)

func memoryFor(workspaceID string) *memory {
	memoriesMu.Lock()
	defer memoriesMu.Unlock()
	m := memories[workspaceID]
	if m == nil {
		m = newMemory()
		memories[workspaceID] = m
	}
	return m
}

func newMemory() *memory {
	return &memory{entries: map[key]*entry{}, lru: list.New(), changed: map[key]time.Time{}, again: map[key]bool{},
		capacity: Capacity, maxAge: MaxAge, wait: Wait, quickWait: QuickWait, downWait: DownWait, quiet: Quiet,
		lag: 400 * time.Millisecond, now: time.Now}
}

// lookup asks the database (or not, while it is failing and the memory can
// answer), keeps its answer, and falls back on the memory when it didn't answer.
func (m *memory) lookup(ctx context.Context, k key, read reader) (any, From, error) {
	rec, have := m.recall(k)
	ask, wait, done := m.plan(have)
	if !ask {
		return rec, FromMemory, nil
	}
	defer done()
	started := m.now()
	dctx, cancel := context.WithTimeout(ctx, wait)
	got, version, keep, err := read(dctx)
	cancel()
	switch {
	case err == nil:
		m.answered()
		if !keep {
			m.forget(k) // in the Trash (or a render being made): served as before, never from memory
			return got, FromDatabase, nil
		}
		return m.keep(k, got, version, started), FromDatabase, nil
	case errors.Is(err, store.ErrNotFound):
		m.answered()
		m.forget(k)
		return nil, FromDatabase, ErrNotFound
	case ctx.Err() != nil:
		// The one asking went away: that says nothing about the database.
		return nil, FromDatabase, ctx.Err()
	}
	m.failed()
	if rec, have := m.recall(k); have {
		return rec, FromMemory, nil
	}
	return nil, FromDatabase, unreachable(err)
}

// plan decides whether a lookup asks the database and how long it waits.
func (m *memory) plan(have bool) (ask bool, wait time.Duration, done func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	nothing := func() {}
	switch {
	case m.failedAt.IsZero() && have:
		return true, m.quickWait, nothing
	case m.failedAt.IsZero():
		return true, m.wait, nothing
	case !have:
		return true, m.downWait, nothing
	case m.asking || m.now().Sub(m.failedAt) < m.quiet:
		return false, 0, nothing
	}
	m.asking = true
	return true, m.quickWait, func() {
		m.mu.Lock()
		m.asking = false
		m.mu.Unlock()
	}
}

func (m *memory) answered() {
	m.mu.Lock()
	m.failedAt = time.Time{}
	m.mu.Unlock()
}

func (m *memory) failed() {
	m.mu.Lock()
	m.failedAt = m.now()
	m.mu.Unlock()
}

// recall returns what is remembered about k, unless the database said it too long ago.
func (m *memory) recall(k key) (any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[k]
	if !ok {
		return nil, false
	}
	if m.now().Sub(e.at) > m.maxAge {
		m.removeLocked(e)
		return nil, false
	}
	m.lru.MoveToFront(e.el)
	return e.rec, true
}

// keep remembers the database's answer (read from started on) and returns the
// record to use: the remembered one when the answer is older than it.
func (m *memory) keep(k key, rec any, version, started time.Time) any {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.changed[k]; ok && !started.After(c) {
		return rec // the record changed while this answer was on its way: it may be older than the change
	}
	now := m.now()
	if e, ok := m.entries[k]; ok {
		if version.Before(e.version) {
			return e.rec // a member that lags behind answered with an older record
		}
		e.rec, e.version, e.at = rec, version, now
		m.lru.MoveToFront(e.el)
		return rec
	}
	e := &entry{k: k, rec: rec, version: version, at: now}
	e.el = m.lru.PushFront(e)
	m.entries[k] = e
	for m.lru.Len() > m.capacity {
		m.removeLocked(m.lru.Back().Value.(*entry))
	}
	return rec
}

// forget drops what is remembered about k because it changed (answers read
// before now are not kept) and reports whether anything was remembered.
func (m *memory) forget(k key) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.changed[k] = now
	if len(m.changed) > 1024 {
		for ck, at := range m.changed {
			if now.Sub(at) > time.Minute { // every lookup that read before it has long given up
				delete(m.changed, ck)
			}
		}
	}
	e, ok := m.entries[k]
	if ok {
		m.removeLocked(e)
	}
	return ok
}

func (m *memory) removeLocked(e *entry) {
	m.lru.Remove(e.el)
	delete(m.entries, e.k)
}

func (m *memory) rereading(k key) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, busy := m.again[k]
	return busy
}

// rereadSoon reads a changed record again in the background, a moment after
// the change (changes come in bursts); a change while it reads makes it read
// once more.
func (m *memory) rereadSoon(k key, read reader) {
	m.mu.Lock()
	if _, busy := m.again[k]; busy {
		m.again[k] = true
		m.mu.Unlock()
		return
	}
	m.again[k] = false
	m.mu.Unlock()
	go func() {
		for {
			time.Sleep(m.lag)
			m.mu.Lock()
			m.again[k] = false
			m.mu.Unlock()
			started := m.now()
			ctx, cancel := context.WithTimeout(context.Background(), m.wait)
			rec, version, keep, err := read(ctx)
			cancel()
			if err == nil && keep {
				m.keep(k, rec, version, started)
			}
			// Gone, in the Trash, or the database didn't answer: it stays
			// forgotten (a change that can't be confirmed is never guessed).
			m.mu.Lock()
			again := m.again[k]
			if !again {
				delete(m.again, k)
			}
			m.mu.Unlock()
			if !again {
				return
			}
		}
	}()
}
