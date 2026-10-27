package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/playback"
	"github.com/metavert-io/metarush/internal/sessions"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/dblink"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Playing through a database outage (D180): the media endpoints answer from
// what they remember when the database doesn't, never for anyone the
// database (or the sign-in) wouldn't let play it, and say plainly and quickly
// when they can't. The outage is played inside the test process: the app
// reaches the test MongoDB through a link the test cuts (live connections
// dropped mid-reply, new ones left unanswered); mongod keeps running.

type mediaDash struct {
	*dash
	link *dblink.Link
}

func newMediaDash(t *testing.T) *mediaDash {
	t.Helper()
	link := dblink.Start(t)
	a := testapp.NewWith(t, testapp.Options{DatabaseURI: link.URI()})
	hs := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(hs.Close)
	u, _ := url.Parse(hs.URL)
	a.Cfg.Dashboard.Port, _ = strconv.Atoi(u.Port()) // the Host guard allows this port only
	srv := New(a)
	t.Cleanup(srv.Assistant.Close)
	hs.Config.Handler = srv.Handler()
	jar, _ := cookiejar.New(nil)
	d := &mediaDash{dash: &dash{t: t, base: hs.URL, client: &http.Client{Jar: jar}, a: a, srv: srv}, link: link}
	d.signIn()
	t.Cleanup(link.Restore) // before the app closes (it saves its activity log)
	return d
}

type mediaAnswer struct {
	code    int
	body    []byte
	from    string // "memory" when the record came from the memory
	took    time.Duration
	errCode string
	message string
	reason  string
}

func (d *mediaDash) get(path, rangeHdr string) mediaAnswer {
	d.t.Helper()
	req, _ := http.NewRequest("GET", d.base+path, nil)
	if rangeHdr != "" {
		req.Header.Set("Range", rangeHdr)
	}
	start := time.Now()
	res, err := d.client.Do(req)
	if err != nil {
		d.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	ans := mediaAnswer{code: res.StatusCode, body: b, from: res.Header.Get(recordHeader), took: time.Since(start)}
	if strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		var env struct {
			Error struct {
				Code    string         `json:"code"`
				Message string         `json:"message"`
				Details map[string]any `json:"details"`
			} `json:"error"`
		}
		_ = json.Unmarshal(b, &env)
		ans.errCode, ans.message = env.Error.Code, env.Error.Message
		ans.reason, _ = env.Error.Details["reason"].(string)
	}
	return ans
}

// stored puts bytes in the media store and returns their key and SHA-256.
func stored(t *testing.T, a *app.App, data []byte, mime string) (string, string) {
	t.Helper()
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	p := filepath.Join(t.TempDir(), sha[:12])
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	key := media.ObjectKey(a.WorkspaceID(), sha)
	if _, err := a.Media.PutFile(context.Background(), key, p, media.PutOptions{SHA256: sha, ContentType: mime}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	return key, sha
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

type clipFiles struct {
	asset          models.Asset
	proxy, picture []byte
}

// addClip stores a clip (its file, preview copy and picture) and its record.
func addClip(t *testing.T, a *app.App, title string, proxySize int) clipFiles {
	t.Helper()
	orig, proxy, picture := randomBytes(t, 64<<10), randomBytes(t, proxySize), randomBytes(t, 4<<10)
	origKey, origSHA := stored(t, a, orig, "video/mp4")
	pk, ps := stored(t, a, proxy, "video/mp4")
	tk, ts := stored(t, a, picture, "image/jpeg")
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{}, Kind: "video", Name: title + ".mp4", Title: title,
		SHA256: origSHA, Size: int64(len(orig)), MIME: "video/mp4", StorageKey: origKey, State: models.AssetAvailable,
		Media: models.MediaInfo{Duration: 10, HasVideo: true, HasAudio: true, Width: 1280, Height: 720, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{
			models.VariantProxy:     {Kind: models.VariantProxy, StorageKey: pk, SHA256: ps, Size: int64(len(proxy)), MIME: "video/mp4", CreatedAt: now},
			models.VariantThumbnail: {Kind: models.VariantThumbnail, StorageKey: tk, SHA256: ts, Size: int64(len(picture)), MIME: "image/jpeg", CreatedAt: now},
		},
		CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(context.Background(), store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	return clipFiles{as, proxy, picture}
}

// addRender stores a finished render (its video and picture) and its record.
func addRender(t *testing.T, a *app.App) (models.Render, []byte) {
	t.Helper()
	video, picture := randomBytes(t, 96<<10), randomBytes(t, 4<<10)
	vk, vs := stored(t, a, video, "video/mp4")
	tk, _ := stored(t, a, picture, "image/jpeg")
	now := time.Now().UTC()
	rd := models.Render{ID: ids.New(ids.Render), WorkspaceID: a.WorkspaceID(), ProjectID: ids.New(ids.Project), RevisionID: ids.New(ids.Revision), RevisionNumber: 3,
		Preset: "vertical-1080p", Quality: "review", State: models.RenderAvailable, CreatedAt: now, CompletedAt: &now,
		Output: &models.RenderOutput{StorageKey: vk, SHA256: vs, Size: int64(len(video)), Duration: 4, Width: 1080, Height: 1920, FPS: 30, ThumbnailKey: tk}}
	if err := a.DB.Insert(context.Background(), store.CRenders, rd); err != nil {
		t.Fatal(err)
	}
	return rd, video
}

// databaseBack waits until the database answers a media request again.
func (d *mediaDash) databaseBack(path string) {
	d.t.Helper()
	start := time.Now()
	deadline := start.Add(30 * time.Second)
	for {
		ans := d.get(path, "bytes=0-15")
		if (ans.code == http.StatusOK || ans.code == http.StatusPartialContent) && ans.from == "" {
			d.t.Logf("the database answered again %s after the link was restored", time.Since(start).Round(time.Millisecond))
			return
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("the database never answered again after the outage: %d %s %s", ans.code, ans.from, ans.body)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestMediaPlaysThroughADatabaseOutage(t *testing.T) {
	d := newMediaDash(t)
	clip := addClip(t, d.a, "Harbour walk", 2<<20)
	rd, video := addRender(t, d.a)
	proxy := "/media/assets/" + clip.asset.ID + "/proxy"

	// The database answers: the records come from it (and are remembered).
	if ans := d.get(proxy, "bytes=0-1023"); ans.code != http.StatusPartialContent || !bytes.Equal(ans.body, clip.proxy[:1024]) || ans.from != "" {
		t.Fatalf("preview copy before the outage: %d from %q (%d bytes)", ans.code, ans.from, len(ans.body))
	}
	for _, p := range []string{"/media/assets/" + clip.asset.ID + "/thumbnail", "/media/renders/" + rd.ID, "/media/renders/" + rd.ID + "/thumbnail"} {
		if ans := d.get(p, ""); ans.code != http.StatusOK || ans.from != "" {
			t.Fatalf("%s before the outage: %d from %q %s", p, ans.code, ans.from, ans.body)
		}
	}
	// A long answer that started before the outage keeps going through it.
	res, err := d.client.Get(d.base + proxy)
	if err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 64<<10)
	if _, err := io.ReadFull(res.Body, head); err != nil {
		t.Fatal(err)
	}

	d.link.Cut()

	rest, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || !bytes.Equal(append(head, rest...), clip.proxy) {
		t.Fatalf("the preview copy that was playing stopped in the outage: %v (%d of %d bytes)", err, len(head)+len(rest), len(clip.proxy))
	}
	// The player asks for the next part (a seek): answered from memory once the
	// database didn't answer quickly, then at once while it stays down.
	first := d.get(proxy, "bytes=1048576-1049599")
	if first.code != http.StatusPartialContent || !bytes.Equal(first.body, clip.proxy[1<<20:1<<20+1024]) || first.from != "memory" {
		t.Fatalf("a seek in the outage: %d from %q %s", first.code, first.from, first.body)
	}
	if first.took > 3*time.Second {
		t.Errorf("the first answer in the outage took %s; it waits at most %s for the database", first.took, playback.QuickWait)
	}
	again := d.get(proxy, "bytes=2048-4095")
	if again.code != http.StatusPartialContent || !bytes.Equal(again.body, clip.proxy[2048:4096]) || again.from != "memory" {
		t.Fatalf("the next seek in the outage: %d from %q", again.code, again.from)
	}
	if again.took > time.Second {
		t.Errorf("while the database is known to be down, a remembered clip waited %s", again.took)
	}
	for _, c := range []struct {
		path string
		want []byte
	}{
		{"/media/assets/" + clip.asset.ID + "/thumbnail", clip.picture},
		{"/media/assets/" + clip.asset.ID + "/original?preview=1", nil},
		{"/media/renders/" + rd.ID, video},
		{"/media/renders/" + rd.ID + "/thumbnail", nil},
	} {
		ans := d.get(c.path, "")
		if ans.code != http.StatusOK || ans.from != "memory" || (c.want != nil && !bytes.Equal(ans.body, c.want)) {
			t.Errorf("%s in the outage: %d from %q %s", c.path, ans.code, ans.from, ans.body[:min(len(ans.body), 200)])
		}
	}

	d.link.Restore()
	d.databaseBack(proxy)
}

func TestMediaWithNothingRememberedSaysSoQuickly(t *testing.T) {
	d := newMediaDash(t)
	one, two := addClip(t, d.a, "Kitchen chat", 32<<10), addClip(t, d.a, "Blurry take", 32<<10)
	rd, _ := addRender(t, d.a)

	d.link.Cut()

	for i, p := range []string{"/media/assets/" + one.asset.ID + "/proxy", "/media/assets/" + two.asset.ID + "/thumbnail", "/media/renders/" + rd.ID} {
		ans := d.get(p, "")
		if ans.code != http.StatusServiceUnavailable || ans.errCode != "E_DATABASE" || ans.reason != "database_unreachable" || ans.from != "" {
			t.Fatalf("%s with the database down and nothing remembered: %d %s %q %s", p, ans.code, ans.errCode, ans.reason, ans.body)
		}
		if !strings.Contains(ans.message, "can't reach its database right now") {
			t.Errorf("the message doesn't say what is wrong: %q", ans.message)
		}
		if f := plainwords.Check(ans.message); len(f) > 0 {
			t.Errorf("the message isn't plain: %q %+v", ans.message, f)
		}
		// Quickly: the driver alone would wait 8 s for a server and then say
		// the clip doesn't exist; a lookup gives up after Wait (then DownWait
		// while the database stays down).
		limit := playback.Wait + 2*time.Second
		if i > 0 {
			limit = playback.DownWait + 2*time.Second
		}
		if ans.took > limit {
			t.Errorf("%s took %s to say the database is down (limit %s)", p, ans.took, limit)
		}
	}

	d.link.Restore()
	d.databaseBack("/media/assets/" + one.asset.ID + "/proxy")
	if ans := d.get("/media/renders/"+rd.ID, ""); ans.code != http.StatusOK || ans.from != "" {
		t.Fatalf("the render once the database is back: %d %q", ans.code, ans.from)
	}
}

// A version that pins a fixed copy the clip no longer plays (D152): the copy
// isn't remembered, so in an outage the answer says the database is down
// (never that the copy doesn't exist), quickly.
func TestMediaPinnedCopyInAnOutageSaysTheDatabaseIsDown(t *testing.T) {
	d := newMediaDash(t)
	clip := addClip(t, d.a, "Garden tour", 32<<10)
	proxy := "/media/assets/" + clip.asset.ID + "/proxy"
	pinned := proxy + "?media=" + strings.Repeat("ab", 32)
	if ans := d.get(proxy, ""); ans.code != http.StatusOK || ans.from != "" {
		t.Fatalf("before the outage: %d %q", ans.code, ans.from)
	}
	if ans := d.get(pinned, ""); ans.code != http.StatusNotFound {
		t.Fatalf("a copy the database doesn't have: %d %s", ans.code, ans.body)
	}

	d.link.Cut()

	if ans := d.get(proxy, ""); ans.code != http.StatusOK || ans.from != "memory" {
		t.Fatalf("the clip's own media in the outage: %d %q", ans.code, ans.from)
	}
	ans := d.get(pinned, "")
	if ans.code != http.StatusServiceUnavailable || ans.errCode != "E_DATABASE" || ans.reason != "database_unreachable" {
		t.Fatalf("a pinned copy in the outage: %d %s %q %s", ans.code, ans.errCode, ans.reason, ans.body)
	}
	if limit := playback.Wait + 2*time.Second; ans.took > limit {
		t.Errorf("a pinned copy took %s to say the database is down (limit %s)", ans.took, limit)
	}

	d.link.Restore()
	d.databaseBack(proxy)
	if ans := d.get(pinned, ""); ans.code != http.StatusNotFound {
		t.Fatalf("once the database is back: %d %s", ans.code, ans.body)
	}
}

func TestMediaMemoryFollowsTheDatabase(t *testing.T) {
	d := newMediaDash(t)
	ctx := context.Background()
	trashed, deleted, replaced := addClip(t, d.a, "Waves at dawn", 16<<10), addClip(t, d.a, "Old take", 16<<10), addClip(t, d.a, "Interview", 16<<10)
	path := func(c clipFiles) string { return "/media/assets/" + c.asset.ID + "/proxy" }
	for _, c := range []clipFiles{trashed, deleted, replaced} {
		if ans := d.get(path(c), ""); ans.code != http.StatusOK {
			t.Fatalf("%s: %d", c.asset.Title, ans.code)
		}
	}
	held := playback.Remembered(d.a)

	// Moved to the Trash: the change (from the change stream or the poller,
	// both through the broker) drops it, and it isn't remembered again.
	now := time.Now().UTC()
	if _, err := d.a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": trashed.asset.ID}, bson.M{"$set": bson.M{"trash": models.AssetTrash{At: now}, "projectIds": []string{}, "updatedAt": now}}); err != nil {
		t.Fatal(err)
	}
	d.srv.Broker().Local(Event{Type: store.CAssets, Op: "update", ID: trashed.asset.ID})
	// Deleted for good: the database says so on the next request.
	if _, err := d.a.DB.C(store.CAssets).DeleteOne(ctx, bson.M{"_id": deleted.asset.ID}); err != nil {
		t.Fatal(err)
	}
	if ans := d.get(path(deleted), ""); ans.code != http.StatusNotFound || ans.message != "MetaRush can't find that clip." {
		t.Fatalf("a deleted clip: %d %s", ans.code, ans.body)
	}
	// Its preview copy made again: the change brings the new one into memory.
	newer := randomBytes(t, 24<<10)
	nk, ns := stored(t, d.a, newer, "video/mp4")
	now = time.Now().UTC()
	if _, err := d.a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": replaced.asset.ID}, bson.M{"$set": bson.M{
		"variants.proxy.storageKey": nk, "variants.proxy.sha256": ns, "variants.proxy.size": len(newer), "updatedAt": now}}); err != nil {
		t.Fatal(err)
	}
	d.srv.Broker().Local(Event{Type: store.CAssets, Op: "update", ID: replaced.asset.ID})
	deadline := time.Now().Add(10 * time.Second)
	for playback.Remembered(d.a) != held-2 {
		if time.Now().After(deadline) {
			t.Fatalf("remembered %d records, want %d (the trashed and the deleted clip forgotten, the changed one read again)", playback.Remembered(d.a), held-2)
		}
		time.Sleep(50 * time.Millisecond)
	}

	d.link.Cut()

	for _, c := range []clipFiles{trashed, deleted} {
		if ans := d.get(path(c), ""); ans.code != http.StatusServiceUnavailable || ans.errCode != "E_DATABASE" {
			t.Errorf("%s played from memory after it changed: %d from %q", c.asset.Title, ans.code, ans.from)
		}
	}
	if ans := d.get(path(replaced), ""); ans.code != http.StatusOK || ans.from != "memory" || !bytes.Equal(ans.body, newer) {
		t.Errorf("the clip whose preview copy was made again: %d from %q, new copy %v", ans.code, ans.from, bytes.Equal(ans.body, newer))
	}

	d.link.Restore()
	d.databaseBack(path(replaced))
	// A clip in the Trash still plays as it always did while the database answers.
	if ans := d.get(path(trashed), ""); ans.code != http.StatusOK || ans.from != "" {
		t.Errorf("a clip in the Trash with the database up: %d from %q", ans.code, ans.from)
	}
}

func TestMediaMemoryIsBehindTheSignIn(t *testing.T) {
	d := newMediaDash(t)
	clip := addClip(t, d.a, "Harbour pan", 16<<10)
	path := "/media/assets/" + clip.asset.ID + "/proxy"
	if ans := d.get(path, ""); ans.code != http.StatusOK {
		t.Fatalf("before the outage: %d", ans.code)
	}
	// Another workspace on this computer, signed in to its own dashboard.
	other := newMediaDash(t)

	d.link.Cut()
	other.link.Cut()

	if ans := d.get(path, ""); ans.code != http.StatusOK || ans.from != "memory" {
		t.Fatalf("signed in, in the outage: %d from %q", ans.code, ans.from)
	}
	// A browser that isn't signed in gets nothing from the memory.
	res, err := http.Get(d.base + path)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized || res.Header.Get(recordHeader) != "" {
		t.Errorf("not signed in, in the outage: %d from %q", res.StatusCode, res.Header.Get(recordHeader))
	}
	// Neither does another workspace's dashboard.
	if ans := other.get(path, ""); ans.code != http.StatusServiceUnavailable || ans.from != "" {
		t.Errorf("another workspace in the outage: %d from %q %s", ans.code, ans.from, ans.body)
	}
	// Nor a browser whose sign-in ended (signed out, or taken off the list).
	if _, err := sessions.For(d.a.Cfg.StateDir()).RemoveWhere(func(sessions.Session) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if ans := d.get(path, ""); ans.code != http.StatusUnauthorized || ans.from != "" {
		t.Errorf("signed out, in the outage: %d from %q", ans.code, ans.from)
	}

	d.link.Restore()
	other.link.Restore()
	d.signIn()
	d.databaseBack(path)
	if ans := other.get(path, ""); ans.code != http.StatusNotFound {
		t.Errorf("another workspace with the database up: %d", ans.code)
	}
}

// preview.prepare remembers every clip of the cut it gets ready, so the cut
// plays through an outage, also the clips the player hasn't reached yet.
func TestPreviewPrepareRemembersTheCut(t *testing.T) {
	d := newMediaDash(t)
	ctx := context.Background()
	p, err := ops.CallTyped[ops.ProjectOut](ctx, d.a, "project.create", ops.ProjectCreateIn{Name: "Spring promo"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	first, second := addClip(t, d.a, "Opening", 16<<10), addClip(t, d.a, "Closing", 16<<10)
	for _, c := range []clipFiles{first, second} {
		if _, err := d.a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": c.asset.ID}, bson.M{"$set": bson.M{"projectIds": []string{p.Project.ID}}}); err != nil {
			t.Fatal(err)
		}
	}
	plan := fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"vertical-1080p","fps":30},"clips":[{"id":"a","assetId":%q,"in":0,"out":4},{"id":"b","assetId":%q,"in":2,"out":6}]}}`,
		p.Project.ID, first.asset.ID, second.asset.ID)
	check, err := ops.CallTyped[ops.EditCheckOut](ctx, d.a, "edit.check", ops.EditCheckIn{Plan: json.RawMessage(plan)}, ops.Caller{Human: true})
	if err != nil || check.ConfirmToken == "" {
		t.Fatalf("edit.check: %v %+v", err, check)
	}
	rev, err := ops.CallTyped[ops.EditApplyOut](ctx, d.a, "edit.apply", ops.EditApplyIn{Plan: json.RawMessage(plan), ConfirmToken: check.ConfirmToken}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	var out ops.PreviewPrepareOut
	d.op("preview.prepare", ops.PreviewPrepareIn{RevisionID: rev.Revision.ID}, &out)
	if out.Total != 2 {
		t.Fatalf("preview.prepare: %+v", out)
	}

	d.link.Cut()

	for _, c := range []clipFiles{first, second} {
		ans := d.get("/media/assets/"+c.asset.ID+"/proxy", "bytes=0-99")
		if ans.code != http.StatusPartialContent || ans.from != "memory" || !bytes.Equal(ans.body, c.proxy[:100]) {
			t.Errorf("%s, never played before the outage: %d from %q", c.asset.Title, ans.code, ans.from)
		}
	}

	d.link.Restore()
	d.databaseBack("/media/assets/" + first.asset.ID + "/proxy")
}
