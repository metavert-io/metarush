package sound

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeeleven"
)

// What may leave (D290): a clip shared with project A, whose rules keep
// voices from AI services, isn't sent to the cloud from a cut of project B.
func TestCloudCleanUpFollowsTheClipsProjects(t *testing.T) {
	f := newSfx(t)
	ctx := context.Background()
	key := "sk" + "_" + strings.Repeat("cd34", 10) + "beef"
	fake := fakeeleven.New(key)
	defer fake.Close()
	t.Setenv(providers.EnvElevenLabsBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), "")
	if _, err := scall[providers.Status](t, f.a, "provider.set", providers.SetIn{Name: providers.ElevenLabsName, Key: key}); err != nil {
		t.Fatal(err)
	}
	rec := filepath.Join(f.dir, "talk.mp4")
	f.ffmpeg("-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30",
		"-f", "lavfi", "-i", "aevalsrc='0.3*sin(2*PI*220*t)':s=48000:c=mono", "-map", "0:v", "-map", "1:a", "-t", "6",
		"-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-c:a", "aac", rec)
	src := f.ingest(rec)
	f.transcript(src.ID, []turn{{Speaker: "Ben", Start: 0.5, End: 5.5, Text: "One two three."}})
	// Project A: the client, who keeps voices from AI services.
	pa := ids.New(ids.Project)
	now := time.Now().UTC()
	if err := f.a.DB.Insert(ctx, store.CProjects, models.Project{ID: pa, WorkspaceID: f.a.WorkspaceID(), Name: "Client A", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": src.ID}, bson.M{"$addToSet": bson.M{"projectIds": pa}}); err != nil {
		t.Fatal(err)
	}
	if _, err := scall[ops.ContentShowOut](t, f.a, "content.own", ops.ContentOwnIn{ProjectID: pa}); err != nil {
		t.Fatal(err)
	}
	if _, err := scall[leave.LeaveOut](t, f.a, "content.leave.set", leave.LeaveSetIn{ProjectID: pa, Keep: map[string][]string{"ai": {"voices"}}}); err != nil {
		t.Fatal(err)
	}
	if p, err := leave.Problem(ctx, f.a, leave.AI(providers.ElevenLabsName, "ElevenLabs", []string{models.LeaveVoices}, []string{pa}, "x")); err != nil || p == nil {
		t.Fatalf("A's rule should refuse: %+v %v", p, err)
	}
	rev := f.cut(editspec.Clip{ID: "talk", AssetID: src.ID, In: 0, Out: 6}) // a cut of B (f.project)
	_, err := scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: rev, Target: "Ben", Mode: "cloud", Apply: true})
	if len(fake.Isolations) > 0 || apperr.From(err).Code != apperr.KeptHere {
		t.Errorf("A's clip's voice from B's cut: %v (%d sent)", err, len(fake.Isolations))
	}
}

// Cloud clean-up refused when it is asked for (not an estimate) is recorded.
func TestCloudCleanUpRefusalIsRecorded(t *testing.T) {
	f := newSfx(t)
	ctx := context.Background()
	key := "sk" + "_" + strings.Repeat("cd34", 10) + "beef"
	fake := fakeeleven.New(key)
	defer fake.Close()
	t.Setenv(providers.EnvElevenLabsBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), "")
	if _, err := scall[providers.Status](t, f.a, "provider.set", providers.SetIn{Name: providers.ElevenLabsName, Key: key}); err != nil {
		t.Fatal(err)
	}
	rec := filepath.Join(f.dir, "talk.mp4")
	f.ffmpeg("-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30",
		"-f", "lavfi", "-i", "aevalsrc='0.3*sin(2*PI*220*t)':s=48000:c=mono", "-map", "0:v", "-map", "1:a", "-t", "6",
		"-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-c:a", "aac", rec)
	src := f.ingest(rec)
	f.transcript(src.ID, []turn{{Speaker: "Ben", Start: 0.5, End: 5.5, Text: "One two three."}})
	if _, err := scall[ops.ContentShowOut](t, f.a, "content.own", ops.ContentOwnIn{ProjectID: f.project}); err != nil {
		t.Fatal(err)
	}
	if _, err := scall[leave.LeaveOut](t, f.a, "content.leave.set", leave.LeaveSetIn{ProjectID: f.project, Keep: map[string][]string{"ai": {"voices"}}}); err != nil {
		t.Fatal(err)
	}
	rev := f.cut(editspec.Clip{ID: "talk", AssetID: src.ID, In: 0, Out: 6})
	_, err := scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: rev, Target: "Ben", Mode: "cloud", Apply: true})
	n, _ := f.a.DB.C(store.CAudit).CountDocuments(ctx, bson.M{"workspaceId": f.a.WorkspaceID(), "action": leave.AuditRefused})
	if n != 1 || len(fake.Isolations) > 0 || apperr.From(err).Code != apperr.KeptHere {
		t.Errorf("refused (%v) and recorded once: %d, %d sent", err, n, len(fake.Isolations))
	}
}
