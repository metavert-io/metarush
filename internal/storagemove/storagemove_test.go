package storagemove

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/metavert-io/metarush/internal/cache"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/testutil/gcsemu"
)

const ws = "wsp_move"

// The S3 and GCS stores here are in-process emulators (gofakes3;
// internal/testutil/gcsemu), the same ones the media drivers are tested with.

func newFS(t *testing.T) *media.FS {
	t.Helper()
	fs, err := media.NewFS(filepath.Join(t.TempDir(), "media"), false)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func newS3(t *testing.T) *media.S3 {
	t.Helper()
	backend := s3mem.New()
	srv := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(srv.Close)
	if err := backend.CreateBucket("media"); err != nil {
		t.Fatal(err)
	}
	s, err := media.NewS3(media.S3Config{Endpoint: srv.URL, Region: "us-east-1", Bucket: "media", Prefix: "team", AccessKeyID: "test", SecretAccessKey: "test", ForcePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newGCS(t *testing.T) (*media.GCS, *gcsemu.Server) {
	t.Helper()
	emu := gcsemu.Start(t, "media")
	g, err := media.NewGCS(media.GCSConfig{Bucket: "media", Prefix: "team", Endpoint: emu.URL, NoAuth: true, ChunkSize: 256 << 10, RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return g, emu
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func shaOf(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func put(t *testing.T, st media.Store, key string, b []byte) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "obj")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutFile(context.Background(), key, p, media.PutOptions{}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
}

func objectKey(b []byte) string { return media.ObjectKey(ws, shaOf(b)) }

func logKey(text string) (string, []byte) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write([]byte(text))
	_ = w.Close()
	s := shaOf([]byte(text))
	return "ws/" + ws + "/logs/" + s[:2] + "/" + s + ".gz", buf.Bytes()
}

// seed fills a store with a workspace's media: objects of several sizes, a
// log text, a file nothing names by hash; and things the move must leave
// alone: another workspace's object and a probe outside any workspace.
func seed(t *testing.T, st media.Store) map[string][]byte {
	t.Helper()
	want := map[string][]byte{}
	for _, n := range []int{0, 1, 4096, 300 << 10, 1 << 20} {
		b := randomBytes(n)
		want[objectKey(b)] = b
	}
	k, gz := logKey("a long prompt " + strings.Repeat("words ", 2000))
	want[k] = gz
	want["ws/"+ws+"/misc/notes.txt"] = []byte("not content-addressed")
	for k, b := range want {
		put(t, st, k, b)
	}
	other := randomBytes(100)
	put(t, st, media.ObjectKey("wsp_other", shaOf(other)), other)
	put(t, st, "probes/prb_1", []byte("probe"))
	return want
}

// contents lists a store (every key) with each object's SHA-256.
func contents(t *testing.T, st media.Store, prefix string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := st.(media.Lister).List(context.Background(), prefix, func(oi media.ObjectInfo) error {
		rc, _, err := st.Get(context.Background(), oi.Key, 0, -1)
		if err != nil {
			return err
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			return err
		}
		out[oi.Key] = shaOf(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func opts(t *testing.T, from, to media.Store) Options {
	d := t.TempDir()
	return Options{Workspace: ws, From: from, To: to, StateDir: filepath.Join(d, "state"), ScratchDir: filepath.Join(d, "scratch")}
}

func TestClassify(t *testing.T) {
	sha := shaOf([]byte("x"))
	for _, c := range []struct{ key, kind, sha string }{
		{media.ObjectKey(ws, sha), KindObject, sha},
		{"ws/" + ws + "/logs/" + sha[:2] + "/" + sha + ".gz", KindLog, sha},
		{"ws/" + ws + "/objects/zz/" + sha, KindOther, ""},                   // folder does not match
		{"ws/" + ws + "/objects/" + sha[:2] + "/" + sha[:60], KindOther, ""}, // not a SHA-256
		{"ws/" + ws + "/misc/notes.txt", KindOther, ""},
		{"ws/other/objects/" + sha[:2] + "/" + sha, KindOther, ""},
	} {
		if k, s := Classify(ws, c.key); k != c.kind || s != c.sha {
			t.Errorf("%s: %s %s", c.key, k, s)
		}
	}
}

// Every pair of drivers, both ways round: folder -> S3 -> GCS -> folder. The
// workspace's files arrive byte for byte, the source is untouched, nothing
// outside the workspace is copied, and a second run finds nothing to do.
func TestMoveBetweenEveryStore(t *testing.T) {
	ctx := context.Background()
	fs, s3 := newFS(t), newS3(t)
	gcs, _ := newGCS(t)
	back := newFS(t)
	want := seed(t, fs)
	wantSums := map[string]string{}
	for k, b := range want {
		wantSums[k] = shaOf(b)
	}
	hops := []struct {
		name     string
		from, to media.Store
	}{{"folder to S3", fs, s3}, {"S3 to GCS", s3, gcs}, {"GCS to folder", gcs, back}}
	for _, h := range hops {
		before := contents(t, h.from, "")
		o := opts(t, h.from, h.to)
		var phases []string
		var mu sync.Mutex
		o.Progress = func(p Progress) {
			mu.Lock()
			if len(phases) == 0 || phases[len(phases)-1] != p.Phase {
				phases = append(phases, p.Phase)
			}
			mu.Unlock()
		}
		plan, err := Plan(ctx, o)
		if err != nil {
			t.Fatalf("%s: plan: %v", h.name, err)
		}
		if plan.Files != len(want) || plan.ToCopy != len(want) || plan.Present != 0 {
			t.Fatalf("%s: plan %+v", h.name, plan)
		}
		res, err := Run(ctx, o)
		if err != nil {
			t.Fatalf("%s: %v", h.name, err)
		}
		if !res.Verified || res.Copied != len(want) || res.CopiedNow != len(want) || len(res.Problems) != 0 || res.Sample == "" {
			t.Fatalf("%s: %+v", h.name, res)
		}
		if got := contents(t, h.to, "ws/"+ws+"/"); !equalMaps(got, wantSums) {
			t.Fatalf("%s: the new store holds %v, want %v", h.name, keys(got), keys(wantSums))
		}
		if got := contents(t, h.to, ""); len(got) != len(want) {
			t.Fatalf("%s: copied things outside the workspace: %v", h.name, keys(got))
		}
		if after := contents(t, h.from, ""); !equalMaps(before, after) {
			t.Fatalf("%s: the old store changed", h.name)
		}
		if strings.Join(phases, ",") != "listing,copying,listing,checking" {
			t.Fatalf("%s: phases %v", h.name, phases)
		}
		// Again: nothing to copy, everything counted as copied by this move.
		again, err := Run(ctx, o)
		if err != nil || !again.Verified || again.CopiedNow != 0 || again.Copied != len(want) || again.Present != 0 {
			t.Fatalf("%s: second run %+v %v", h.name, again, err)
		}
		// A different move into the same store finds everything there already.
		o2 := opts(t, h.from, h.to)
		third, err := Run(ctx, o2)
		if err != nil || !third.Verified || third.Present != len(want) || third.Copied != 0 {
			t.Fatalf("%s: fresh run %+v %v", h.name, third, err)
		}
	}
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// flaky wraps a store: Get can fail part way (a dropped connection), and
// every Get is counted with its offset.
type flaky struct {
	media.Store
	mu     sync.Mutex
	failAt int64 // a full read of a large object stops after this many bytes (once)
	gets   map[string][]int64
}

func (f *flaky) List(ctx context.Context, prefix string, fn func(media.ObjectInfo) error) error {
	return f.Store.(media.Lister).List(ctx, prefix, fn)
}

func (f *flaky) Get(ctx context.Context, key string, off, n int64) (io.ReadCloser, media.ObjectInfo, error) {
	rc, info, err := f.Store.Get(ctx, key, off, n)
	if err != nil {
		return rc, info, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gets == nil {
		f.gets = map[string][]int64{}
	}
	f.gets[key] = append(f.gets[key], off)
	if f.failAt > 0 && off == 0 && info.Size > f.failAt {
		limit := f.failAt
		f.failAt = 0
		return &cut{ReadCloser: rc, left: limit}, info, nil
	}
	return rc, info, nil
}

type cut struct {
	io.ReadCloser
	left int64
}

// stopAfter stops everything (cancel) once an upload passed a byte count,
// right after the store reported its progress (and its session) there.
type stopAfter struct {
	media.Store
	at     int64
	cancel func()
}

func (s *stopAfter) List(ctx context.Context, prefix string, fn func(media.ObjectInfo) error) error {
	return s.Store.(media.Lister).List(ctx, prefix, fn)
}

func (s *stopAfter) PutFile(ctx context.Context, key, path string, o media.PutOptions, r media.Resume, progress media.Progress) (media.ObjectInfo, error) {
	return s.Store.PutFile(ctx, key, path, o, r, func(done, total int64, rr media.Resume) {
		if progress != nil {
			progress(done, total, rr)
		}
		if done >= s.at && done < total {
			s.cancel()
		}
	})
}

func (c *cut) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errors.New("connection reset")
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.ReadCloser.Read(p)
	c.left -= int64(n)
	return n, err
}

// A stopped move continues where it stopped: a download from its last byte,
// a GCS upload in its resumable session, and nothing is copied twice.
func TestMoveResumes(t *testing.T) {
	ctx := context.Background()
	s3 := newS3(t)
	big := randomBytes(3 << 20)
	put(t, s3, objectKey(big), big)
	small := randomBytes(1000)
	put(t, s3, objectKey(small), small)

	// 1. The download drops at 1 MB: that file is reported, the rest is copied.
	src := &flaky{Store: s3, failAt: 1 << 20}
	gcs, emu := newGCS(t)
	o := opts(t, src, gcs)
	res, err := Run(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verified || len(res.Problems) != 1 || res.Problems[0].Key != objectKey(big) || res.Problems[0].Reason != Failed || res.Copied != 1 {
		t.Fatalf("after the drop: %+v", res)
	}
	part, _ := filepath.Glob(filepath.Join(o.ScratchDir, "*.part"))
	if len(part) != 1 {
		t.Fatalf("partial download kept: %v", part)
	}
	// 2. Stop the upload part way through its resumable session.
	o2 := o
	cctx, cancel := context.WithCancel(ctx)
	o2.To = &stopAfter{Store: gcs, at: 1 << 20, cancel: cancel}
	if _, err := Run(cctx, o2); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopping: %v", err)
	}
	if offs := src.gets[objectKey(big)]; len(offs) != 2 || offs[1] != 1<<20 {
		t.Fatalf("the download should continue from 1 MB: %v", offs)
	}
	ups := loadResumes(o.StateDir)
	if r, ok := ups[objectKey(big)]; !ok || r.SessionURI == "" {
		t.Fatalf("the upload session is kept for the next run: %+v", ups)
	}
	sessions := emu.Stats().Initiations
	// 3. Again: the session continues, the file is checked, the move verifies.
	res, err = Run(ctx, o)
	if err != nil || !res.Verified || res.Copied != 2 || res.CopiedNow != 1 {
		t.Fatalf("resumed: %+v %v", res, err)
	}
	if emu.Stats().Initiations != sessions {
		t.Fatalf("a new upload session was started (%d, was %d)", emu.Stats().Initiations, sessions)
	}
	if offs := src.gets[objectKey(big)]; len(offs) != 2 {
		t.Fatalf("downloaded again: %v", offs)
	}
	if got := contents(t, gcs, ""); got[objectKey(big)] != shaOf(big) {
		t.Fatal("the big file arrived damaged")
	}
	if _, err := os.Stat(o.ScratchDir); !os.IsNotExist(err) {
		t.Fatalf("the move's downloads are cleaned up once verified: %v", err)
	}
}

// Damaged files in the old store are never copied; files the new store holds
// with other contents are never overwritten; either keeps the move unverified.
func TestMoveRefusesDamagedAndDiffering(t *testing.T) {
	ctx := context.Background()
	from, to := newFS(t), newFS(t)
	good := randomBytes(5000)
	put(t, from, objectKey(good), good)
	// Bytes under another file's name: damaged.
	named := randomBytes(64)
	put(t, from, objectKey(named), randomBytes(64))
	// A log text whose contents do not match.
	k, _ := logKey("the real text")
	_, gz := logKey("another text")
	put(t, from, k, gz)
	// The new store already has this key with another size.
	clash := randomBytes(700)
	put(t, from, objectKey(clash), clash)
	put(t, to, objectKey(clash), randomBytes(300))

	res, err := Run(ctx, opts(t, from, to))
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, p := range res.Problems {
		reasons[p.Key] = p.Reason
	}
	if res.Verified || reasons[objectKey(named)] != Damaged || reasons[k] != Damaged || reasons[objectKey(clash)] != Differs || len(reasons) != 3 {
		t.Fatalf("%+v", res)
	}
	got := contents(t, to, "")
	if got[objectKey(good)] != shaOf(good) || got[objectKey(named)] != "" || got[k] != "" || len(got) != 2 {
		t.Fatalf("new store: %v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(to.Root, filepath.FromSlash(objectKey(clash)))); len(b) != 300 {
		t.Fatal("a differing file was overwritten")
	}
}

// Files this machine already has (a folder store's own files, the cache) are
// not downloaded; a damaged cached copy is not used.
func TestMoveUsesLocalCopies(t *testing.T) {
	ctx := context.Background()
	s3 := newS3(t)
	a, b, c := randomBytes(2000), randomBytes(3000), randomBytes(4000)
	for _, x := range [][]byte{a, b, c} {
		put(t, s3, objectKey(x), x)
	}
	cc, err := cache.New(filepath.Join(t.TempDir(), "cache"), 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range [][]byte{a, b} {
		p := cc.Path(shaOf(x))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, x, 0o600)
	}
	_ = os.WriteFile(cc.Path(shaOf(b)), randomBytes(3000), 0o600) // damaged, same size
	src := &flaky{Store: s3}
	o := opts(t, src, newFS(t))
	o.Cache = cc
	plan, err := Plan(ctx, o)
	if err != nil || plan.Local != 2 || plan.LocalBytes != 5000 {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	res, err := Run(ctx, o)
	if err != nil || !res.Verified || res.Copied != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	if len(src.gets[objectKey(a)]) != 0 || len(src.gets[objectKey(b)]) != 1 || len(src.gets[objectKey(c)]) != 1 || res.Downloaded != 7000 {
		t.Fatalf("downloads: %v (%d bytes)", src.gets, res.Downloaded)
	}
}

// --read-back reads every file back from the new store, including ones that
// were there before; a wrong one is reported, never overwritten.
func TestMoveReadBack(t *testing.T) {
	ctx := context.Background()
	from, to := newFS(t), newFS(t)
	want := seed(t, from)
	pre := randomBytes(900)
	put(t, from, objectKey(pre), pre)
	put(t, to, objectKey(pre), pre)
	bad := randomBytes(800)
	put(t, from, objectKey(bad), bad)
	put(t, to, objectKey(bad), randomBytes(800)) // same size, other bytes
	o := opts(t, from, to)
	o.ReadBack = true
	res, err := Run(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verified || len(res.Problems) != 1 || res.Problems[0].Key != objectKey(bad) || res.Problems[0].Reason != Differs {
		t.Fatalf("%+v", res)
	}
	if res.ReadBack != 1 || res.Copied != len(want) || res.Present != 2 {
		t.Fatalf("counts: %+v", res)
	}
	// Without the bad file the move verifies, and nothing is read back twice.
	_ = os.Remove(filepath.Join(to.Root, filepath.FromSlash(objectKey(bad))))
	res, err = Run(ctx, o)
	if err != nil || !res.Verified || res.ReadBack != 0 || res.CopiedNow != 1 {
		t.Fatalf("after removing it: %+v %v", res, err)
	}
}
