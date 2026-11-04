package recipes

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// A draft keeps a link only when anyone may open it (D444): a clip from the
// person's cloud folder or a file of theirs gives no link and a title in
// MetaRush's own words, never the clip's; a recipe file leaves a private link
// out and says so, and keeps a public video page's.
func TestADraftKeepsOnlyAPublicLink(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	private := "https://www.dropbox.com/home/Clients/Acme Corp/Unreleased/competitor-teaser.mp4"
	box := seedReference(t, a, "")
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": box.ID}, bson.M{"$set": bson.M{"source": models.AssetSource{Platform: models.PlatformDropbox,
		URL: private, VideoID: "id:abc", Title: "competitor-teaser.mp4", Tool: "rclone", Method: models.SourceDownload}}}); err != nil {
		t.Fatal(err)
	}
	out := must[RecipeFromVideoOut](t, a, "recipe.fromVideo", RecipeFromVideoIn{From: box.ID}, false)
	if out.Recipe.Title != "Like a 26-second vertical video you liked" || strings.Contains(out.Sheet, "dropbox") || strings.Contains(out.Sheet, "Acme") {
		t.Fatalf("a cloud folder's clip: %q\n%s", out.Recipe.Title, out.Sheet)
	}
	// A file brought in (no source): MetaRush's words, none of the clip's.
	file := seedReference(t, a, "")
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": file.ID}, bson.M{"$unset": bson.M{"source": ""}}); err != nil {
		t.Fatal(err)
	}
	out2 := must[RecipeFromVideoOut](t, a, "recipe.fromVideo", RecipeFromVideoIn{From: file.ID}, false)
	r2, _ := loadRecipe(ctx, a, out2.Recipe.Name)
	v2, _ := loadVersion(ctx, a, r2, "draft")
	canon, _ := Canonical(&v2.Doc)
	noRefWords(t, "the file's draft", strings.ReplaceAll(string(canon), file.ID, ""))
	if strings.Contains(string(canon), "Fake video refshort001") {
		t.Fatalf("the draft holds the clip's title: %s", canon)
	}

	// A recipe file: a private link left out (and said), a public one kept.
	if _, err := a.DB.C(store.CRecipeVersions).UpdateOne(ctx, bson.M{"_id": v2.ID}, bson.M{"$set": bson.M{"doc.reference.url": private}}); err != nil {
		t.Fatal(err)
	}
	file1 := filepath.Join(t.TempDir(), "private.recipe.zip")
	must[RecipeExportOut](t, a, "recipe.export", RecipeExportIn{Recipe: out2.Recipe.Name, Version: "1", Out: file1}, false)
	rf, _, _ := readZip(t, file1)
	if ref := rf.Doc.Reference; ref == nil || ref.URL != "" || ref.AssetID != "" || !leftOutNamed(rf, "the link to the video it was drafted from") {
		t.Fatalf("a private link in a recipe file: %+v %+v", rf.Doc.Reference, rf.LeftOut)
	}
	pub := seedReference(t, a, "")
	out3 := must[RecipeFromVideoOut](t, a, "recipe.fromVideo", RecipeFromVideoIn{From: pub.ID, Name: "from-youtube"}, false)
	file2 := filepath.Join(t.TempDir(), "public.recipe.zip")
	must[RecipeExportOut](t, a, "recipe.export", RecipeExportIn{Recipe: out3.Recipe.Name, Version: "1", Out: file2}, false)
	if rf, _, _ := readZip(t, file2); rf.Doc.Reference == nil || rf.Doc.Reference.URL != "https://www.youtube.com/watch?v=refshort001" || rf.Doc.Reference.AssetID != "" {
		t.Fatalf("a public video page's link stays (its clip doesn't): %+v", rf.Doc.Reference)
	}
}

func leftOutNamed(rf *RecipeFile, what string) bool {
	for _, l := range rf.LeftOut {
		if l.What == what {
			return true
		}
	}
	return false
}

// A draft from a video is made once (D444): asking while another computer
// holds the claim waits for it; a claim gone stale by the server's time is
// taken by the daemon's sweep (a computer stopped mid-draft); asking again
// gives the same draft; two asks at once make one.
func TestADraftFromAVideoIsMadeOnce(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	recipesNow := func() int64 {
		n, _ := a.DB.C(store.CRecipes).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID()})
		return n
	}
	ref := seedReference(t, a, "")
	held := time.Now().UTC()
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": ref.ID}, bson.M{"$set": bson.M{"startFrom": models.StartFromVideo{For: a.Actor, State: models.StartFromMaking, At: held, MakingAt: &held}}}); err != nil {
		t.Fatal(err)
	}
	before := recipesNow()
	out := must[RecipeFromVideoOut](t, a, "recipe.fromVideo", RecipeFromVideoIn{From: ref.ID}, false)
	if out.State != FromVideoLooking || !strings.Contains(out.Message, "drafting from") || recipesNow() != before {
		t.Fatalf("another computer is drafting it: %s %q, recipes %d → %d", out.State, out.Message, before, recipesNow())
	}
	if n := SweepDrafts(ctx, a); n != 0 || recipesNow() != before {
		t.Fatalf("a fresh claim is left alone: %d", n)
	}
	// The computer stopped mid-draft: its claim goes stale, and the sweep drafts.
	stale := time.Now().UTC().Add(-11 * time.Minute)
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": ref.ID}, bson.M{"$set": bson.M{"startFrom.makingAt": stale}}); err != nil {
		t.Fatal(err)
	}
	if n := SweepDrafts(ctx, a); n != 1 || recipesNow() != before+1 {
		t.Fatalf("the sweep drafts a stale claim once: %d, recipes %d", n, recipesNow())
	}
	if n := SweepDrafts(ctx, a); n != 0 {
		t.Fatalf("and only once: %d", n)
	}
	again := must[RecipeFromVideoOut](t, a, "recipe.fromVideo", RecipeFromVideoIn{From: ref.ID}, false)
	if again.State != FromVideoDrafted || !again.Existing || recipesNow() != before+1 {
		t.Fatalf("asked again: %s %v", again.State, again.Existing)
	}

	// Two asks at once (a terminal's --wait and the daemon): one draft.
	two := seedReference(t, a, "")
	before = recipesNow()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = call[RecipeFromVideoOut](t, a, "recipe.fromVideo", RecipeFromVideoIn{From: two.ID}, false)
		}()
	}
	wg.Wait()
	if n := recipesNow() - before; n != 1 {
		t.Fatalf("two asks at once made %d drafts", n)
	}
}
