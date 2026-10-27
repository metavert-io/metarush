package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/probe"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

// tone writes a short WAV (16-bit mono, 16 kHz): footage every computer's
// ffprobe reads, made without ffmpeg.
func tone(t *testing.T, dir, name string, seconds, hz float64) string {
	t.Helper()
	n := int(16000 * seconds)
	data := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		v := int16(6000 * math.Sin(2*math.Pi*hz*float64(i)/16000))
		binary.LittleEndian.PutUint16(data[2*i:], uint16(v))
	}
	var b bytes.Buffer
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + len(data)))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1))
	w(uint16(1))
	w(uint32(16000))
	w(uint32(32000))
	w(uint16(2))
	w(uint16(16))
	b.WriteString("data")
	w(uint32(len(data)))
	b.Write(data)
	return writeAt(t, filepath.Join(dir, name), b.Bytes(), time.Minute)
}

// logo writes a small PNG.
func logo(t *testing.T, dir, name string, c color.RGBA) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 96, 40))
	for y := 6; y < 34; y++ {
		for x := 8; x < 88; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return writeAt(t, filepath.Join(dir, name), b.Bytes(), time.Minute)
}

// writeAt writes a file dated ago in the past (a file that stopped changing).
func writeAt(t *testing.T, p string, b []byte, ago time.Duration) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-ago)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	return p
}

func needProbe(t *testing.T, a *app.App) {
	t.Helper()
	if !a.Tools.Has(tools.FFprobe) {
		t.Skip("needs ffprobe")
	}
}

func newProject(t *testing.T, a *app.App, name string) models.Project {
	t.Helper()
	now := time.Now().UTC()
	p := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: name, Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(context.Background(), store.CProjects, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func getAsset(t *testing.T, a *app.App, id string) models.Asset {
	t.Helper()
	var as models.Asset
	if err := a.DB.GetByID(context.Background(), store.CAssets, a.WorkspaceID(), id, &as); err != nil {
		t.Fatal(err)
	}
	return as
}

func copyFile(t *testing.T, from, to string) string {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	return writeAt(t, to, b, time.Minute)
}

// A sound file is added once: probed, hashed, stored, made available with
// its preparation queued. The same file again, the same bytes under another
// name (skipped, or kept as a second name) are told apart.
func TestFileAddsFootageAndKnowsItAgain(t *testing.T) {
	a := testapp.New(t)
	needProbe(t, a)
	ctx := context.Background()
	p := newProject(t, a, "Garden film")
	dir := t.TempDir()
	wav := tone(t, dir, "birdsong.wav", 1.5, 440)
	var phases []string
	res := File(ctx, a, wav, Options{ProjectID: p.ID, Via: "cli", Transcribe: true, Progress: func(_, phase string, _, _ int64) {
		if !slices.Contains(phases, phase) {
			phases = append(phases, phase)
		}
	}})
	if res.Status != "ingested" || res.AssetID == "" || len(res.SHA256) != 64 || res.Kind != "audio" || math.Abs(res.Duration-1.5) > 0.05 || res.SessionID == "" ||
		res.Title != "birdsong" || len(res.JobIDs) == 0 || res.Error != "" {
		t.Fatalf("ingest: %+v", res)
	}
	for _, want := range []string{"probe", "hash", "upload", "done"} {
		if !slices.Contains(phases, want) {
			t.Errorf("no %q progress in %v", want, phases)
		}
	}
	as := getAsset(t, a, res.AssetID)
	if as.State != models.AssetAvailable || as.Fingerprint == "" || as.Sample == "" || !slices.Equal(as.ProjectIDs, []string{p.ID}) || as.Via != "cli" || as.TitleBy != models.TitleByFile {
		t.Fatalf("asset %+v", as)
	}
	if _, err := a.Media.Stat(ctx, as.StorageKey); err != nil {
		t.Fatalf("the original is stored: %v", err)
	}
	var sess models.IngestSession
	if err := a.DB.GetByID(ctx, store.CIngest, a.WorkspaceID(), res.SessionID, &sess); err != nil || sess.State != "available" || sess.BytesDone != res.Size {
		t.Fatalf("session %+v %v", sess, err)
	}
	var proj models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), p.ID, &proj); err != nil || proj.AssetCount != 1 {
		t.Fatalf("project count %+v %v", proj.AssetCount, err)
	}

	// The same file again: nothing new, now also in another project.
	other := newProject(t, a, "Trailer")
	same := File(ctx, a, wav, Options{ProjectID: other.ID})
	if same.Status != "deduped" || same.AssetID != res.AssetID || same.Duplicate == nil || same.Duplicate.Action != models.DuplicateSameFile {
		t.Fatalf("same file: %+v", same)
	}
	if as := getAsset(t, a, res.AssetID); !slices.Contains(as.ProjectIDs, other.ID) {
		t.Fatalf("the clip joins the other project: %v", as.ProjectIDs)
	}
	// The same bytes under another name: skipped by default, saying which clip it matches.
	dup := File(ctx, a, copyFile(t, wav, filepath.Join(dir, "copy.wav")), Options{})
	if dup.Status != "duplicate" || dup.Duplicate == nil || dup.Duplicate.Action != models.DuplicateSkipped || dup.Duplicate.Title != "birdsong" {
		t.Fatalf("duplicate: %+v", dup)
	}
	if as := getAsset(t, a, res.AssetID); as.HasName("copy.wav") {
		t.Fatal("a skipped copy's name is not kept")
	}
	// Kept: the name is recorded on the same clip; the bytes are stored once.
	kept := File(ctx, a, filepath.Join(dir, "copy.wav"), Options{Duplicates: DuplicatesKeep})
	if as := getAsset(t, a, res.AssetID); kept.Status != "deduped" || kept.Duplicate == nil || kept.Duplicate.Action != models.DuplicateKeptBoth || !as.HasName("copy.wav") {
		t.Fatalf("kept both: %+v", kept)
	}
	n, err := a.DB.C(store.CAssets).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID()})
	if err != nil || n != 1 {
		t.Fatalf("one clip: %d %v", n, err)
	}
}

// An ingest that stopped before it finished picks up where it stopped; a
// clip in the Trash comes back when its file is added again, and what older
// versions didn't record (fingerprints, where it came from) is filled in.
func TestFileResumesRestoresAndFillsIn(t *testing.T) {
	a := testapp.New(t)
	needProbe(t, a)
	ctx := context.Background()
	p := newProject(t, a, "Garden film")
	dir := t.TempDir()
	wav := tone(t, dir, "interview.wav", 1, 330)
	sha, err := media.HashFile(wav)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(wav)
	now := time.Now().UTC()
	staged := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "audio", Name: "interview.wav", SHA256: sha, Size: st.Size(),
		StorageKey: media.ObjectKey(a.WorkspaceID(), sha), State: models.AssetStaging, ProjectIDs: []string{}, Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, staged); err != nil {
		t.Fatal(err)
	}
	sess := models.IngestSession{ID: ids.New(ids.Ingest), WorkspaceID: a.WorkspaceID(), SourceName: "interview.wav", Size: st.Size(), SHA256: sha, AssetID: staged.ID,
		State: "uploading", Upload: models.UploadState{Key: staged.StorageKey}, Via: "cli", CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CIngest, sess); err != nil {
		t.Fatal(err)
	}
	src := &models.AssetSource{Platform: models.PlatformWeb, URL: "https://example.com/interview"}
	res := File(ctx, a, wav, Options{ProjectID: p.ID, Source: src})
	if res.Status != "resumed" || res.AssetID != staged.ID || res.SessionID != sess.ID {
		t.Fatalf("resumed: %+v", res)
	}
	as := getAsset(t, a, staged.ID)
	if as.State != models.AssetAvailable || as.Source == nil || as.Source.URL != src.URL || !slices.Contains(as.ProjectIDs, p.ID) {
		t.Fatalf("after resuming: %+v", as)
	}

	// In the Trash, without fingerprints or a source: the same file again
	// brings it back and fills them in.
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": staged.ID}, bson.M{
		"$set":   bson.M{"trash": models.AssetTrash{At: now, ProjectIDs: []string{p.ID}}, "projectIds": []string{}},
		"$unset": bson.M{"fingerprint": "", "sample": "", "source": ""}}); err != nil {
		t.Fatal(err)
	}
	back := File(ctx, a, wav, Options{Source: src})
	if back.Status != "deduped" || back.Duplicate == nil || back.Duplicate.Action != models.DuplicateSameFile {
		t.Fatalf("again: %+v", back)
	}
	as = getAsset(t, a, staged.ID)
	if as.Trash != nil || !slices.Contains(as.ProjectIDs, p.ID) || as.Fingerprint == "" || as.Sample == "" || as.Source == nil {
		t.Fatalf("restored and filled in: trash %+v projects %v fingerprint %q sample %q source %+v", as.Trash, as.ProjectIDs, as.Fingerprint, as.Sample, as.Source)
	}
}

// A new clip takes the title an import brings (MetaRush's), tidied.
func TestFileTakesAnImportsTitle(t *testing.T) {
	a := testapp.New(t)
	needProbe(t, a)
	res := File(context.Background(), a, tone(t, t.TempDir(), "dl-4471.wav", 0.5, 500), Options{Title: "  Morning   walk\n", DisplayName: "Morning walk.wav"})
	if res.Status != "ingested" || res.Name != "Morning walk.wav" {
		t.Fatalf("ingest: %+v", res)
	}
	as := getAsset(t, a, res.AssetID)
	if as.Title != "Morning walk" || as.TitleBy != models.TitleByMetaRush || as.Name != "Morning walk.wav" {
		t.Fatalf("asset title %q by %q name %q", as.Title, as.TitleBy, as.Name)
	}
}

// What can't be added says why in plain words, and never the temporary path
// a browser upload arrived under.
func TestFileRefusesWhatItCantAdd(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	empty := writeAt(t, filepath.Join(dir, "empty.mov"), nil, time.Minute)
	if r := File(ctx, a, empty, Options{}); r.Status != "skipped" || r.Error != "It's empty, or not a file MetaRush can add." {
		t.Fatalf("empty: %+v", r)
	}
	if r := File(ctx, a, dir, Options{}); r.Status != "skipped" {
		t.Fatalf("a folder: %+v", r)
	}
	if r := File(ctx, a, filepath.Join(dir, "gone.mov"), Options{}); r.Status != "failed" || r.Error != "MetaRush can't find this file." {
		t.Fatalf("missing: %+v", r)
	}
	notes := writeAt(t, filepath.Join(dir, "notes.mov"), []byte("this is not a video"), time.Minute)
	if r := File(ctx, a, notes, Options{ProjectID: ids.New(ids.Project)}); r.Status != "failed" || r.Error != "The project it was meant for was not found. It may have been deleted." {
		t.Fatalf("unknown project: %+v", r)
	}
	if a.Tools.Has(tools.FFprobe) {
		if r := File(ctx, a, notes, Options{}); r.Status != "skipped" || r.Error != "It isn't a video or sound file MetaRush can read." {
			t.Fatalf("not media: %+v", r)
		}
	}
	// A picture that can't be read: its name, not the temporary path.
	tmp := writeAt(t, filepath.Join(dir, "upload-7f3a.part"), append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{3}, 80)...), time.Minute)
	r := File(ctx, a, tmp, Options{DisplayName: "brand.png"})
	if r.Status != "failed" || r.Name != "brand.png" || strings.Contains(r.Error, tmp) || !strings.Contains(r.Error, "brand.png") || r.Kind != "resource" {
		t.Fatalf("broken picture: %+v", r)
	}
}

// Pictures go to the resources library (D80), the same picture again is
// nothing new.
func TestFilePicturesGoToResources(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := newProject(t, a, "Brand")
	pic := logo(t, t.TempDir(), "acme-logo.png", color.RGBA{220, 30, 120, 255})
	done := false
	r := File(ctx, a, pic, Options{ProjectID: p.ID, Tags: []string{"brand"}, Progress: func(_, phase string, _, _ int64) { done = done || phase == "done" }})
	if r.Status != "ingested" || r.Kind != "resource" || r.Resource == nil || r.Resource.Kind != models.ResourceImage || r.Resource.Scope != models.ScopeProject ||
		r.Resource.Status != resources.StatusAdded || r.Title == "" || len(r.SHA256) != 64 || !done {
		t.Fatalf("picture: %+v (resource %+v)", r, r.Resource)
	}
	again := File(ctx, a, pic, Options{ProjectID: p.ID})
	if again.Status != "deduped" || again.Resource == nil || again.Resource.ID != r.Resource.ID {
		t.Fatalf("again: %+v", again)
	}
	var res models.Resource
	if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), r.Resource.ID, &res); err != nil || !slices.Contains(res.Tags, "brand") || res.Latest().Source.Machine == "" {
		t.Fatalf("resource %+v %v", res, err)
	}
}

// A caller that brings in footage only (generated shots, Remotion renders,
// call recordings) never makes a picture, an SVG, a PDF or a font a resource,
// nor a clip of a still picture (the Library hides those since D79): it is
// skipped with a plain reason. Footage still comes in as footage.
func TestFootageOnlySkipsPictures(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	pic := logo(t, dir, "still.png", color.RGBA{20, 90, 220, 255})
	r := File(ctx, a, pic, Options{FootageOnly: true})
	if r.Status != "skipped" || r.Error != "It's a picture, not a video or sound file." || r.AssetID != "" || r.Resource != nil {
		t.Fatalf("a picture: %+v", r)
	}
	svg := writeAt(t, filepath.Join(dir, "mark.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10"/></svg>`), time.Minute)
	if r := File(ctx, a, svg, Options{FootageOnly: true}); r.Status != "skipped" || r.Error != "It's an SVG, not a video or sound file." {
		t.Fatalf("an SVG: %+v", r)
	}
	for _, c := range []string{store.CAssets, store.CResources} {
		if n, err := a.DB.C(c).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID()}); err != nil || n != 0 {
			t.Fatalf("%s: %d %v", c, n, err)
		}
	}
	if a.Tools.Has(tools.FFprobe) {
		snd := tone(t, dir, "voice.wav", 0.5, 220)
		if r := File(ctx, a, snd, Options{FootageOnly: true}); r.Status != "ingested" || r.Kind != "audio" || r.AssetID == "" {
			t.Fatalf("a sound: %+v", r)
		}
	}
}

func TestCollectAndPaths(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.wav", ".hidden.wav", "sub/b.wav", ".cache/c.wav", "Thumbs.db", "desktop.ini", ".DS_Store"} {
		writeAt(t, filepath.Join(dir, name), []byte("x"), time.Minute)
	}
	files, skipped := Collect([]string{dir, filepath.Join(dir, "missing.wav")}, true)
	if !slices.Equal(files, []string{filepath.Join(dir, "a.wav"), filepath.Join(dir, "sub", "b.wav")}) {
		t.Fatalf("recursive: %v", files)
	}
	if len(skipped) != 1 || skipped[0].Status != "failed" || skipped[0].Name != "missing.wav" || skipped[0].Error != "MetaRush can't find this file." {
		t.Fatalf("skipped: %+v", skipped)
	}
	if files, _ := Collect([]string{dir}, false); !slices.Equal(files, []string{filepath.Join(dir, "a.wav")}) {
		t.Fatalf("not recursive: %v", files)
	}
	if files, _ := Collect([]string{filepath.Join(dir, "a.wav")}, false); len(files) != 1 {
		t.Fatalf("a file: %v", files)
	}

	// Stopped before they were added: each file says so (nothing touched).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := Paths(ctx, nil, []string{dir}, Options{Recursive: true})
	if len(res) != 2 || res[0].Status != "skipped" || res[0].Error != "Stopped before it was added." {
		t.Fatalf("stopped: %+v", res)
	}
}

func TestFailTextSaysWhatHappened(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, ""},
		{context.Canceled, "It stopped before it finished. Add it again and it picks up where it stopped."},
		{fmt.Errorf("upload: %w", context.DeadlineExceeded), "It stopped before it finished. Add it again and it picks up where it stopped."},
		{&fs.PathError{Op: "open", Path: "/tmp/x", Err: fs.ErrNotExist}, "MetaRush can't find this file."},
		{&fs.PathError{Op: "open", Path: "/tmp/x", Err: fs.ErrPermission}, "MetaRush isn't allowed to read this file."},
		{&probe.ErrNotMedia{Reason: "zero duration"}, "It has no length, so there's nothing to add."},
		{&probe.ErrNotMedia{Reason: "no audio or video streams"}, "It isn't a video or sound file MetaRush can read."},
		{apperr.New(apperr.InvalidInput, "logo.png is larger than 512 MB."), "logo.png is larger than 512 MB."},
		{apperr.Wrap(apperr.Internal, errors.New("disk on fire"), "Internal trouble."), "Something went wrong while adding it: "},
		{errors.New("boom"), "Something went wrong while adding it: boom"},
	}
	for _, c := range cases {
		got := FailText(c.err)
		if c.want == "Something went wrong while adding it: " {
			if !strings.HasPrefix(got, c.want) {
				t.Errorf("%v: %q", c.err, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("%v: %q, want %q", c.err, got, c.want)
		}
	}
}

func TestHashWithProgress(t *testing.T) {
	p := writeAt(t, filepath.Join(t.TempDir(), "x.bin"), bytes.Repeat([]byte("metarush"), 1<<17), time.Minute)
	b, _ := os.ReadFile(p)
	sum := sha256.Sum256(b)
	var last int64
	got, err := hashWithProgress(p, int64(len(b)), func(d int64) { last = d })
	if err != nil || got != hex.EncodeToString(sum[:]) || last != int64(len(b)) {
		t.Fatalf("hash %q %v (last progress %d)", got, err, last)
	}
	if _, err := hashWithProgress(filepath.Join(t.TempDir(), "gone"), 0, func(int64) {}); err == nil {
		t.Fatal("a missing file hashed")
	}
}
