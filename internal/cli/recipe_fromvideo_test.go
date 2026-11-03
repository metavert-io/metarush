package cli

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// `metarush recipe from-video <file>` marks the file a reference only when
// adding it made a new clip (D442): the person's own footage already in
// MetaRush (the same bytes) stays theirs, and the answer says so plainly.
func TestAFileAlreadyHereIsNeverMarkedAReference(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	video := func(name string, hz int) string {
		p := filepath.Join(dir, name)
		if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=2",
			"-f", "lavfi", "-i", "sine=frequency="+strconv.Itoa(hz)+":duration=2", "-shortest", "-pix_fmt", "yuv420p", p).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v %s", err, out)
		}
		return p
	}
	assetOf := func(path string) models.Asset {
		t.Helper()
		var as models.Asset
		if err := a.DB.C(store.CAssets).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "name": filepath.Base(path)}).Decode(&as); err != nil {
			t.Fatal(err)
		}
		return as
	}
	// The person's own video, added earlier.
	own := video("our-launch.mp4", 440)
	if _, err := ops.CallTyped[ingest.FilesOut](ctx, a, "ingest.files", ingest.FilesIn{Paths: []string{own}}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	r := runCLI(t, a.Cfg.Home(), "recipe", "from-video", own, "--json")
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &env) != nil || !strings.Contains(env.Data.Message, "already in MetaRush") || !strings.Contains(env.Data.Message, "stays your own footage") {
		t.Fatalf("the same file again: %d %s %s", r.code, r.stdout, r.stderr)
	}
	if as := assetOf(own); as.IsReference() {
		t.Fatalf("the person's own footage was marked a reference: %+v", as.Reference)
	}
	// Someone else's video, added now: a reference.
	theirs := video("their-tips.mp4", 660)
	if r := runCLI(t, a.Cfg.Home(), "recipe", "from-video", theirs, "--json"); r.code != 0 || strings.Contains(r.stdout, "already in MetaRush") {
		t.Fatalf("a new file: %d %s %s", r.code, r.stdout, r.stderr)
	}
	if as := assetOf(theirs); !as.IsReference() {
		t.Fatalf("a file added to start from is a reference: %+v", as)
	}
}
