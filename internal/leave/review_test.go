package leave_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/receipt"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// strict makes Base (the default's settings, nothing set), Acme (nothing of
// it leaves) and Other (no rules), and a clip of Acme's with words.
func strict(t *testing.T) (a *app.App, acme, other models.Project, clip models.Asset) {
	t.Helper()
	t.Setenv("CLAUDECODE", "")
	a = testapp.New(t)
	ctx := context.Background()
	do[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Base"}, ops.Caller{})
	acme = do[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Acme"}, ops.Caller{}).Project
	other = do[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Other"}, ops.Caller{}).Project
	do[ops.ContentShowOut](t, a, "content.own", ops.ContentOwnIn{ProjectID: acme.ID}, ops.Caller{})
	do[leave.LeaveOut](t, a, "content.leave.set", leave.LeaveSetIn{ProjectID: acme.ID, Nothing: ptr(true)}, person)
	now := time.Now().UTC()
	clip = models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{acme.ID}, Kind: "video", Name: "secret.mov",
		SHA256: strings.Repeat("ab", 32), Size: 1000, MIME: "video/quicktime", StorageKey: "k", State: models.AssetAvailable,
		Media:     models.MediaInfo{Container: "mov", Duration: 30, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}},
		Variants:  map[string]models.Variant{models.VariantContactSheet: {Kind: models.VariantContactSheet, StorageKey: "k2", SHA256: strings.Repeat("cd", 32), Size: 10, MIME: "image/jpeg"}},
		CreatedAt: now, UpdatedAt: now, Transcript: &models.TranscriptStatus{State: "available", Precision: "word", Language: "en", Segments: 1, Words: 4}}
	if err := a.DB.Insert(ctx, store.CAssets, clip); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Insert(ctx, store.CSegments, models.TranscriptSegment{ID: ids.New("seg"), WorkspaceID: a.WorkspaceID(), AssetID: clip.ID, From: 1, To: 4,
		Text: "Acme merger closes Friday", Words: []models.Word{{Text: "Acme", From: 1, To: 1.5}, {Text: "merger", From: 1.5, To: 2}, {Text: "closes", From: 2, To: 3}, {Text: "Friday", From: 3, To: 4}}}); err != nil {
		t.Fatal(err)
	}
	return
}

func asVia(a *app.App, via, agent string) *app.App {
	c := *a
	c.Actor.Via, c.Actor.Agent = via, agent
	return &c
}

func codeOf(a *app.App, ctx context.Context, op string, in any, c ops.Caller) (any, string) {
	raw, _ := json.Marshal(in)
	out, err := ops.Call(ctx, a, op, raw, c)
	if err != nil {
		return nil, apperr.From(err).Code
	}
	return out, ""
}

// A clip in two projects counts under each: Other's cut sheet shows Acme's
// clip, so Claude can't read it; nor a quality check of Acme's by its ID, nor
// Acme's clip's card (its contact sheet's hash); reads that say they show
// nothing of a client's still work.
func TestSharedThingsCountUnderEveryProject(t *testing.T) {
	a, acme, other, clip := strict(t)
	ctx := context.Background()
	claude := asVia(a, models.ViaAssistant, "")
	do[ops.AssetMoveOut](t, a, "asset.move", ops.AssetMoveIn{AssetID: clip.ID, To: other.ID}, person) // into Other too (an owner, a person)
	plan := fmt.Sprintf(`{"projectId":%q,"instruction":"cut","spec":{"schemaVersion":1,"title":"Cut","output":{"preset":"vertical-1080p","fps":30},"clips":[{"id":"a","assetId":%q,"in":0.5,"out":4.5}]}}`, other.ID, clip.ID)
	dash := asVia(a, "dashboard", "")
	if _, err := ops.CallTyped[ops.EditApplyOut](ctx, dash, "edit.apply", ops.EditApplyIn{Plan: json.RawMessage(plan)}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string]any{
		"cut.show":        ops.CutShowIn{ProjectID: other.ID},
		"transcript.find": ops.TranscriptFindIn{Query: "merger", ProjectID: other.ID},
		"asset.show":      map[string]any{"id": clip.ID},
	} {
		if out, code := codeOf(claude, ctx, name, in, ops.Caller{}); code != apperr.KeptHere {
			b, _ := json.Marshal(out)
			t.Errorf("%s: %s %.200s", name, code, b)
		}
	}
	qar := models.QAReport{ID: ids.New(models.QAReportPrefix), WorkspaceID: a.WorkspaceID(), ProjectID: acme.ID}
	_ = a.DB.Insert(ctx, store.CQAReports, qar)
	if err := ops.AIReads(ctx, claude, "qa.show", &ops.QAShowIn{ID: qar.ID}); apperr.From(err).Code != apperr.KeptHere {
		t.Errorf("a quality check of Acme's by its ID: %v", err)
	}
	if err := ops.AIReads(ctx, claude, "resources.path", map[string]any{"projectId": acme.ID, "ref": "logo"}); apperr.From(err).Code != apperr.KeptHere {
		t.Errorf("a picture of Acme's: %v", err)
	}
	if err := ops.AIReads(ctx, claude, "voiceover.show", map[string]any{"revisionId": "rev_x"}); apperr.From(err).Code != apperr.KeptHere {
		t.Errorf("a version nobody can place counts every project: %v", err)
	}
	if err := ops.AIReads(ctx, claude, "workspace.show", map[string]any{}); err != nil {
		t.Errorf("a read that shows nothing of a client's: %v", err)
	}
	if err := ops.AIReads(ctx, claude, "no.such.read", map[string]any{}); apperr.From(err).Code != apperr.KeptHere {
		t.Errorf("a read that doesn't say what it shows is refused: %v", err)
	}
	// A person reads them.
	if _, code := codeOf(a, ctx, "cut.show", ops.CutShowIn{ProjectID: other.ID}, ops.Caller{}); code == apperr.KeptHere {
		t.Error("a person reads Other's cut")
	}
}

// Letting more leave takes a person who is an owner: never an AI app (which
// counts as the owner on this computer), never a member moving a clip out.
func TestLooseningTakesAPersonWhoIsAnOwner(t *testing.T) {
	a, acme, other, clip := strict(t)
	ctx := context.Background()
	mcp := asVia(a, "mcp", "codex")
	if _, code := codeOf(mcp, ctx, "content.follow", ops.ContentFollowIn{ProjectID: acme.ID, UseDefault: true}, ops.Caller{}); code != apperr.HumanOnly {
		t.Fatalf("an AI app switching Acme to looser settings: %s", code)
	}
	if r, _, _ := leave.For(ctx, a, acme.ID); r == nil || !r.Nothing {
		t.Fatal("Acme's rules held")
	}
	if _, code := codeOf(a, member(), "asset.move", ops.AssetMoveIn{AssetID: clip.ID, From: acme.ID, To: other.ID}, ops.Caller{Human: true, Remote: true}); code != apperr.Unauthorized {
		t.Fatalf("a member moving Acme's clip out: %s", code)
	}
	if _, code := codeOf(a, member(), "asset.link", ops.AssetLinkIn{AssetID: clip.ID, ProjectID: acme.ID, Unlink: true}, ops.Caller{Human: true, Remote: true}); code != apperr.Unauthorized {
		t.Fatalf("a member taking Acme's clip out of Acme: %s", code)
	}
	if _, code := codeOf(a, member(), "project.link", ops.ProjectLinkIn{ProjectID: other.ID, From: acme.ID}, ops.Caller{Human: true, Remote: true}); code != apperr.Unauthorized {
		t.Fatalf("a member letting Other use Acme's things: %s", code)
	}
	if _, code := codeOf(a, ctx, "asset.move", ops.AssetMoveIn{AssetID: clip.ID, From: acme.ID, To: other.ID}, ops.Caller{}); code != apperr.HumanOnly {
		t.Fatalf("a script moving Acme's clip out: %s", code)
	}
	if _, code := codeOf(a, ctx, "asset.move", ops.AssetMoveIn{AssetID: clip.ID, From: acme.ID, To: other.ID}, person); code != "" {
		t.Fatalf("an owner, a person: %s", code)
	}
}

// Storage: archiving a client's project doesn't open the way to a bucket, and
// pictures added anywhere meet the rule.
func TestStorageCountsArchivedProjectsAndPictures(t *testing.T) {
	a, acme, _, _ := strict(t)
	ctx := context.Background()
	cloud := config.Media{Provider: "s3", S3: config.S3{Endpoint: "https://storage.example.com", Bucket: "media"}}
	if _, code := codeOf(a, member(), "project.delete", map[string]any{"id": acme.ID}, ops.Caller{Human: true, Remote: true}); code != "" {
		t.Fatalf("archiving: %s", code)
	}
	if err := ops.LeaveStorage(ctx, a, cloud); apperr.From(err).Code != apperr.KeptHere {
		t.Fatalf("a move to a bucket with an archived client: %v", err)
	}
	inCloud := *a
	cfg := *a.Cfg
	cfg.Media = cloud
	inCloud.Cfg = &cfg
	pic := filepath.Join(t.TempDir(), "logo.svg")
	_ = os.WriteFile(pic, []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10"/></svg>`), 0o600)
	if _, err := resources.AddFile(ctx, &inCloud, pic, resources.AddOptions{ProjectID: acme.ID, Title: "Logo"}); apperr.From(err).Code != apperr.KeptHere {
		t.Fatalf("a picture of Acme's into a bucket: %v", err)
	}
}

// The record: a read an AI app made, logged only by the clip, counts under
// the clip's project.
func TestReceiptCountsReadsLoggedByClip(t *testing.T) {
	t.Setenv("CLAUDECODE", "")
	a := testapp.New(t)
	ctx := context.Background()
	p := do[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Plain"}, ops.Caller{}).Project
	now := time.Now().UTC()
	clip := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.ID}, Kind: "video", Name: "a.mov", State: models.AssetAvailable, CreatedAt: now, UpdatedAt: now,
		Media: models.MediaInfo{Streams: []models.Stream{}}, Variants: map[string]models.Variant{}}
	if err := a.DB.Insert(ctx, store.CAssets, clip); err != nil {
		t.Fatal(err)
	}
	if l := do[receipt.ProjectLeavesOut](t, a, "project.leaves", receipt.ProjectLeavesIn{ProjectID: p.ID}, ops.Caller{}); !l.Left.Nothing {
		t.Fatalf("nothing yet: %+v", l.Left)
	}
	_, err := a.DB.C(store.CLogEntries).InsertOne(ctx, bson.M{"_id": ids.New("log"), "workspaceId": a.WorkspaceID(), "at": now, "kind": "op", "level": "info", "channel": "mcp",
		"agent": "codex", "summary": "x", "installationId": "i", "refs": bson.M{"assetId": clip.ID}, "op": bson.M{"name": "footage.transcript", "ok": true, "mutating": false}})
	if err != nil {
		t.Fatal(err)
	}
	l := do[receipt.ProjectLeavesOut](t, a, "project.leaves", receipt.ProjectLeavesIn{ProjectID: p.ID}, ops.Caller{})
	if l.Left.Nothing || len(l.Left.Places) != 1 || l.Left.Places[0].To != "the AI app codex" || strings.Join(l.Left.Places[0].Kinds, ",") != models.LeaveWords {
		t.Fatalf("the read counts: %+v", l.Left)
	}
}

// Remotion: pushing back to a repository and refreshes are sends.
func TestRemotionSends(t *testing.T) {
	s := leave.RepoPush("prj_x", "https://github.com/acme/teaser.git", "github.com/acme/teaser", "v")
	if s.Place != models.PlaceDelivery || s.To != "github.com" || len(s.Kinds) != 2 {
		t.Fatalf("push: %+v", s)
	}
	if s := leave.RepoPush("prj_x", "git@example.com:acme/teaser.git", "x", "v"); s.To != "example.com" {
		t.Fatalf("ssh form: %+v", s)
	}
	a, acme, _, _ := strict(t)
	if r := leave.KeptRefusal(context.Background(), a, leave.Refresh(acme.ID, "a refresh")); r == nil {
		t.Fatal("a refresh of Acme's source is kept here")
	}
	if r := leave.KeptRefusal(context.Background(), a, leave.RepoPush(acme.ID, "https://github.com/acme/teaser.git", "github.com/acme/teaser", "v")); r == nil {
		t.Fatal("a push of Acme's source is kept here")
	}
}

// A keep card that gives a project its own settings back (its own from before)
// asks What may leave first: no looser settings without a person who is an owner.
func TestKeepCardOwnSettingsAskFirst(t *testing.T) {
	a, acme, other, _ := strict(t)
	ctx := context.Background()
	do[ops.ContentShowOut](t, a, "content.own", ops.ContentOwnIn{ProjectID: other.ID}, person) // its own, with a house rule…
	do[ops.HouseRulesOut](t, a, "content.rules.add", ops.HouseRuleAddIn{ProjectID: other.ID, Text: "Captions at the top"}, person)
	do[ops.ContentShowOut](t, a, "content.follow", ops.ContentFollowIn{ProjectID: other.ID, Follow: acme.ID}, person) // …set aside: it follows Acme
	pic := filepath.Join(t.TempDir(), "logo.svg")
	_ = os.WriteFile(pic, []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10"/></svg>`), 0o600)
	res, err := resources.AddFile(ctx, a, pic, resources.AddOptions{ProjectID: other.ID, Title: "Logo"})
	if err != nil {
		t.Fatal(err)
	}
	in := ops.KeepAddIn{ProjectID: other.ID, Kind: "picture", Detail: models.KeepDetail{ResourceID: res.Resource.ID, BrandKit: true}}
	if _, code := codeOf(a, ctx, "keep.add", in, ops.Caller{}); code != apperr.HumanOnly {
		t.Fatalf("a keep card switching to looser settings by itself: %s", code)
	}
	if r, owner, _ := leave.For(ctx, a, other.ID); owner != acme.ID || r == nil || !r.Nothing {
		t.Fatalf("Other still follows Acme: %s %+v", owner, r)
	}
}

// What people read about Claude and AI apps says plainly that the rules hold
// only for what MetaRush hands them.
func TestClaudePlaceSaysFilesAreOpenToTerminals(t *testing.T) {
	a, acme, _, _ := strict(t)
	out := do[leave.LeaveOut](t, a, "content.leave", leave.LeaveIn{ProjectID: acme.ID}, ops.Caller{})
	for _, p := range out.Places {
		if p.Place == models.PlaceClaude && !strings.Contains(p.Says, "open files on this computer directly") {
			t.Fatalf("the plain line: %q", p.Says)
		}
	}
}

// A terminal Claude Code started (its variable inherited, as by a tmux
// session) is taken for Claude Code; the refusal says so and what to do.
func TestClaudeCodeTerminalIsToldWhy(t *testing.T) {
	a, acme, _, _ := strict(t)
	t.Setenv("CLAUDECODE", "1")
	cli := asVia(a, "cli", "")
	err := ops.AIReads(context.Background(), cli, "transcript.find", ops.TranscriptFindIn{Query: "x", ProjectID: acme.ID})
	e := apperr.From(err)
	d, _ := e.Details.(map[string]any)
	if e.Code != apperr.KeptHere || !strings.Contains(e.Hint, "Claude Code didn't start") || d["claudeCodeTerminal"] != true {
		t.Fatalf("the terminal's refusal: %v (%s) %v", err, e.Hint, d)
	}
}
