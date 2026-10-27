package recipes

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakerclone"
	"github.com/metavert-io/metarush/internal/tools"
)

// fakeDrive gives the fixture's app a fake rclone with a Google Drive
// "gdrive" whose folder Teaser/Art is root/Teaser/Art, and returns that folder
// and the fake's call log.
func (f *fx) fakeDrive(st fakerclone.State) (string, string) {
	f.t.Helper()
	root := f.t.TempDir()
	art := filepath.Join(root, "Teaser", "Art")
	if err := os.MkdirAll(art, 0o755); err != nil {
		f.t.Fatal(err)
	}
	if st.Remotes == nil {
		st.Remotes = []fakerclone.Remote{{Name: "gdrive", Type: "drive", Root: root}}
	}
	tool, _ := fakerclone.Write(f.t, st)
	log := filepath.Join(f.t.TempDir(), "rclone.log")
	f.t.Setenv("METARUSH_FAKE_RCLONE_LOG", log)
	f.useRclone(tool)
	return art, log
}

// useRclone points the app at an rclone (a fake, or a path where there is none).
func (f *fx) useRclone(tool string) {
	cfg := f.a.Cfg.Tools
	cfg.Rclone = tool
	ts := tools.New(cfg)
	ts.OnRun = f.a.Tools.OnRun
	f.a.Tools = ts
}

// drawPicture makes a PNG of one colour at t (its time in the Drive).
func drawPicture(t *testing.T, path, color string, at time.Time) {
	t.Helper()
	ffmpeg(t, "-f", "lavfi", "-i", "color=c="+color+":size=320x240", "-frames:v", "1", path)
	_ = os.Chtimes(path, at, at)
}

// copyIDs counts the files the fake rclone fetched.
func copyIDs(t *testing.T, log string) int {
	t.Helper()
	fh, err := os.Open(log)
	if err != nil {
		return 0
	}
	defer fh.Close()
	n := 0
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		var args []string
		_ = json.Unmarshal(sc.Bytes(), &args)
		for _, a := range args {
			if a == "copyid" {
				n++
			}
		}
	}
	return n
}

// A pictures input takes the newest picture of a Google Drive folder through
// the person's own rclone (D220): the plan lists the folder and says the run
// fetches it; the run fetches only that file, keeps it in the workspace library
// tied to its Drive file and records its Drive ID and hash; the same file
// isn't fetched again while Google's checksum says it is the same; new art in
// the file is a new version of the same picture. Checks never list the Drive;
// without rclone the plan says how to set it up.
func TestPicturesFromAGoogleDriveFolder(t *testing.T) {
	f := setup(t)
	f.save(RecipeSaveIn{})
	art, log := f.fakeDrive(fakerclone.State{})
	old := time.Now().Add(-time.Hour)
	drawPicture(t, filepath.Join(art, "old.png"), "yellow", old)
	drawPicture(t, filepath.Join(art, "blue.png"), "blue", old.Add(time.Minute))
	if err := os.WriteFile(filepath.Join(art, "notes.txt"), []byte("not a picture"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The input takes its pictures from the folder (a draft a person makes current).
	up := must[RecipeUpdateOut](t, f.a, "recipe.update", RecipeUpdateIn{RecipeRefIn: RecipeRefIn{Recipe: "app-teaser"}, FromDrive: map[string]string{"hero": "gdrive:Teaser/Art/"}}, false)
	if up.Same || !strings.Contains(strings.Join(changeLines(up.Changes), " "), "a run takes the newest files of “Teaser/Art” in the Google Drive “gdrive”") {
		t.Fatalf("update %+v", up)
	}
	must[RecipePublishOut](t, f.a, "recipe.publish", RecipeRefIn{Recipe: "app-teaser", Version: "2"}, true)
	r, _ := loadRecipe(f.ctx, f.a, "app-teaser")
	v, _ := loadVersion(f.ctx, f.a, r, "")
	if hero := inputOf(&v.Doc, models.RecipePictures); hero.From == nil || hero.From.Drive != "gdrive:Teaser/Art" {
		t.Fatalf("hero %+v", hero)
	}
	// Its sheet says so and reads back to the same version.
	co := must[RecipeCheckoutOut](t, f.a, "recipe.checkout", RecipeRefIn{Recipe: "app-teaser"}, false)
	if !strings.Contains(co.Sheet, `from drive "gdrive:Teaser/Art"`) || !strings.Contains(co.Sheet, "the newest files of “Teaser/Art” in the Google Drive “gdrive”") {
		t.Fatalf("sheet:\n%s", co.Sheet)
	}
	if chk := must[RecipeCheckOut](t, f.a, "recipe.check", RecipeCheckIn{Sheet: co.Sheet}, false); !chk.Valid || !chk.Same {
		t.Fatalf("the sheet reads back: %+v", chk)
	}

	plan := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser"}, false)
	hero := runInput(plan.Inputs, "hero")
	if !plan.Ready || hero == nil || hero.Drive != "gdrive:Teaser/Art" || len(hero.Items) != 1 || !pendingDrive(hero.Items[0]) {
		t.Fatalf("plan %+v hero %+v", plan.Problems, hero)
	}
	it := hero.Items[0]
	if it.Drive.ID != fakerclone.ID("gdrive", "Teaser/Art/blue.png") || it.Drive.Name != "blue.png" || !strings.HasPrefix(it.Drive.Checksum, "sha256:") || it.Title != "blue" {
		t.Fatalf("the newest picture, still in the Drive: %+v %+v", it, it.Drive)
	}
	if s := plan.Steps[0]; s.ID != stepInputs || !strings.Contains(s.Detail, "Fetches 1 file from Google Drive") {
		t.Fatalf("the plan says it fetches: %+v", s)
	}
	if copyIDs(t, log) != 0 {
		t.Fatal("planning fetched a file")
	}
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: RecipePlanIn{Recipe: "app-teaser"}, Fingerprint: plan.Fingerprint}, true).Run
	if copyIDs(t, log) != 1 {
		t.Fatalf("fetched %d files", copyIDs(t, log))
	}
	got := runInput(run.Inputs, "hero").Items[0]
	if got.ID == "" || got.Drive == nil || !got.Drive.Fetched || "sha256:"+got.SHA256 != it.Drive.Checksum {
		t.Fatalf("the run records the picture it fetched: %+v", got)
	}
	pic := resources.DriveFileResource(f.ctx, f.a, it.Drive.ID)
	if pic == nil || pic.ID != got.ID || pic.Scope != models.ScopeWorkspace || pic.DriveFile.Folder != "gdrive:Teaser/Art" || pic.Latest().Source.Kind != models.FromDrive ||
		!strings.Contains(pic.Latest().Source.URL, it.Drive.ID) {
		t.Fatalf("kept in the workspace library, tied to its Drive file: %+v", pic)
	}
	if !strings.Contains(stepOf(&run.RecipeRun, stepInputs).Detail, "1 file fetched from Google Drive") {
		t.Fatalf("inputs step: %+v", stepOf(&run.RecipeRun, stepInputs))
	}
	if head := f.revision(run.Revision); head.Spec.Resources["hero"].ID != pic.ID || head.Spec.Resources["hero"].SHA256 != got.SHA256 {
		t.Fatalf("the cut shows it: %+v", head.Spec.Resources)
	}

	// Planned again: the same file, the same checksum — nothing to fetch.
	again := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser"}, false)
	if same := runInput(again.Inputs, "hero").Items[0]; same.ID != pic.ID || pendingDrive(same) || strings.Contains(again.Steps[0].Detail, "Fetches") {
		t.Fatalf("reused: %+v %+v", same, again.Steps[0])
	}
	// New art in the same file: fetched again, a new version of the same picture.
	drawPicture(t, filepath.Join(art, "blue.png"), "red", time.Now())
	next := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser"}, false)
	if !pendingDrive(runInput(next.Inputs, "hero").Items[0]) {
		t.Fatalf("new art is fetched: %+v", runInput(next.Inputs, "hero"))
	}
	run2 := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: RecipePlanIn{Recipe: "app-teaser"}, Fingerprint: next.Fingerprint}, true).Run
	got2 := runInput(run2.Inputs, "hero").Items[0]
	pic2 := resources.DriveFileResource(f.ctx, f.a, it.Drive.ID)
	if got2.ID != pic.ID || got2.SHA256 == got.SHA256 || len(pic2.Versions) != 2 || got2.Version != 2 {
		t.Fatalf("a new version of the same picture: %+v (%d versions)", got2, len(pic2.Versions))
	}

	// A check never lists the Drive: it is fine without rclone.
	f.useRclone(filepath.Join(t.TempDir(), "rclone"))
	n := copyIDs(t, log)
	if chk := must[RecipeCheckOut](t, f.a, "recipe.check", RecipeCheckIn{Recipe: "app-teaser"}, false); !chk.Check.OK {
		t.Fatalf("check %+v", chk.Check.Problems)
	}
	// Without rclone the plan says how to set it up.
	bad := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser"}, false)
	if bad.Ready || !strings.Contains(strings.Join(bad.Problems, " "), "isn't installed on this computer") || !strings.Contains(strings.Join(bad.Problems, " "), "rclone's own setup") {
		t.Fatalf("no rclone: %+v", bad.Problems)
	}
	if copyIDs(t, log) != n {
		t.Fatal("fetched without rclone")
	}
}

// Footage from a Google Drive folder: the run fetches the clip, adds it as
// footage that says where it came from (Google Drive, its ID), and it joins
// the run's project for Claude to choose the moments.
func TestFootageFromAGoogleDriveFolder(t *testing.T) {
	f := setup(t)
	f.save(RecipeSaveIn{})
	art, log := f.fakeDrive(fakerclone.State{})
	clip := filepath.Join(art, "episode.mp4")
	ffmpeg(t, "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30", "-f", "lavfi", "-i", "sine=frequency=330:sample_rate=48000", "-t", "8",
		"-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", clip)
	must[RecipeUpdateOut](t, f.a, "recipe.update", RecipeUpdateIn{RecipeRefIn: RecipeRefIn{Recipe: "app-teaser"}, FromDrive: map[string]string{"footage": "gdrive:Teaser/Art"}}, false)
	must[RecipePublishOut](t, f.a, "recipe.publish", RecipeRefIn{Recipe: "app-teaser"}, true)
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser"}, false)
	fi := runInput(plan.Inputs, "footage")
	if !plan.Ready || fi == nil || len(fi.Items) != 1 || !pendingDrive(fi.Items[0]) || !plan.Claude {
		t.Fatalf("plan %+v footage %+v", plan.Problems, fi)
	}
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: RecipePlanIn{Recipe: "app-teaser"}, Fingerprint: plan.Fingerprint}, true).Run
	got := runInput(run.Inputs, "footage").Items[0]
	var as models.Asset
	if err := f.a.DB.GetByID(f.ctx, store.CAssets, f.a.WorkspaceID(), got.ID, &as); err != nil {
		t.Fatalf("the clip: %+v %v", got, err)
	}
	if as.Source == nil || as.Source.Platform != models.PlatformDrive || as.Source.VideoID != fakerclone.ID("gdrive", "Teaser/Art/episode.mp4") || as.Source.Tool != "rclone" {
		t.Fatalf("provenance %+v", as.Source)
	}
	if !containsString(as.ProjectIDs, run.ProjectID) || got.Duration < 7 || copyIDs(t, log) != 1 {
		t.Fatalf("joins the project: %+v %+v", as.ProjectIDs, got)
	}
	if run.State != models.RunWaiting || run.WaitingFor != models.RunWaitsForClaude {
		t.Fatalf("waits for Claude: %+v", run.RecipeRun)
	}
	// The same clip planned again isn't fetched again (Google's checksum is the same).
	again := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser"}, false)
	if it := runInput(again.Inputs, "footage").Items[0]; it.ID != as.ID || pendingDrive(it) {
		t.Fatalf("reused: %+v", it)
	}
}

// An input can only take pictures or footage from a Drive folder, and the
// folder must be written the way rclone names one.
func TestDriveFoldersAreChecked(t *testing.T) {
	f := setup(t)
	f.save(RecipeSaveIn{})
	for in, want := range map[string]string{"headline": "Only pictures and footage", "hero": "written as the rclone remote", "nope": "no input called"} {
		folder := "gdrive:Art"
		if in == "hero" {
			folder = ":local:/etc"
		}
		_, err := call[RecipeUpdateOut](t, f.a, "recipe.update", RecipeUpdateIn{RecipeRefIn: RecipeRefIn{Recipe: "app-teaser"}, FromDrive: map[string]string{in: folder}}, false)
		if codeOf(err) != apperr.InvalidInput || !strings.Contains(apperr.From(err).Message, want) {
			t.Errorf("%s: %v", in, err)
		}
	}
}

func changeLines(parts []RecipeChangePart) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p.Changes...)
	}
	return out
}

// A pictures input from a Dropbox folder (D306), the same way: the plan lists
// it through rclone and says the run fetches it from Dropbox; the run fetches
// it by its Dropbox ID, keeps it tied to its file with where it came from; the
// sheet spells it and reads back.
func TestPicturesFromADropboxFolder(t *testing.T) {
	f := setup(t)
	f.save(RecipeSaveIn{})
	root := t.TempDir()
	art := filepath.Join(root, "Teaser", "Art")
	if err := os.MkdirAll(art, 0o755); err != nil {
		t.Fatal(err)
	}
	f.fakeDrive(fakerclone.State{Remotes: []fakerclone.Remote{{Name: "dropbox", Type: "dropbox", Root: root}}})
	drawPicture(t, filepath.Join(art, "blue.png"), "blue", time.Now().Add(-time.Minute))

	up := must[RecipeUpdateOut](t, f.a, "recipe.update", RecipeUpdateIn{RecipeRefIn: RecipeRefIn{Recipe: "app-teaser"}, FromDropbox: map[string]string{"hero": "dropbox:Teaser/Art"}}, false)
	if up.Same || !strings.Contains(strings.Join(changeLines(up.Changes), " "), "“Teaser/Art” in the Dropbox “dropbox”") {
		t.Fatalf("update %+v", changeLines(up.Changes))
	}
	must[RecipePublishOut](t, f.a, "recipe.publish", RecipeRefIn{Recipe: "app-teaser", Version: "2"}, true)
	co := must[RecipeCheckoutOut](t, f.a, "recipe.checkout", RecipeRefIn{Recipe: "app-teaser"}, false)
	if !strings.Contains(co.Sheet, `from dropbox "dropbox:Teaser/Art"`) {
		t.Fatalf("sheet:\n%s", co.Sheet)
	}
	if chk := must[RecipeCheckOut](t, f.a, "recipe.check", RecipeCheckIn{Sheet: co.Sheet}, false); !chk.Valid || !chk.Same {
		t.Fatalf("the sheet reads back: %+v", chk)
	}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser"}, false)
	hero := runInput(plan.Inputs, "hero")
	if !plan.Ready || hero == nil || hero.DriveKind != "dropbox" || len(hero.Items) != 1 || !strings.HasPrefix(hero.Items[0].Drive.ID, "id:") ||
		!strings.HasPrefix(hero.Items[0].Drive.Checksum, "dropbox:") || !strings.Contains(plan.Steps[0].Detail, "Fetches 1 file from Dropbox") {
		t.Fatalf("plan %+v hero %+v steps %+v", plan.Problems, hero, plan.Steps[0])
	}
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: RecipePlanIn{Recipe: "app-teaser"}, Fingerprint: plan.Fingerprint}, true).Run
	got := runInput(run.Inputs, "hero").Items[0]
	pic := resources.DriveFileResource(f.ctx, f.a, hero.Items[0].Drive.ID)
	if got.ID == "" || pic == nil || pic.ID != got.ID || pic.Latest().Source.Kind != models.FromDropbox || pic.Latest().Source.Site != "Dropbox" ||
		!strings.Contains(stepOf(&run.RecipeRun, stepInputs).Detail, "1 file fetched from Dropbox") {
		t.Fatalf("fetched %+v pic %+v step %+v", got, pic, stepOf(&run.RecipeRun, stepInputs))
	}
}
