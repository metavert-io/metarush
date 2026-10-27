package receipt_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/receipt"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func insert(t *testing.T, a *app.App, coll string, doc any) {
	t.Helper()
	if err := a.DB.Insert(context.Background(), coll, doc); err != nil {
		t.Fatal(err)
	}
}

// The receipt (D240): generation records read generically (a kind of AI work
// it has never seen appears too), the AI voice and cloud clean-up, Claude's
// conversations with whose account answered, the activity log's requests,
// generated media, Auto's spending, and the cost at cost per version and
// project.
func TestProjectReceipt(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	p := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: "Garden story", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	insert(t, a, store.CProjects, p)
	person := models.Actor{UserID: "usr_theo", UserName: "Theo", Via: "dashboard"}

	// Footage: a recording and a generated shot.
	rec := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.ID}, Kind: "video", Name: "talk.mov", SHA256: strings.Repeat("a", 64),
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: 30, HasVideo: true, HasAudio: true}, CreatedAt: now, UpdatedAt: now}
	shot := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.ID}, Kind: "video", Name: "beach.mp4", Title: "Beach at dusk", SHA256: strings.Repeat("b", 64),
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: 4, HasVideo: true}, Source: &models.AssetSource{Platform: "generated", Method: "generated"}, CreatedAt: now, UpdatedAt: now}
	insert(t, a, store.CAssets, rec)
	insert(t, a, store.CAssets, shot)

	// A generated shot: settled, deleted at the provider.
	gen := models.Generation{ID: ids.New("gen"), WorkspaceID: a.WorkspaceID(), ProjectID: p.ID, Operation: "shot", Provider: "gemini", ProviderTitle: "Google Gemini",
		Model: "omni-flash", ModelTitle: "Gemini Omni Flash", Prompt: "A beach at dusk", References: []models.GenReference{{Role: "start-frame", Ref: "talk@12.40", SHA256: strings.Repeat("c", 64)}},
		Estimate: models.GenCost{USD: 0.42, Basis: "estimate"}, Settled: &models.GenSettlement{USD: 0.4, At: now},
		Outcome:   &models.GenOutcome{State: models.GenDone, At: now, Outputs: []models.GenOutput{{AssetID: shot.ID}}},
		Forgotten: &models.GenForgotten{At: now, OK: true}, CreatedBy: person, Channel: "dashboard", JobID: "job_gen1", CreatedAt: now}
	insert(t, a, store.CGenerations, gen)
	// A kind of AI work this receipt has never seen, recorded with other field names.
	insert(t, a, store.CGenerations, bson.M{"_id": ids.New("gen"), "workspaceId": a.WorkspaceID(), "projectId": p.ID, "kind": "music",
		"provider": "elevenlabs", "model": "music_v2", "createdAt": now.Add(time.Second), "createdBy": bson.M{"userName": "Theo", "via": "assistant-auto"},
		"cost": bson.M{"usd": 0.3}, "inputs": bson.A{bson.M{"role": "prompt-file", "name": "mood.txt"}}, "outcome": bson.M{"state": "done"}})

	// An AI voice line and a clearer voice in the cloud.
	take := models.VoiceTake{ID: ids.New("tk"), WorkspaceID: a.WorkspaceID(), ProjectID: p.ID, Text: "Welcome to the garden.", Source: "tts", AssetID: ids.New(ids.Asset),
		Duration: 2, AIVoice: true, Provider: "elevenlabs", ModelID: "eleven_v4", Characters: 22, Credits: 22, CreatedBy: person, CreatedAt: now}
	insert(t, a, store.CVoiceTakes, take)
	insert(t, a, "sound_cache", bson.M{"_id": a.WorkspaceID() + "/wet:k1", "workspaceId": a.WorkspaceID(), "kind": "wet", "key": "k1", "mode": "cloud",
		"sourceAssetId": rec.ID, "from": 0.0, "to": 30.0, "credits": 500.0, "createdAt": now, "createdBy": person})

	// Claude: a conversation answered by the host's Claude Max account.
	insert(t, a, store.CAssistantConversations, bson.M{"_id": "cnv_receipt", "workspaceId": a.WorkspaceID(), "projectId": p.ID, "title": "Tighten the intro",
		"userName": "Theo", "installationName": "Studio", "turnCount": 3, "costUsd": 1.7, "createdAt": now, "updatedAt": now, "tokens": bson.M{"total": 120000},
		"account": bson.M{"who": "Claude Code on Studio, signed in as host@example.com (Claude Max)", "plan": "max", "termsName": "Anthropic's consumer terms"}})
	// The activity log's requests.
	for i := 0; i < 3; i++ {
		insert(t, a, store.CLogEntries, logbook.Entry{ID: ids.New("log"), WorkspaceID: a.WorkspaceID(), At: now, Kind: logbook.KindProvider, Level: "info", Channel: "dashboard",
			Summary: "ElevenLabs: make speech", Refs: logbook.Refs{ProjectID: p.ID}, Provider: &logbook.ProviderInfo{Name: "elevenlabs", Title: "ElevenLabs", What: "make speech", OK: i > 0}})
	}
	// Auto spent once.
	insert(t, a, store.CAutoSpending, models.AutoSpend{ID: ids.New("aus"), WorkspaceID: a.WorkspaceID(), ProjectID: p.ID, ForID: "usr_theo", ForName: "Theo",
		ConversationID: "cnv_receipt", ActionID: "act_1", Op: "generate.shot", What: "Make a shot of a beach", USD: 0.42, Day: now.Format("2006-01-02"), At: now,
		JobIDs: []string{"job_gen1"}})

	// The clearer copy the cut plays is made of that cloud voice; a copy made
	// on this computer of the same recording costs nothing.
	variant := ids.New(ids.Asset)
	insert(t, a, "sound_cache", bson.M{"_id": a.WorkspaceID() + "/copy:c1", "workspaceId": a.WorkspaceID(), "kind": "copy", "key": "c1", "assetId": variant,
		"sourceAssetId": rec.ID, "from": 0.0, "to": 30.0, "wets": bson.A{"k1"}, "createdAt": now, "createdBy": person})
	insert(t, a, "sound_cache", bson.M{"_id": a.WorkspaceID() + "/wet:k2", "workspaceId": a.WorkspaceID(), "kind": "wet", "key": "k2", "mode": "clean",
		"sourceAssetId": rec.ID, "from": 0.0, "to": 30.0, "createdAt": now, "createdBy": person})
	local := ids.New(ids.Asset)
	insert(t, a, "sound_cache", bson.M{"_id": a.WorkspaceID() + "/copy:c2", "workspaceId": a.WorkspaceID(), "kind": "copy", "key": "c2", "assetId": local,
		"sourceAssetId": rec.ID, "from": 0.0, "to": 30.0, "wets": bson.A{"k2"}, "createdAt": now, "createdBy": person})

	// The cut uses the generated shot, the AI voice line and the clearer voice.
	spec := editspec.Spec{SchemaVersion: 1, Title: "Garden story", Output: editspec.Output{Preset: "vertical-1080p", Width: 1080, Height: 1920, FPS: 30},
		Clips:     []editspec.Clip{{ID: "talk", AssetID: rec.ID, In: 0, Out: 6}, {ID: "beach", AssetID: shot.ID, In: 0, Out: 4}},
		Voiceover: &editspec.Voiceover{Beats: []editspec.Beat{{ID: "b1", Text: take.Text, Take: &editspec.TakeRef{TakeID: take.ID, AssetID: take.AssetID, Out: 2, Source: "tts", AIVoice: true}}}},
		Mix:       &editspec.Mix{Enhanced: []editspec.EnhancedSound{{AssetID: rec.ID, Variant: variant, Settings: "Theo: cloud 100%"}}}}
	rev := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: a.WorkspaceID(), ProjectID: p.ID, Branch: "main", Number: 1, Spec: spec, SpecHash: "h", Duration: 10, CreatedAt: now}
	insert(t, a, store.CRevisions, rev)
	if err := a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), p.ID, bson.M{"$set": bson.M{"branches.main": models.BranchHead{Head: rev.ID}}}); err != nil {
		t.Fatal(err)
	}

	out, err := ops.CallTyped[receipt.ProjectReceiptOut](ctx, a, "project.receipt", receipt.ProjectReceiptIn{ProjectID: p.ID}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	if len(out.Requests) != 4 {
		t.Fatalf("requests: %s", b)
	}
	byKind := map[string]receipt.ReceiptRequest{}
	for _, r := range out.Requests {
		byKind[r.Kind] = r
	}
	v := byKind["video"]
	if v.Service != "Google Gemini" || v.USD != 0.4 || v.Basis != "charged" || v.Deleted != receipt.DeletedThere || len(v.Sent) != 2 || v.Prompt != "A beach at dusk" ||
		!strings.Contains(v.Why, "Theo asked in the dashboard") || len(v.Outputs) != 1 {
		t.Fatalf("video request %+v", v)
	}
	if m := byKind["music"]; m.What != "music" || m.USD != 0.3 || m.Basis != "estimate" || !strings.Contains(m.Why, "Auto ran Claude's card") || len(m.Sent) != 1 {
		t.Fatalf("an unknown kind of work: %+v", m)
	}
	if vo := byKind["voice"]; vo.Service != "ElevenLabs" || vo.Deleted != receipt.KeptThere || vo.USD <= 0 || vo.Prompt != take.Text {
		t.Fatalf("voice %+v", vo)
	}
	if cl := byKind["clean-up"]; cl.USD != 0.06 || cl.Model != "Voice Isolator" {
		t.Fatalf("clean-up %+v", cl)
	}
	if len(out.Claude) != 1 || out.Claude[0].Billed || out.Claude[0].USD != 0 || !strings.Contains(out.Claude[0].Who, "host@example.com") {
		t.Fatalf("claude %+v", out.Claude)
	}
	if len(out.Logged) != 1 || out.Logged[0].Count != 3 || out.Logged[0].Failed != 1 || out.Logged[0].Service != "ElevenLabs" {
		t.Fatalf("logged %+v", out.Logged)
	}
	// What Auto spent is what its shot settled at ($0.40), not its estimate ($0.42).
	if len(out.Auto) != 1 || out.Auto[0].USD != 0.4 || out.Auto[0].Pending || out.Auto[0].For != "Theo" {
		t.Fatalf("auto %+v", out.Auto)
	}
	shots, voices := 0, 0
	for _, g := range out.Generated {
		if g.Kind == "shot" && g.InVersion && g.Service == "Google Gemini" {
			shots++
		}
		if g.Kind == "voice" {
			voices++
		}
	}
	if shots != 1 || voices != 1 {
		t.Fatalf("generated %+v", out.Generated)
	}
	// At cost: the project and the version (what its cut uses).
	c := out.Cost
	if c.ProjectUSD < 0.76 || c.ProjectUSD > 0.77 || len(c.ByService) != 2 || c.Version == nil || c.Number != 1 || len(c.Version.Parts) != 3 || c.Version.USD < 0.46 || c.Version.USD > 0.47 {
		t.Fatalf("cost %+v %+v", c, c.Version)
	}
	if !strings.Contains(c.Claude, "Claude plan") || !strings.Contains(c.Note, "nothing added") {
		t.Fatalf("cost words %+v", c)
	}
	// A version playing the copy made on this computer costs nothing for its
	// clearer voice, though the same recording was also cleaned in the cloud.
	spec2 := spec
	spec2.Mix = &editspec.Mix{Enhanced: []editspec.EnhancedSound{{AssetID: rec.ID, Variant: local, Settings: "Theo: clean 100%"}}}
	rev2 := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: a.WorkspaceID(), ProjectID: p.ID, Branch: "main", Number: 2, Spec: spec2, SpecHash: "h2", Duration: 10, CreatedAt: now}
	insert(t, a, store.CRevisions, rev2)
	out2, err := ops.CallTyped[receipt.ProjectReceiptOut](ctx, a, "project.receipt", receipt.ProjectReceiptIn{ProjectID: p.ID, Revision: rev2.ID}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range out2.Cost.Version.Parts {
		if part.What == "a clearer voice" {
			t.Fatalf("a copy made on this computer was priced: %+v", out2.Cost.Version)
		}
	}
	if len(out2.Cost.Version.Parts) != 2 || out2.Cost.ProjectUSD != c.ProjectUSD {
		t.Fatalf("version 2 %+v (project %v)", out2.Cost.Version, out2.Cost.ProjectUSD)
	}
	if len(out.Lines) < 5 || !strings.Contains(out.Summary, "4 requests") || !strings.Contains(strings.Join(out.Lines, " "), "Auto spent $0.40") {
		t.Fatalf("lines %v", out.Lines)
	}
	for _, l := range out.Lines {
		if err := plainwords.CheckShown(l); err != nil {
			t.Errorf("%q: %v", l, err)
		}
	}
}

// A delivered video's record carries the receipt in short: what the version
// costs at cost and what the project sent (D240).
func TestDeliveryRecordHasTheReceipt(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: "Garden story", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	insert(t, a, store.CProjects, p)
	shot := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.ID}, Kind: "video", Name: "beach.mp4", SHA256: strings.Repeat("b", 64),
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: 4, HasVideo: true}, Source: &models.AssetSource{Platform: "generated"}, CreatedAt: now, UpdatedAt: now}
	insert(t, a, store.CAssets, shot)
	insert(t, a, store.CGenerations, models.Generation{ID: ids.New("gen"), WorkspaceID: a.WorkspaceID(), ProjectID: p.ID, Provider: "gemini", ProviderTitle: "Google Gemini",
		Settled: &models.GenSettlement{USD: 0.4, At: now}, Outcome: &models.GenOutcome{State: models.GenDone, Outputs: []models.GenOutput{{AssetID: shot.ID}}}, CreatedAt: now})
	insert(t, a, store.CAssistantConversations, bson.M{"_id": "cnv_delivered", "workspaceId": a.WorkspaceID(), "projectId": p.ID, "title": "Make the beach shot",
		"userName": "Theo", "installationName": "Studio", "turnCount": 2, "createdAt": now, "updatedAt": now,
		"account": bson.M{"who": "Claude Code on Studio, signed in as host@example.com (Claude Max)", "plan": "max", "planName": "Claude Max", "termsName": "Anthropic's consumer terms"}})
	spec := editspec.Spec{SchemaVersion: 1, Title: "Beach", Output: editspec.Output{Preset: "vertical-1080p", Width: 1080, Height: 1920, FPS: 30, Platform: "tiktok"},
		Clips: []editspec.Clip{{ID: "beach", AssetID: shot.ID, In: 0, Out: 4}}}
	rev := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: a.WorkspaceID(), ProjectID: p.ID, Branch: "main", Number: 1, Spec: spec, SpecHash: "h", Duration: 4, CreatedAt: now}
	insert(t, a, store.CRevisions, rev)
	b := []byte(strings.Repeat("render bytes ", 200))
	path := t.TempDir() + "/r.mp4"
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	sha, _ := media.HashFile(path)
	key := media.ObjectKey(a.WorkspaceID(), sha)
	if _, err := a.Media.PutFile(ctx, key, path, media.PutOptions{SHA256: sha}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	insert(t, a, store.CRenders, models.Render{ID: ids.New(ids.Render), WorkspaceID: a.WorkspaceID(), ProjectID: p.ID, RevisionID: rev.ID, RevisionNumber: 1, SpecHash: "h",
		Preset: "vertical-1080p", Quality: "review", State: models.RenderAvailable, CompletedAt: &now, Output: &models.RenderOutput{StorageKey: key, SHA256: sha, Size: int64(len(b)), Duration: 4}})
	out, err := ops.CallTyped[ops.DeliverOut](ctx, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rev.ID}, Out: t.TempDir(), Quality: "review", AllowDraft: true}, ops.Caller{Human: true})
	if err != nil || len(out.Files) != 1 {
		t.Fatalf("deliver %+v %v", out, err)
	}
	var sc ops.DeliverySidecar
	raw, _ := os.ReadFile(out.Files[0].Sidecar)
	_ = json.Unmarshal(raw, &sc)
	if sc.Receipt == nil || sc.Receipt.CostUSD != 0.4 || sc.Receipt.Requests != 1 || sc.Receipt.Services[0] != "Google Gemini" || !strings.Contains(sc.Receipt.Command, "project receipt") {
		t.Fatalf("record %s", raw)
	}
	// The file goes to other people: the plan and the terms, never whose account (D239).
	if strings.Contains(string(raw), "host@example.com") || strings.Contains(string(raw), "signed in as") ||
		len(sc.Receipt.ClaudeAnswers) != 1 || sc.Receipt.ClaudeAnswers[0] != "a Claude Max plan, under Anthropic's consumer terms" {
		t.Fatalf("the delivered record says whose account answered: %s", raw)
	}
	// People who can see the receipt still see whose it was.
	rc, err := ops.CallTyped[receipt.ProjectReceiptOut](ctx, a, "project.receipt", receipt.ProjectReceiptIn{ProjectID: p.ID}, ops.Caller{})
	if err != nil || !strings.Contains(strings.Join(rc.Lines, " "), "host@example.com") {
		t.Fatalf("receipt lines %v %v", rc, err)
	}
}

// A clearer copy made before copies listed their processed voices is priced
// from its recording's cloud voices made before it, one per stretch: tries of
// the same stretch aren't added up, and voices made after it aren't counted
// (D240).
func TestOlderClearerCopiesArePricedOnePerStretch(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	p := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: "Old copy", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	insert(t, a, store.CProjects, p)
	person := models.Actor{UserID: "usr_theo", UserName: "Theo", Via: "dashboard"}
	rec := ids.New(ids.Asset)
	wet := func(key string, from, to, credits float64, mode string, at time.Time) {
		insert(t, a, "sound_cache", bson.M{"_id": a.WorkspaceID() + "/wet:" + key, "workspaceId": a.WorkspaceID(), "kind": "wet", "key": key, "mode": mode,
			"sourceAssetId": rec, "from": from, "to": to, "credits": credits, "createdAt": at, "createdBy": person})
	}
	wet("a", 0, 10, 100, "cloud", now.Add(-3*time.Hour))
	wet("a2", 0, 10, 120, "cloud", now.Add(-time.Hour))  // the same stretch tried again
	wet("b", 5, 15, 200, "cloud", now.Add(-2*time.Hour)) // overlaps both pieces
	wet("c", 10, 20, 50, "cloud", now.Add(-3*time.Hour))
	wet("d", 0, 20, 999, "cloud", now.Add(time.Hour)) // made after the copy
	wet("e", 0, 20, 0, "clean", now.Add(-time.Hour))  // on this computer
	variant := ids.New(ids.Asset)
	insert(t, a, "sound_cache", bson.M{"_id": a.WorkspaceID() + "/copy:old", "workspaceId": a.WorkspaceID(), "kind": "copy", "key": "old", "assetId": variant,
		"sourceAssetId": rec, "from": 0.0, "to": 20.0, "scores": bson.A{bson.M{"mode": "cloud"}}, "createdAt": now, "createdBy": person})
	spec := editspec.Spec{SchemaVersion: 1, Title: "Old copy", Output: editspec.Output{Preset: "vertical-1080p", Width: 1080, Height: 1920, FPS: 30},
		Clips: []editspec.Clip{{ID: "talk", AssetID: rec, In: 0, Out: 20}},
		Mix:   &editspec.Mix{Enhanced: []editspec.EnhancedSound{{AssetID: rec, Variant: variant, Settings: "Theo: cloud 100%"}}}}
	rev := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: a.WorkspaceID(), ProjectID: p.ID, Branch: "main", Number: 1, Spec: spec, SpecHash: "h", Duration: 20, CreatedAt: now}
	insert(t, a, store.CRevisions, rev)
	out, err := ops.CallTyped[receipt.ProjectReceiptOut](ctx, a, "project.receipt", receipt.ProjectReceiptIn{ProjectID: p.ID, Revision: rev.ID}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	credits, n := 0.0, 0
	for _, part := range out.Cost.Version.Parts {
		if part.What == "a clearer voice" {
			credits += part.Amount
			n++
		}
	}
	// The newest try of 0–10 s (120) and 10–20 s (50).
	if n != 2 || credits != 170 {
		t.Fatalf("%d clearer voices, %v credits: %+v", n, credits, out.Cost.Version.Parts)
	}
}

// Every kind of AI work (D226) is on the receipt with what it cost and whether
// it was deleted at the service — a voice copy stays while it is in use, what
// a service's API can't delete is said — its pictures and sound are listed as
// generated media, and a version's cost (on its page and in a delivered
// record) includes the AI pictures, sound and cloud transcripts it uses.
func TestReceiptShowsEveryAIKind(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	ws := a.WorkspaceID()
	p := models.Project{ID: ids.New(ids.Project), WorkspaceID: ws, Name: "Garden story", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	insert(t, a, store.CProjects, p)
	person := models.Actor{UserID: "usr_theo", UserName: "Theo", Via: "dashboard"}
	rec := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: ws, ProjectIDs: []string{p.ID}, Kind: "video", Name: "talk.mov", SHA256: strings.Repeat("a", 64),
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: 30, HasVideo: true, HasAudio: true}, CreatedAt: now, UpdatedAt: now}
	insert(t, a, store.CAssets, rec)
	clipOf := func(kind, title string, n int) models.Asset {
		as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: ws, ProjectIDs: []string{p.ID}, Kind: "audio", Name: title + ".mp3", Title: title, SHA256: strings.Repeat(string(rune('b'+n)), 64),
			State: models.AssetAvailable, Media: models.MediaInfo{Duration: 5, HasAudio: true}, CreatedAt: now, UpdatedAt: now,
			Source: &models.AssetSource{Platform: models.PlatformGenerated, Method: models.SourceGenerated, Generated: &models.GeneratedSource{Kind: kind, Provider: "elevenlabs", ProviderTitle: "ElevenLabs"}}}
		insert(t, a, store.CAssets, as)
		return as
	}
	pic := models.Resource{ID: ids.New("res"), WorkspaceID: ws, Name: "kite", Title: "A watercolour kite", Kind: models.ResourceImage, Scope: "project", ProjectID: p.ID,
		Current: strings.Repeat("e", 64), CreatedAt: now, UpdatedAt: now,
		Versions: []models.ResourceVersion{{SHA256: strings.Repeat("e", 64), MIME: "image/png", Ext: "png", CreatedAt: now,
			Source: models.ResourceSource{MadeWith: &models.ResourceMadeWith{Kind: "ai", Provider: "gemini", ProviderTitle: "Google Gemini"}}}}}
	insert(t, a, store.CResources, pic)
	gens := 0
	work := func(kind, provider, title string, usd float64, outputs []models.GenOutput, forgotten *models.GenForgotten) {
		t.Helper()
		gens++
		insert(t, a, store.CGenerations, models.Generation{ID: ids.New("gen"), WorkspaceID: ws, ProjectID: p.ID, Kind: kind, Operation: "make", Provider: provider, ProviderTitle: title,
			Model: kind + "-model", Prompt: "for " + kind, Estimate: models.GenCost{USD: usd + 0.01, Basis: "estimate"}, Settled: &models.GenSettlement{USD: usd, At: now},
			Outcome: &models.GenOutcome{State: models.GenDone, At: now, Outputs: outputs}, Forgotten: forgotten, CreatedBy: person, Channel: "dashboard",
			CreatedAt: now.Add(time.Duration(gens) * time.Second)})
	}
	deleted := &models.GenForgotten{At: now, OK: true}
	cannot := &models.GenForgotten{At: now, Kept: true, Detail: "ElevenLabs offers no way to delete what it made."}
	music := clipOf("music", "Calm guitar", 1)
	work("picture", "gemini", "Google Gemini", 0.04, []models.GenOutput{{ResourceID: pic.ID}}, deleted)
	work("voice", "elevenlabs", "ElevenLabs", 0.02, []models.GenOutput{{AssetID: clipOf("voice", "Welcome", 2).ID}}, deleted)
	work("voice-copy", "gemini", "Google Gemini", 0, []models.GenOutput{{VoiceID: "voice_abc123"}}, deleted)
	work("voice-change", "elevenlabs", "ElevenLabs", 0.03, []models.GenOutput{{AssetID: clipOf("voice-change", "In another voice", 3).ID}}, deleted)
	work("sound-effect", "elevenlabs", "ElevenLabs", 0.05, []models.GenOutput{{AssetID: clipOf("sound-effect", "A door", 4).ID}}, cannot)
	work("music", "elevenlabs", "ElevenLabs", 0.2, []models.GenOutput{{AssetID: music.ID}}, cannot)
	work("dubbing", "elevenlabs", "ElevenLabs", 0.6, []models.GenOutput{{AssetID: clipOf("dubbing", "Talk in French", 5).ID}}, deleted)
	work("transcription", "gemini", "Google Gemini", 0.01, []models.GenOutput{{AssetID: rec.ID, Words: 80}}, deleted)
	work("presenter-make", "d-id", "D-ID", 0, []models.GenOutput{{PresenterID: "avt_abc"}}, deleted)
	work("presenter", "d-id", "D-ID", 0.28, []models.GenOutput{{AssetID: clipOf("presenter", "Ada reads line 1", 6).ID}}, deleted)

	// The version uses the recording (written down in the cloud), the music and the picture.
	spec := editspec.Spec{SchemaVersion: 1, Title: "Garden story", Output: editspec.Output{Preset: "vertical-1080p", Width: 1080, Height: 1920, FPS: 30},
		Clips:     []editspec.Clip{{ID: "talk", AssetID: rec.ID, In: 0, Out: 6}},
		Resources: map[string]editspec.ResourcePin{"kite": {ID: pic.ID, SHA256: pic.Current}},
		Audio:     []editspec.AudioTrack{{ID: "music", AssetID: music.ID, Role: "music", In: 0, Out: 5, At: 0}}}
	rev := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: ws, ProjectID: p.ID, Branch: "main", Number: 1, Spec: spec, SpecHash: "h", Duration: 6, CreatedAt: now}
	insert(t, a, store.CRevisions, rev)
	if err := a.DB.Update(ctx, store.CProjects, ws, p.ID, bson.M{"$set": bson.M{"branches.main": models.BranchHead{Head: rev.ID}}}); err != nil {
		t.Fatal(err)
	}
	out, err := ops.CallTyped[receipt.ProjectReceiptOut](ctx, a, "project.receipt", receipt.ProjectReceiptIn{ProjectID: p.ID}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	byKind := map[string]receipt.ReceiptRequest{}
	for _, r := range out.Requests {
		byKind[r.Kind] = r
	}
	want := map[string]string{"picture": "a picture", "voice": "AI speech", "voice-copy": "a voice copy", "voice-change": "a recording in another voice",
		"sound-effect": "a sound effect", "music": "music", "dubbing": "a dubbed copy", "transcription": "a transcript in the cloud",
		"presenter": "a presenter clip", "presenter-make": "a person's presenter"}
	for kind, what := range want {
		r, ok := byKind[kind]
		if !ok || r.What != what || r.Basis != "charged" || r.Service == "" || r.Model == "" {
			t.Errorf("%s: %+v", kind, r)
		}
	}
	if r := byKind["dubbing"]; r.USD != 0.6 || r.Deleted != receipt.DeletedThere {
		t.Errorf("dub %+v", r)
	}
	for _, kind := range []string{"music", "sound-effect"} {
		if r := byKind[kind]; r.Deleted != receipt.KeptThere || !strings.Contains(r.DeletedSays, "no way to delete") {
			t.Errorf("%s kept by the service: %+v", kind, r)
		}
	}
	if r := byKind["voice-copy"]; r.Deleted != receipt.KeptThere || !strings.Contains(r.DeletedSays, "while it is in use") {
		t.Errorf("voice copy in use: %+v", r)
	}
	if r := byKind["presenter-make"]; r.Deleted != receipt.KeptThere || !strings.Contains(r.DeletedSays, "retiring it deletes it there") {
		t.Errorf("presenter in use: %+v", r)
	}
	// Generated media: the picture (used), and the sound each kind made.
	kinds := map[string]int{}
	for _, g := range out.Generated {
		kinds[g.Kind]++
		if g.Kind == "picture" && (!g.InVersion || g.ResourceID != pic.ID || g.Service != "Google Gemini") {
			t.Errorf("picture %+v", g)
		}
		if g.Kind == "sound" && g.What == "" {
			t.Errorf("sound without words: %+v", g)
		}
		if g.AssetID == music.ID && (!g.InVersion || g.What != "music") {
			t.Errorf("music %+v", g)
		}
	}
	if kinds["picture"] != 1 || kinds["sound"] != 5 || kinds["shot"] != 0 || kinds["presenter"] != 1 {
		t.Fatalf("generated %v: %s", kinds, b)
	}
	// The version costs its picture, its music and the cloud transcript of its recording.
	v := out.Cost.Version
	if v == nil || len(v.Parts) != 3 || v.USD < 0.249 || v.USD > 0.251 {
		t.Fatalf("version cost %+v", v)
	}
	if out.Cost.ProjectUSD < 1.229 || out.Cost.ProjectUSD > 1.231 { // with the presenter clip's $0.28
		t.Fatalf("project %v", out.Cost.ProjectUSD)
	}
	lines := strings.Join(out.Lines, " ")
	if !strings.Contains(lines, "1 AI picture") || !strings.Contains(lines, "5 AI sound clips") || !strings.Contains(lines, "1 AI presenter clip") || !strings.Contains(lines, "10 requests") {
		t.Fatalf("lines %v", out.Lines)
	}
	for _, l := range out.Lines {
		if err := plainwords.CheckShown(l); err != nil {
			t.Errorf("%q: %v", l, err)
		}
	}
	// A delivered video's record carries the same version cost.
	d := ops.DeliveryReceiptFor(ctx, a, &p, &rev)
	if d == nil || d.CostUSD != v.USD || d.Requests != 10 || d.Generated != 7 {
		t.Fatalf("delivery receipt %+v", d)
	}
}

// What left this computer (D291) counts Vertex AI and sync. like any AI
// service — lip sync's part of a clip as footage with faces and voices —
// and never work done here: a model on this computer, or an ability (the
// hosts its run reached are the abilities' places).
func TestWhatLeftCountsEveryService(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	p := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: "Garden story", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	insert(t, a, store.CProjects, p)
	tk := &models.GenTicket{ID: "t1", SubmittedAt: now}
	for _, g := range []models.Generation{
		{Provider: "sync", ProviderTitle: "sync.", Kind: "lip-sync", Model: "lipsync-2", References: []models.GenReference{{Role: "media", MIME: "video/mp4", AssetID: ids.New(ids.Asset)},
			{Role: "audio", MIME: "audio/flac", AssetID: ids.New(ids.Asset)}}},
		{Provider: "vertex", ProviderTitle: "Google Cloud Vertex AI", Model: "veo-3.1-fast-generate-001", Prompt: "A beach at dusk"},
		{Provider: "local", ProviderTitle: "This computer", Kind: "transcription", Model: "parakeet-v2", References: []models.GenReference{{Role: "audio", AssetID: ids.New(ids.Asset)}}},
		{Provider: "ability-grow", ProviderTitle: "Grow pictures", Kind: "upscale", Model: "grow", References: []models.GenReference{{Role: "picture", MIME: "image/png"}}},
	} {
		g.ID, g.WorkspaceID, g.ProjectID, g.Operation, g.Ticket, g.CreatedAt = ids.New("gen"), a.WorkspaceID(), p.ID, models.GenMake, tk, now
		insert(t, a, store.CGenerations, g)
	}
	l, err := ops.CallTyped[receipt.ProjectLeavesOut](ctx, a, "project.leaves", receipt.ProjectLeavesIn{ProjectID: p.ID}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, x := range l.Left.Places {
		got[x.To] = strings.Join(x.Kinds, ",")
	}
	if len(got) != 2 || got["sync."] != "footage,voices,faces" && got["sync."] != "footage,faces,voices" || got["Google Cloud Vertex AI"] != "words" {
		t.Fatalf("places %+v", l.Left.Places)
	}
}

// A version a person sent out past the ship gate (D299) is on the receipt:
// who, when, their note and what held it — in the delivery record's lines too.
func TestReceiptSaysWhatWentOutAnyway(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	p := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: "Garden story", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	insert(t, a, store.CProjects, p)
	o := models.ShipOverride{ID: ids.New(ids.ShipOverride), By: models.Actor{UserID: "usr_theo", UserName: "Theo", Via: "dashboard"}, At: now,
		Note: "The client needs it today.", Holds: []models.ShipHold{{Key: "qa-errors:qar_x", Kind: models.HoldQAErrors, Says: "The quality check found 1 problem in its final render."},
			{Key: "signoff:sgn_x@main", Kind: models.HoldSignoff, Says: "Still waiting for the sign-off “Legal OKs it”."}}}
	rev := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: a.WorkspaceID(), ProjectID: p.ID, Branch: "main", Number: 3, SpecHash: "h", CreatedAt: now, ShipOverrides: []models.ShipOverride{o}}
	insert(t, a, store.CRevisions, rev)
	other := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: a.WorkspaceID(), ProjectID: p.ID, Branch: "main", Number: 4, SpecHash: "i", CreatedAt: now}
	insert(t, a, store.CRevisions, other)

	rc, err := receipt.Receipt(ctx, a, &p, &rev)
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.SentAnyway) != 1 || rc.SentAnyway[0].Number != 3 || rc.SentAnyway[0].By != "Theo" || len(rc.SentAnyway[0].Held) != 2 || rc.SentAnyway[0].Note != o.Note {
		t.Fatalf("sent anyway: %+v", rc.SentAnyway)
	}
	line := ""
	for _, l := range rc.Lines {
		if strings.Contains(l, "although") {
			line = l
		}
	}
	if !strings.Contains(line, "Version 3 was allowed to go out although 2 things held it: Theo let it go anyway on") || !strings.Contains(line, "hasn't gone out yet") ||
		!strings.Contains(line, "The client needs it today.") || rc.SentAnyway[0].WentOut {
		t.Fatalf("allowed, not delivered yet: line %q in %v", line, rc.Lines)
	}
	// Once it is delivered with that note, it went out.
	if _, err := a.DB.C(store.CRevisions).UpdateOne(ctx, bson.M{"_id": rev.ID}, bson.M{"$push": bson.M{"sentOut": models.ShipSent{At: now, By: o.By, Overrides: []string{o.ID}, What: "delivery", Folder: "Acme"}}}); err != nil {
		t.Fatal(err)
	}
	rc, _ = receipt.Receipt(ctx, a, &p, &rev)
	line = ""
	for _, l := range rc.Lines {
		if strings.Contains(l, "although") {
			line = l
		}
	}
	if !strings.Contains(line, "Version 3 went out although 2 things held it: Theo sent it out anyway on") || !rc.SentAnyway[0].WentOut {
		t.Fatalf("went out: %q", line)
	}
	if fs := plainwords.CheckShown(line); fs != nil {
		t.Errorf("%q: %v", line, fs)
	}
	if d := ops.DeliveryReceiptFor(ctx, a, &p, &rev); d == nil || !strings.Contains(strings.Join(d.Lines, " "), "although") {
		t.Fatalf("the delivery record's receipt: %+v", d)
	}
}
