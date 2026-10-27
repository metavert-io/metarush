package ops_test

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/transcripts"
)

// Content Settings (D192, D193): whose settings a project uses — the
// default's, another project's (followed through chains) or its own — with
// circles refused, followers counted through chains, the default moving, a
// gone project's followers back on the default (and told), and edits through
// a follower refused.
func TestContentSettingsResolution(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	mk := func(name string) models.Project {
		return do[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: name}).Project
	}
	fernbrook, b, c, d := mk("fernbrook"), mk("Bee"), mk("Cee"), mk("Dee")
	show := func(p string) *ops.ContentShowOut {
		return do[ops.ContentShowOut](t, a, "content.show", ops.ContentShowIn{ProjectID: p})
	}

	// The first project holds the settings and is the default; the others use them.
	if s := show(fernbrook.ID); s.Uses != models.ContentOwn || !s.IsDefault || s.ReadOnly || len(s.UsedBy) != 3 || !strings.Contains(s.Summary, "Also used by 3 projects") {
		t.Fatalf("fernbrook: %+v", s)
	}
	if s := show(b.ID); s.Uses != models.ContentDefault || s.From == nil || s.From.ID != fernbrook.ID || !s.ReadOnly || !strings.Contains(s.Summary, "uses the default settings") {
		t.Fatalf("Bee uses the default: %+v", s)
	}
	if s := do[ops.ContentShowOut](t, a, "content.show", ops.ContentShowIn{}); s.Project != nil || s.From == nil || s.From.ID != fernbrook.ID || len(s.Parts) != 10 {
		t.Fatalf("the default's: %+v", s)
	}
	names := []string{}
	for _, p := range content.Parts() {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "brand,spelling,voice,rules,style,signoffs,pack,gate,leave,aiServices" { // the layout pack (D263), before it goes out (D298), What may leave (D288), AI services (D225)
		t.Fatalf("parts %v", names)
	}

	// Cee gets its own (empty), Bee follows Cee, Dee follows Bee: Dee gets Cee's through the chain.
	own := do[ops.ContentShowOut](t, a, "content.own", ops.ContentOwnIn{ProjectID: c.ID})
	if own.Uses != models.ContentOwn || own.ReadOnly || !strings.Contains(own.Changed, "start empty") {
		t.Fatalf("Cee's own: %+v", own)
	}
	do[ops.ContentShowOut](t, a, "content.follow", ops.ContentFollowIn{ProjectID: b.ID, Follow: c.ID})
	dee := do[ops.ContentShowOut](t, a, "content.follow", ops.ContentFollowIn{ProjectID: d.ID, Follow: "Bee"})
	if dee.From == nil || dee.From.ID != c.ID || dee.Follows == nil || dee.Follows.ID != b.ID || len(dee.Through) != 1 || dee.Through[0].ID != b.ID ||
		!strings.Contains(dee.Changed, "come from “Cee”") {
		t.Fatalf("a chain: %+v", dee)
	}
	if s := show(c.ID); len(s.UsedBy) != 2 || !strings.Contains(s.Summary, "Also used by 2 projects") {
		t.Fatalf("followers counted through the chain: %+v", s)
	}
	if s := show(fernbrook.ID); len(s.UsedBy) != 0 {
		t.Fatalf("fernbrook is used by no one now: %+v", s)
	}
	// A circle is refused: Cee can't follow Dee (whose settings come from Cee).
	if err := doErr(a, "content.follow", ops.ContentFollowIn{ProjectID: c.ID, Follow: d.ID}, person); apperr.From(err).Code != apperr.Conflict || !strings.Contains(apperr.From(err).Message, "circle") {
		t.Fatalf("a circle: %v", err)
	}
	for _, ch := range show(c.ID).Choices {
		if ch.ID == b.ID || ch.ID == d.ID {
			t.Fatalf("Cee is offered a project that would go round in a circle: %+v", ch)
		}
	}
	if err := doErr(a, "content.follow", ops.ContentFollowIn{ProjectID: c.ID, Follow: c.ID}, person); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("following itself: %v", err)
	}

	// Edits through a follower are refused plainly; through its owner they reach every follower.
	err := doErr(a, "content.rules.add", ops.HouseRuleAddIn{ProjectID: d.ID, Text: "The name bar never animates"}, person)
	if ae := apperr.From(err); ae.Code != apperr.Conflict || !strings.Contains(ae.Message, "“Dee” uses “Cee”'s settings") || !strings.Contains(ae.Hint, "give “Dee” its own") {
		t.Fatalf("a follower's edit: %v", err)
	}
	hr := do[ops.HouseRulesOut](t, a, "content.rules.add", ops.HouseRuleAddIn{ProjectID: c.ID, Text: "The name bar never animates"})
	if !strings.Contains(hr.Summary, "Also used by 2 projects") {
		t.Fatalf("also used by: %+v", hr)
	}
	if got := do[ops.HouseRulesOut](t, a, "content.rules.list", ops.HouseRulesIn{ProjectID: d.ID}); len(got.Rules) != 1 || !got.ReadOnly || got.ProjectID != c.ID {
		t.Fatalf("Dee reads Cee's house rules: %+v", got)
	}
	// Without a project: the default's, and the answer says so.
	def := do[ops.HouseRulesOut](t, a, "content.rules.add", ops.HouseRuleAddIn{Text: "Captions at the top"})
	if def.ProjectID != fernbrook.ID || !strings.Contains(def.Summary, "These are “fernbrook”'s settings, which new projects use.") {
		t.Fatalf("the default's: %+v", def)
	}

	// The default moves: Bee's settings for new projects (it keeps following Cee).
	moved := do[ops.ContentShowOut](t, a, "content.setDefault", ops.ContentDefaultIn{ProjectID: b.ID})
	if !moved.IsDefault || moved.From.ID != c.ID || !strings.Contains(moved.Changed, "come from “Cee”") {
		t.Fatalf("the default moved: %+v", moved)
	}
	if s := show(fernbrook.ID); s.IsDefault {
		t.Fatal("one default only")
	}
	newer := mk("New")
	if s := show(newer.ID); s.From == nil || s.From.ID != c.ID {
		t.Fatalf("new projects use the default (Bee's, Cee's): %+v", s)
	}
	// A project that used the default keeps its settings when ticked: it follows the old default.
	do[ops.ContentShowOut](t, a, "content.follow", ops.ContentFollowIn{ProjectID: fernbrook.ID, UseDefault: true})
	if err := doErr(a, "content.follow", ops.ContentFollowIn{ProjectID: b.ID, UseDefault: true}, person); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("the default can't use the default: %v", err)
	}
	ticked := do[ops.ContentShowOut](t, a, "content.setDefault", ops.ContentDefaultIn{ProjectID: newer.ID})
	if ticked.Uses != models.ContentProject || ticked.Follows == nil || ticked.Follows.ID != b.ID || ticked.From.ID != c.ID {
		t.Fatalf("a project on the default, ticked: %+v", ticked)
	}

	// fernbrook's own settings were kept aside; "its own" brings them back (with its house rule).
	back := do[ops.ContentShowOut](t, a, "content.own", ops.ContentOwnIn{ProjectID: fernbrook.ID})
	if back.ReadOnly || !strings.Contains(back.Changed, "the ones it had before") || len(back.Settings.HouseRules) != 1 {
		t.Fatalf("its own again: %+v", back)
	}
	// A copy of what a project uses, for a project without settings of its own.
	cp := do[ops.ContentShowOut](t, a, "content.own", ops.ContentOwnIn{ProjectID: d.ID, Copy: true})
	if cp.ReadOnly || len(cp.Settings.HouseRules) != 1 || cp.Settings.HouseRules[0].Text != "The name bar never animates" || !strings.Contains(cp.Changed, "copy of “Cee”'s") {
		t.Fatalf("a copy: %+v", cp)
	}
	if err := doErr(a, "content.own", ops.ContentOwnIn{ProjectID: fernbrook.ID, Copy: true}, person); err != nil {
		t.Fatalf("own when it has its own already: %v", err)
	}

	// A followed project that is gone: its followers use the default again, and the people involved are told.
	do[ops.ContentShowOut](t, a, "content.follow", ops.ContentFollowIn{ProjectID: d.ID, Follow: fernbrook.ID})
	if _, err := a.DB.C(store.CProjects).DeleteOne(ctx, bson.M{"_id": fernbrook.ID}); err != nil {
		t.Fatal(err)
	}
	after := show(d.ID)
	if after.Uses != models.ContentDefault || after.From == nil || after.From.ID != c.ID {
		t.Fatalf("back on the default: %+v", after)
	}
	var n models.Notification
	if err := a.DB.C(store.CNotifications).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "kind": models.NotifyContentSettings, "projectId": d.ID}).Decode(&n); err != nil ||
		!strings.Contains(n.Title, "“Dee” uses the default settings again") || !strings.Contains(n.Link, "tab=settings") {
		t.Fatalf("the people involved are told: %+v %v", n, err)
	}
	// The default gone: no default (nothing set up) for projects on the default.
	if _, err := a.DB.C(store.CProjects).DeleteOne(ctx, bson.M{"_id": newer.ID}); err != nil {
		t.Fatal(err)
	}
	if s := do[ops.ContentShowOut](t, a, "content.show", ops.ContentShowIn{}); s.From != nil {
		t.Fatalf("the default is gone: %+v", s)
	}
}

// Spellings follow Content Settings: a clip in several projects uses all of
// their lists, footage in no project the default's, and a project following
// another uses that one's.
func TestSpellingFollowsContentSettings(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	fernbrook := do[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "fernbrook"}).Project
	client := do[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Client"}).Project
	plain := do[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Plain"}).Project
	do[ops.SpellingAddOut](t, a, "transcript.spelling.add", ops.SpellingAddIn{Word: "FernBrook"}) // the default's
	if err := doErr(a, "transcript.spelling.add", ops.SpellingAddIn{ProjectID: client.ID, Word: "Kestrelby"}, person); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("a follower's list: %v", err)
	}
	do[ops.ContentShowOut](t, a, "content.own", ops.ContentOwnIn{ProjectID: client.ID})
	do[ops.SpellingAddOut](t, a, "transcript.spelling.add", ops.SpellingAddIn{ProjectID: client.ID, Word: "Kestrelby"})
	words := func(ps ...string) string {
		es, err := transcripts.ListsFor(ctx, a, ps)
		if err != nil {
			t.Fatal(err)
		}
		var w []string
		for _, e := range es {
			w = append(w, e.Word)
		}
		sort.Strings(w)
		return strings.Join(w, ",")
	}
	for name, c := range map[string]struct {
		ps   []string
		want string
	}{
		"the default's project": {[]string{fernbrook.ID}, "FernBrook"},
		"a follower":            {[]string{plain.ID}, "FernBrook"},
		"its own":               {[]string{client.ID}, "Kestrelby"},
		"in several projects":   {[]string{client.ID, plain.ID}, "FernBrook,Kestrelby"},
		"in no project (Inbox)": {nil, "FernBrook"},
	} {
		if got := words(c.ps...); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
	if hw := transcripts.HintWords(ctx, a, []string{client.ID, plain.ID}, 10); len(hw) != 2 {
		t.Fatalf("hints: %v", hw)
	}
	// Following another project's: its list, live.
	do[ops.ContentShowOut](t, a, "content.follow", ops.ContentFollowIn{ProjectID: plain.ID, Follow: client.ID})
	if got := words(plain.ID); got != "Kestrelby" {
		t.Fatalf("a follower of Client: %q", got)
	}
	sl := do[ops.SpellingListOut](t, a, "transcript.spelling.list", ops.SpellingListIn{ProjectID: plain.ID})
	if !sl.ReadOnly || sl.From == nil || sl.From.ID != client.ID || len(sl.Entries) != 1 {
		t.Fatalf("Plain's list: %+v", sl)
	}
	// "Fix existing transcripts too": every clip using the list — the
	// projects using these settings, and footage in no project for the default's.
	txClip(t, a, "Client talk", client.ID, []models.Word{wd("Kestrel", 0, 0.4, 0.9), wd("by", 0.45, 0.8, 0.9)})
	inbox := txClip(t, a, "Inbox clip", "", []models.Word{wd("Fern", 0, 0.2, 0.9), wd("Brook", 0.25, 0.6, 0.9)})
	if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), inbox.ID, bson.M{"$set": bson.M{"projectIds": []string{}}}); err != nil {
		t.Fatal(err)
	}
	if r := do[ops.SpellingApplyOut](t, a, "transcript.spelling.apply", ops.SpellingApplyIn{SettingsOf: plain.ID, DryRun: true}); r.Total != 1 || r.Hits[0].Clip != "Client talk" {
		t.Fatalf("Client's list: %+v", r)
	}
	if r := do[ops.SpellingApplyOut](t, a, "transcript.spelling.apply", ops.SpellingApplyIn{SettingsOf: fernbrook.ID, DryRun: true}); r.Total != 1 || r.Hits[0].Clip != "Inbox clip" {
		t.Fatalf("the default's list corrects footage in no project: %+v", r)
	}
}

// Narration voice choices are Content Settings; budgets stay the project's.
func TestVoiceFollowsContentSettingsBudgetsStay(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	fernbrook := do[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "fernbrook"}).Project
	other := do[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Other"}).Project
	speed := 1.1
	if err := content.Save(ctx, a, fernbrook.ID, bson.M{"voice": map[string]models.VoiceChoices{"elevenlabs": {VoiceID: "voice_ada", VoiceName: "Ada", Voice: &editspec.TTSVoiceSettings{Speed: &speed}}}}, nil); err != nil {
		t.Fatal(err)
	}
	budget := 300.0
	if err := a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), other.ID, bson.M{"$set": bson.M{"providers.elevenlabs": models.ProviderSettings{BudgetCredits: &budget, UsedCredits: 12,
		VoiceID: "voice_stale"}}}); err != nil {
		t.Fatal(err)
	}
	r, err := providers.ResolveFor(ctx, a, other.ID, nil)
	if err != nil || r.VoiceID != "voice_ada" || r.From["voice"] != "settings" || r.Settings.Speed == nil || *r.Settings.Speed != 1.1 {
		t.Fatalf("Other uses fernbrook's voice (a voice left on the project is not read): %+v %v", r, err)
	}
	line := &editspec.VoiceRef{Provider: providers.ElevenLabsName, VoiceID: "voice_cleo"}
	if r, _ := providers.ResolveFor(ctx, a, other.ID, line); r.VoiceID != "voice_cleo" || r.From["voice"] != "beat" {
		t.Fatalf("a line's own voice wins: %+v", r)
	}
	if b, _ := providers.ProjectBudget(ctx, a, other.ID); b.LimitCredits == nil || *b.LimitCredits != 300 || b.UsedCredits != 12 {
		t.Fatalf("Other's budget: %+v", b)
	}
	if b, _ := providers.ProjectBudget(ctx, a, fernbrook.ID); b.LimitCredits != nil {
		t.Fatalf("budgets are never followed: %+v", b)
	}
}

// House rules reach the quality check and Claude's context, labelled, next to
// the project's own rules.
func TestHouseRulesWithTheProjectsRules(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	fernbrook := do[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "fernbrook"}).Project
	other := do[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Other"}).Project
	do[ops.HouseRulesOut](t, a, "content.rules.add", ops.HouseRuleAddIn{Text: "First names only"})
	do[ops.RuleAddOut](t, a, "project.rules.add", ops.RuleAddIn{ProjectID: other.ID, Text: "Captions at the top"})
	p, _ := ops.ResolveProject(ctx, a, other.ID)
	rules := ops.RulesFor(ctx, a, p)
	if len(rules) != 2 || rules[0].House || !rules[1].House || rules[1].HouseOf != "fernbrook" || rules[1].Measure != ops.MeasureFirstNames {
		t.Fatalf("rules: %+v", rules)
	}
	if !content.HasMeasure(rules, ops.MeasureFirstNames) {
		t.Fatal("a house rule's measure counts")
	}
	shown := do[ops.ProjectShowOut](t, a, "project.show", ops.ProjectShowIn{ID: other.ID})
	if len(shown.HouseRules) != 1 || shown.HouseRules[0].Text != "First names only" {
		t.Fatalf("project.show house rules: %+v", shown.HouseRules)
	}
	pe, _ := ops.ResolveProject(ctx, a, fernbrook.ID)
	if r := ops.RulesFor(ctx, a, pe); len(r) != 1 || !r[0].House {
		t.Fatalf("fernbrook's: %+v", r)
	}
	b, _ := json.Marshal(models.ProjectRule{Text: "x", House: true, HouseOf: "fernbrook"})
	var stored bson.M
	raw, _ := bson.Marshal(models.ProjectRule{Text: "x", House: true, HouseOf: "fernbrook"})
	_ = bson.Unmarshal(raw, &stored)
	if _, ok := stored["house"]; ok || !strings.Contains(string(b), `"house":true`) {
		t.Fatalf("house marks are shown, never stored: %s %v", b, stored)
	}
}

// legacyProject is a project as it was before Content Settings (v35).
func legacyProject(t *testing.T, a interface{ WorkspaceID() string }, db *store.DB, name string, at time.Time, archived bool, extra bson.M) string {
	t.Helper()
	id := ids.New(ids.Project)
	doc := bson.M{"_id": id, "workspaceId": a.WorkspaceID(), "name": name, "brief": bson.M{}, "branches": bson.M{}, "revisionSeq": 0, "assetCount": 0, "archived": archived,
		"createdAt": at.Add(-48 * time.Hour), "createdBy": bson.M{"userId": "usr_x", "installationId": "ins_x", "via": "cli"}, "updatedAt": at}
	for k, v := range extra {
		doc[k] = v
	}
	if _, err := db.C(store.CProjects).InsertOne(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	return id
}

func legacySpelling(t *testing.T, a interface{ WorkspaceID() string }, db *store.DB, projectID, word string) {
	t.Helper()
	doc := bson.M{"_id": ids.New(ids.Spelling), "workspaceId": a.WorkspaceID(), "word": word, "key": transcripts.SpellingKey(word),
		"addedBy": bson.M{"userId": "usr_x", "installationId": "ins_x", "via": "cli"}, "addedAt": time.Now().UTC(), "updatedAt": time.Now().UTC()}
	if projectID != "" {
		doc["projectId"] = projectID
	}
	if _, err := db.C(store.CSpelling).InsertOne(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
}

// Migration 36 moves today's data into Content Settings so nothing anyone
// sees changes: every project resolves to the brand, spellings and voice it
// used before, the most recently active project that used the workspace's
// becomes the default, budgets and rules stay, and running it again changes nothing.
func TestMigration36KeepsWhatEveryProjectUses(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	wsKit := bson.M{"logo": "res_wslogo", "primary": "#111111", "accent": "#E0A458", "font": "Inter", "updatedAt": now}
	if _, err := a.DB.C(store.CWorkspaces).InsertOne(ctx, bson.M{"_id": a.WorkspaceID(), "workspaceId": a.WorkspaceID(), "name": "T", "createdAt": now, "brandKit": wsKit,
		"providers": bson.M{"elevenlabs": bson.M{"voiceId": "voice_ada", "voiceName": "Ada", "modelId": "eleven_multilingual_v2", "voice": bson.M{"stability": 0.4},
			"region": "eu", "favorites": bson.A{bson.M{"voiceId": "voice_bram", "name": "Bram"}}}, "gemini": bson.M{"dailyCapUsd": 5.0}}}); err != nil {
		t.Fatal(err)
	}
	// fernbrook: the most recently active, uses the workspace's everything, has a rule and a budget.
	fernbrook := legacyProject(t, a, a.DB, "fernbrook", now, false, bson.M{"rules": bson.A{bson.M{"id": "rul_1", "text": "Captions at the top", "measure": "captions-top", "addedAt": now,
		"addedBy": bson.M{"userId": "usr_x", "installationId": "ins_x", "via": "cli"}}}, "providers": bson.M{"elevenlabs": bson.M{"budgetCredits": 900.0, "usedCredits": 40.0}}})
	// Client: older, its own kit, its own spellings and its own voice.
	client := legacyProject(t, a, a.DB, "Client", now.Add(-time.Hour), false, bson.M{"brandKit": bson.M{"primary": "#E2552D", "accent": "#2D6AE2", "font": "Fraunces"},
		"providers": bson.M{"elevenlabs": bson.M{"voiceId": "voice_cleo", "voiceName": "Cleo", "voice": bson.M{"speed": 1.1}, "budgetCredits": 100.0}}})
	// Speller: only its own spellings. Plain: nothing. Old: archived but newer than all.
	speller := legacyProject(t, a, a.DB, "Speller", now.Add(-2*time.Hour), false, nil)
	plain := legacyProject(t, a, a.DB, "Plain", now.Add(-3*time.Hour), false, nil)
	old := legacyProject(t, a, a.DB, "Old", now.Add(time.Hour), true, nil)
	legacySpelling(t, a, a.DB, "", "FernBrook")
	legacySpelling(t, a, a.DB, "", "Elmwood")
	legacySpelling(t, a, a.DB, client, "Kestrelby")
	legacySpelling(t, a, a.DB, speller, "Siobhan")
	legacySpelling(t, a, a.DB, speller, "Elmwood") // the same word on both lists

	type uses struct{ primary, font, spellings, voice, model, region, favs string }
	want := map[string]uses{
		fernbrook: {"#111111", "Inter", "Elmwood,FernBrook", "voice_ada", "eleven_multilingual_v2", "eu", "Bram"},
		client:    {"#E2552D", "Fraunces", "Elmwood,FernBrook,Kestrelby", "voice_cleo", "eleven_multilingual_v2", "eu", "Bram"},
		speller:   {"#111111", "Inter", "Elmwood,FernBrook,Siobhan", "voice_ada", "eleven_multilingual_v2", "eu", "Bram"},
		plain:     {"#111111", "Inter", "Elmwood,FernBrook", "voice_ada", "eleven_multilingual_v2", "eu", "Bram"},
		old:       {"#111111", "Inter", "Elmwood,FernBrook", "voice_ada", "eleven_multilingual_v2", "eu", "Bram"},
		"":        {"#111111", "Inter", "Elmwood,FernBrook", "voice_ada", "eleven_multilingual_v2", "eu", "Bram"},
	}
	check := func(round string) {
		for pid, w := range want {
			k, err := ops.KitFor(ctx, a, pid)
			if err != nil {
				t.Fatal(err)
			}
			es, _ := transcripts.ListsFor(ctx, a, nonEmpty(pid))
			var sp []string
			for _, e := range es {
				sp = append(sp, e.Word)
			}
			sort.Strings(sp)
			ws, _, _, _ := providers.LoadSettings(ctx, a, providers.ElevenLabsName, pid)
			r := providers.ResolveVoice(ws, nil, nil)
			favs := ""
			if ws != nil && len(ws.Favorites) > 0 {
				favs = ws.Favorites[0].Name
			}
			got := uses{k.Primary, k.Font, strings.Join(sp, ","), r.VoiceID, r.ModelID, r.Region, favs}
			if got != w {
				t.Errorf("%s: %q uses %+v, want %+v", round, pid, got, w)
			}
		}
		// Client's voice: Cleo with the workspace's stability under its own speed.
		r, _ := providers.ResolveFor(ctx, a, client, nil)
		if r.Settings.Speed == nil || *r.Settings.Speed != 1.1 || r.Settings.Stability == nil || *r.Settings.Stability != 0.4 {
			t.Errorf("%s: Client's voice settings %+v", round, r.Settings)
		}
	}
	for round := 1; round <= 2; round++ {
		def, err := store.MoveContentSettings(ctx, a.DB, a.WorkspaceID())
		if err != nil {
			t.Fatal(err)
		}
		if def != fernbrook {
			t.Fatalf("round %d: the default is %s, want fernbrook", round, def)
		}
		check("round " + string(rune('0'+round)))
	}
	g, _ := content.Load(ctx, a)
	for pid, wantUses := range map[string]string{fernbrook: models.ContentOwn, client: models.ContentOwn, speller: models.ContentOwn, plain: models.ContentDefault, old: models.ContentDefault} {
		if u, _ := g.Uses(pid); u != wantUses {
			t.Errorf("%s uses %q, want %q", g.Name(pid), u, wantUses)
		}
	}
	var n int64
	n, _ = a.DB.C(store.CContentSettings).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID()})
	if n != 3 {
		t.Fatalf("%d settings records, want 3 (fernbrook, Client, Speller)", n)
	}
	// The old places are cleared; money, the generated-video settings and rules stay.
	var w models.Workspace
	_ = a.DB.C(store.CWorkspaces).FindOne(ctx, bson.M{"_id": a.WorkspaceID()}).Decode(&w)
	if w.BrandKit != nil {
		t.Fatalf("the workspace's kit moved: %+v", w.BrandKit)
	}
	if el := w.Providers["elevenlabs"]; el.VoiceID != "" || el.Favorites != nil || el.Region != "" || w.Providers["gemini"].DailyCapUSD == nil || *w.Providers["gemini"].DailyCapUSD != 5 {
		t.Fatalf("the workspace's providers: %+v", w.Providers)
	}
	var e models.Project
	_ = a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), fernbrook, &e)
	if len(e.Rules) != 1 || e.Providers["elevenlabs"].BudgetCredits == nil || *e.Providers["elevenlabs"].BudgetCredits != 900 {
		t.Fatalf("fernbrook's rules and budget stay: %+v %+v", e.Rules, e.Providers)
	}
	var cl models.Project
	_ = a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), client, &cl)
	if cl.BrandKit != nil || cl.Providers["elevenlabs"].VoiceID != "" || cl.Providers["elevenlabs"].BudgetCredits == nil {
		t.Fatalf("Client's old places cleared, its budget kept: %+v %+v", cl.BrandKit, cl.Providers)
	}
	if left, _ := a.DB.C(store.CSpelling).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectId": bson.M{"$exists": false}}); left != 0 {
		t.Fatalf("%d workspace spellings left", left)
	}
}

func nonEmpty(id string) []string {
	if id == "" {
		return nil
	}
	return []string{id}
}

// The shape of a real installation before Content Settings: no brand kit, one
// word on the workspace's spelling list, the most recently active project and
// an older one. The active one becomes the default with the word; the other
// uses it; running the move again changes nothing.
func TestMigration36OnAnInstallLikeTodays(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := a.DB.C(store.CWorkspaces).InsertOne(ctx, bson.M{"_id": a.WorkspaceID(), "workspaceId": a.WorkspaceID(), "name": "T", "createdAt": now}); err != nil {
		t.Fatal(err)
	}
	active := legacyProject(t, a, a.DB, "fernbrook", now, false, nil)
	older := legacyProject(t, a, a.DB, "Test project", now.Add(-2*time.Hour), false, nil)
	legacySpelling(t, a, a.DB, "", "FernBrook")
	for round := 0; round < 2; round++ {
		def, err := store.MoveContentSettings(ctx, a.DB, a.WorkspaceID())
		if err != nil || def != active {
			t.Fatalf("round %d: default %s %v", round, def, err)
		}
		for _, pid := range []string{active, older, ""} {
			es, _ := transcripts.ListsFor(ctx, a, nonEmpty(pid))
			if len(es) != 1 || es[0].Word != "FernBrook" {
				t.Fatalf("round %d: %q's spellings %+v", round, pid, es)
			}
			if k, err := ops.KitFor(ctx, a, pid); err != nil || k == nil || k.Logo != "" || k.Primary != "" {
				t.Fatalf("round %d: no kit before, none after: %+v %v", round, k, err)
			}
		}
	}
	g, _ := content.Load(ctx, a)
	if u, _ := g.Uses(older); u != models.ContentDefault || g.Resolve(older).Owner != active || len(g.UsedBy(active)) != 1 {
		t.Fatalf("Test project uses fernbrook's: %q %+v", u, g.Resolve(older))
	}
	if n, _ := a.DB.C(store.CContentSettings).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID()}); n != 1 {
		t.Fatalf("%d settings records, want 1", n)
	}
}
