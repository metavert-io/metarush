package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Layout packs (D263–D267): a Content Settings part that follows and copies
// like the others; graphics that name a role are drawn with it; a pack with
// code becomes current only when a person accepts it.

func callHuman[T any](t *testing.T, a *app.App, op string, in any) (*T, error) {
	t.Helper()
	return CallTyped[T](context.Background(), a, op, in, Caller{Human: true})
}

// acmeTitle records a checked motion graphic for a title card (waiting for a person).
func acmeTitle(t *testing.T, a *app.App, projectID string) string {
	t.Helper()
	hash := strings.Repeat("ac", 32)
	_, err := motion.SaveCheck(context.Background(), a.DB, &models.MotionComponent{WorkspaceID: a.WorkspaceID(), Hash: hash, Name: "acme-title", ProjectID: projectID, Region: "full", MinDuration: 2, MaxDuration: 6,
		SchemaJSON: `{"type":"object","properties":{"headline":{"type":"string"},"tagline":{"type":"string"},"logo":{"type":"string","format":"metarush-resource"},"wipe":{"type":"string","enum":["left","right"]}},"required":["headline"],"additionalProperties":false}`,
		Files:      []models.MotionFile{}, Imports: []models.MotionImport{}, Check: models.MotionCheckInfo{By: a.Actor, At: time.Now().UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func overlayNamed(t *testing.T, s editspec.Spec, id string) editspec.Overlay {
	t.Helper()
	for _, o := range s.Overlays {
		if o.ID == id {
			return o
		}
	}
	t.Fatalf("no graphic %s in %+v", id, s.Overlays)
	return editspec.Overlay{}
}

func TestLayoutPacks(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	fernbrook := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "fernbrook"}).Project
	client := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Client"}).Project

	// MetaRush's own pack until the settings have one.
	show := call[ContentPackOut](t, a, "content.pack.show", ContentPackIn{ProjectID: client.ID})
	if show.Own || !show.ReadOnly || show.ProjectID != fernbrook.ID || len(show.Roles) != 7 || show.Roles[0].Role != editspec.PackTitleCard || show.Roles[0].Piece.Template != editspec.TplTitleCard {
		t.Fatalf("MetaRush's own pack: %+v", show)
	}
	cs := call[ContentShowOut](t, a, "content.show", ContentShowIn{ProjectID: client.ID})
	if p := partOf(cs, "pack"); p.Label != "Layout pack" || !p.Empty || !strings.Contains(p.Summary, "MetaRush's own") {
		t.Fatalf("the part: %+v", p)
	}

	// The default's pack: a cinematic lower third, centred; a round inset with a frame.
	name, center, circle, accent := "Acme", json.RawMessage(`{"align":"center"}`), "circle", "accent"
	set := call[ContentPackOut](t, a, "content.pack.set", ContentPackSetIn{Name: &name, Role: "lower third", Template: editspec.TplLowerThirdCinematic, Fields: center, PipShape: &circle, PipFrame: &accent})
	if !set.Own || set.Pack.Version != 1 || set.Pack.Name != "Acme" || set.Pack.Pieces[editspec.PackLowerThird].Template != editspec.TplLowerThirdCinematic ||
		set.Pack.Pieces[editspec.PackTitleCard].Template != editspec.TplTitleCard || set.Pack.Layouts == nil || set.Pack.Layouts.PiP.Shape != "circle" {
		t.Fatalf("a pack of its own starts as MetaRush's own: %+v", set.Pack)
	}
	if !strings.Contains(set.Summary, "These are “fernbrook”'s settings, which new projects use.") {
		t.Fatalf("summary: %s", set.Summary)
	}
	if err := callErr(a, "content.pack.set", ContentPackSetIn{ProjectID: client.ID, Name: &name}); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("a follower can't change the pack: %v", err)
	}
	if err := callErr(a, "content.pack.set", ContentPackSetIn{Role: "title card", Template: editspec.TplEndCard}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("an end card can't be the title card: %v", err)
	}
	hash := acmeTitle(t, a, fernbrook.ID)
	ref := "acme-title@" + hash[:8]
	if err := callErr(a, "content.pack.set", ContentPackSetIn{Role: "title-card", Motion: ref}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("code reaches a pack only through a person: %v", err)
	}
	if _, err := callHuman[ContentPackOut](t, a, "content.pack.set", ContentPackSetIn{Role: "title-card", Motion: ref}); err == nil || !strings.Contains(err.Error(), "Nobody has approved") {
		t.Fatalf("a person can't use a graphic nobody approved: %v", err)
	}

	// A new cut names roles; the pack draws them.
	as := talkAsset(t, a, client.ID)
	first := savePlan(t, a, fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"title":"Spring","output":{"preset":"landscape-1080p","fps":30},`+
		`"clips":[{"id":"a","assetId":%q,"in":1,"out":13}],"overlays":[{"id":"ada","role":"lower-third","at":2,"duration":3,"params":{"name":"Ada","align":"left"}},`+
		`{"id":"intro","role":"title-card","at":0,"duration":0,"params":{"title":"Spring"}}]}}`, client.ID, as.ID))
	s := specOf(t, a, first.Revision.ID)
	ada := overlayNamed(t, s, "ada")
	if ada.Template != editspec.TplLowerThirdCinematic || !strings.Contains(string(ada.Params), `"align":"center"`) || !strings.Contains(string(ada.Params), `"name":"Ada"`) {
		t.Fatalf("the lower third is the pack's: %+v %s", ada, ada.Params)
	}
	if intro := overlayNamed(t, s, "intro"); intro.Template != editspec.TplTitleCard || intro.Duration != 3 || intro.Role != editspec.PackTitleCard {
		t.Fatalf("the title card is the pack's, for its seconds: %+v", intro)
	}
	// A later change that names a role without a template is filled too.
	plan := fmt.Sprintf(`{"baseRevisionId":%q,"ops":[{"op":"addOverlay","overlay":{"id":"q","role":"quote-card","at":6,"duration":4,"params":{"text":"It works.","attribution":"Ada"}}}]}`, first.Revision.ID)
	second := savePlan(t, a, plan)
	if q := overlayNamed(t, specOf(t, a, second.Revision.ID), "q"); q.Template != editspec.TplSlideQuote {
		t.Fatalf("the quote card: %+v", q)
	}

	// Claude proposes a pack whose title card is its motion graphic: a draft until a person accepts it.
	proposed := editspec.LayoutPack{Name: "Acme spring", Pieces: map[string]editspec.PackPiece{
		editspec.PackTitleCard:  {Motion: &editspec.MotionRef{Name: "acme-title", Hash: hash}, Fields: json.RawMessage(`{"wipe":"left"}`), Logo: true, Seconds: 4},
		editspec.PackLowerThird: {Template: editspec.TplLowerThirdCinematic, Fields: center},
	}}
	dry := call[ContentPackOut](t, a, "content.pack.propose", ContentPackProposeIn{Pack: proposed, DryRun: true})
	if dry.Draft == nil || dry.Draft.Ready || len(dry.Draft.Waiting) != 1 || dry.Draft.Waiting[0].Name != "acme-title" || !strings.Contains(dry.Summary, "approves the motion graphic acme-title") {
		t.Fatalf("a dry run says what waits: %+v", dry.Draft)
	}
	if again := call[ContentPackOut](t, a, "content.pack.show", ContentPackIn{}); again.Draft != nil {
		t.Fatal("a dry run saves nothing")
	}
	prop := call[ContentPackOut](t, a, "content.pack.propose", ContentPackProposeIn{Pack: proposed, Note: "Acme's spring look", Origin: "assistant:cnv_x"})
	if prop.Draft == nil || prop.Draft.BaseVersion != 1 || prop.Draft.By != "Claude" || prop.Pack.Name != "Acme" {
		t.Fatalf("the draft waits beside the pack: %+v", prop)
	}
	if err := callErr(a, "content.pack.accept", ContentPackAcceptIn{}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("accepting is a person's: %v", err)
	}
	if _, err := callHuman[ContentPackOut](t, a, "content.pack.accept", ContentPackAcceptIn{}); apperr.From(err).Code != apperr.Conflict || !strings.Contains(err.Error(), "acme-title") {
		t.Fatalf("not before its graphic is approved: %v", err)
	}
	if _, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), hash, motion.Approve, a.Actor, ""); err != nil {
		t.Fatal(err)
	}
	acc, err := callHuman[ContentPackOut](t, a, "content.pack.accept", ContentPackAcceptIn{})
	if err != nil {
		t.Fatal(err)
	}
	if acc.Pack.Version != 2 || acc.Pack.Name != "Acme spring" || acc.Draft != nil || acc.Pack.Pieces[editspec.PackTitleCard].Motion == nil {
		t.Fatalf("accepted: %+v", acc)
	}
	if len(acc.LatestCuts) != 1 || acc.LatestCuts[0].ProjectID != client.ID || len(acc.LatestCuts[0].Changes) == 0 {
		t.Fatalf("the latest cut is offered the pack: %+v", acc.LatestCuts)
	}

	// Use the pack on the latest cut: checked, saved, then nothing more to do.
	chk := call[CutEditOut](t, a, "content.pack.apply", ContentPackApplyIn{CutEditCommon: CutEditCommon{ProjectID: client.ID, DryRun: true}})
	if chk.Saved || chk.Check == nil || chk.Check.ConfirmToken == "" || !chk.Check.Valid {
		t.Fatalf("the check: %+v %+v", chk, chk.Check)
	}
	done := call[CutEditOut](t, a, "content.pack.apply", ContentPackApplyIn{CutEditCommon: CutEditCommon{ProjectID: client.ID, ConfirmToken: chk.Check.ConfirmToken}})
	if !done.Saved {
		t.Fatalf("saved: %+v", done)
	}
	s3 := specOf(t, a, done.Applied.Revision.ID)
	intro := overlayNamed(t, s3, "intro")
	if intro.Motion == nil || intro.Motion.Hash != hash || !strings.Contains(string(intro.Params), `"headline":"Spring"`) || !strings.Contains(string(intro.Params), `"wipe":"left"`) {
		t.Fatalf("the intro is Acme's motion graphic with its words: %+v %s", intro, intro.Params)
	}
	if again := call[CutEditOut](t, a, "content.pack.apply", ContentPackApplyIn{CutEditCommon: CutEditCommon{ProjectID: client.ID}}); !again.NoChange {
		t.Fatalf("again: %+v", again)
	}

	// A draft made before a change can't undo it.
	call[ContentPackOut](t, a, "content.pack.propose", ContentPackProposeIn{Pack: editspec.LayoutPack{Name: "Old"}})
	call[ContentPackOut](t, a, "content.pack.set", ContentPackSetIn{Role: "quote card", Template: editspec.TplQuoteKinetic})
	stale := call[ContentPackOut](t, a, "content.pack.show", ContentPackIn{})
	if stale.Draft == nil || !stale.Draft.Stale || stale.Draft.Ready {
		t.Fatalf("stale: %+v", stale.Draft)
	}
	if _, err := callHuman[ContentPackOut](t, a, "content.pack.accept", ContentPackAcceptIn{}); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("a stale draft is refused: %v", err)
	}
	dec := call[ContentPackOut](t, a, "content.pack.decline", ContentPackDeclineIn{Note: "not now"})
	if dec.Draft != nil || dec.Pack.Version != 3 {
		t.Fatalf("declined: %+v", dec)
	}

	// A card's pack whose graphic waits is kept as the draft, not made current.
	other := strings.Repeat("bd", 32)
	_, _ = motion.SaveCheck(ctx, a.DB, &models.MotionComponent{WorkspaceID: a.WorkspaceID(), Hash: other, Name: "acme-end", Region: "full", MinDuration: 5, MaxDuration: 20,
		SchemaJSON: `{"type":"object","properties":{"headline":{"type":"string"}},"additionalProperties":false}`, Files: []models.MotionFile{}, Imports: []models.MotionImport{},
		Check: models.MotionCheckInfo{By: a.Actor, At: time.Now().UTC()}})
	card := editspec.LayoutPack{Name: "Acme end", Pieces: map[string]editspec.PackPiece{editspec.PackEndScreen: {Motion: &editspec.MotionRef{Name: "acme-end", Hash: other}}}}
	kept, err := callHuman[ContentPackOut](t, a, "content.pack.accept", ContentPackAcceptIn{Pack: &card, Origin: "assistant:cnv_y"})
	if err != nil {
		t.Fatal(err)
	}
	if kept.Pack.Version != 3 || kept.Draft == nil || kept.Draft.Pack.Name != "Acme end" || !strings.Contains(kept.Summary, "it isn't the pack yet") {
		t.Fatalf("kept as the draft: %+v", kept)
	}

	// Previews in the brand: a plan per graphic, tiles per call layout. (The
	// accepted pack was a whole pack, without call layouts: set them again.)
	call[ContentPackOut](t, a, "content.pack.set", ContentPackSetIn{PipShape: &circle, PipFrame: &accent})
	pv := call[ContentPackPreviewOut](t, a, "content.pack.preview", ContentPackPreviewIn{ProjectID: client.ID})
	if pv.Shape != "landscape" || len(pv.Previews) != 7 {
		t.Fatalf("previews: %+v", pv)
	}
	for _, p := range pv.Previews {
		switch {
		case p.Graphic && (p.Plan == nil || len(p.Plan.Overlays) != 1 || p.Plan.Width != 1920):
			t.Errorf("%s: %+v", p.Role, p.Plan)
		case p.Role == editspec.PackPiP && (len(p.Tiles) != 2 || p.Tiles[1].Rect.W != p.Tiles[1].Rect.H || p.Tiles[1].Border == nil):
			t.Errorf("the round inset with its frame: %+v", p.Tiles)
		}
	}
	if pv.Previews[0].Plan.Overlays[0].Motion == nil || pv.Previews[0].Plan.Overlays[0].Motion.Region != "full" {
		t.Fatalf("the title card's preview is the motion graphic: %+v", pv.Previews[0].Plan.Overlays[0])
	}
	one := call[ContentPackPreviewOut](t, a, "content.pack.preview", ContentPackPreviewIn{Role: "end-screen", Shape: "vertical", Draft: true})
	if len(one.Previews) != 1 || one.Shape != "vertical" || one.Previews[0].Plan.Height != 1920 || !one.Previews[0].Waiting {
		t.Fatalf("one role of the draft: %+v", one)
	}

	// Claude's context; a copy of the settings carries the pack; reset.
	if line := PackContext(ctx, a, client.ID); !strings.Contains(line, "Layout pack from “fernbrook”'s settings") || !strings.Contains(line, "acme-title") {
		t.Fatalf("Claude's context: %s", line)
	}
	copied := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Copied"}).Project
	call[ContentShowOut](t, a, "content.own", ContentOwnIn{ProjectID: copied.ID, Copy: true})
	if ps := content.PackFor(ctx, a, copied.ID); ps.Own == nil || ps.Own.Name != "Acme spring" {
		t.Fatalf("a copy of the settings has the pack: %+v", ps.Own)
	}
	// Accepting is of what the person saw: a card made before a change, or a
	// proposed pack replaced since, is refused.
	seen := call[ContentPackOut](t, a, "content.pack.show", ContentPackIn{})
	old := seen.Version - 1
	if _, err := callHuman[ContentPackOut](t, a, "content.pack.accept", ContentPackAcceptIn{Pack: &editspec.LayoutPack{Name: "Late"}, BaseVersion: &old}); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("a card made before a change: %v", err)
	}
	early := seen.Draft.At.Add(-time.Second)
	if _, err := callHuman[ContentPackOut](t, a, "content.pack.accept", ContentPackAcceptIn{DraftAt: &early}); apperr.From(err).Code != apperr.Conflict || !strings.Contains(err.Error(), "Another layout pack was proposed") {
		t.Fatalf("another proposed pack since: %v", err)
	}

	// Back to MetaRush's own: the proposed pack goes too, and versions keep counting.
	reset := call[ContentPackOut](t, a, "content.pack.set", ContentPackSetIn{Reset: true})
	if reset.Own || reset.Draft != nil || reset.Version != seen.Version+1 || !strings.Contains(reset.Summary, "MetaRush's own layout pack again") {
		t.Fatalf("reset: %+v", reset)
	}
	again := call[ContentPackOut](t, a, "content.pack.set", ContentPackSetIn{Name: &name})
	if again.Pack.Version != seen.Version+2 || again.Version != again.Pack.Version {
		t.Fatalf("versions keep counting after a reset: %d (was %d)", again.Pack.Version, seen.Version)
	}
}

// A pack's motion graphic that imports a shared library (D171): proposing pins
// the library's newest version; the pack becomes current only once the
// library is approved too.
func TestLayoutPackSharedLibraries(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Acme"}).Project
	lib, gfx := strings.Repeat("1b", 32), strings.Repeat("9f", 32)
	for _, c := range []models.MotionComponent{
		{Hash: lib, Name: "acme-theme", Kind: models.MotionKindLibrary, Region: "full", SchemaJSON: `{}`},
		{Hash: gfx, Name: "acme-title", Libraries: []string{"acme-theme"}, Region: "full", MinDuration: 2, MaxDuration: 6,
			SchemaJSON: `{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}`},
	} {
		c.WorkspaceID, c.ProjectID, c.Files, c.Imports, c.Check = a.WorkspaceID(), p.ID, []models.MotionFile{}, []models.MotionImport{}, models.MotionCheckInfo{By: a.Actor, At: time.Now().UTC()}
		if _, err := motion.SaveCheck(ctx, a.DB, &c); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), gfx, motion.Approve, a.Actor, ""); err != nil {
		t.Fatal(err)
	}
	pack := editspec.LayoutPack{Name: "Acme", Pieces: map[string]editspec.PackPiece{editspec.PackTitleCard: {Motion: &editspec.MotionRef{Name: "acme-title", Hash: gfx}}}}
	prop := call[ContentPackOut](t, a, "content.pack.propose", ContentPackProposeIn{Pack: pack})
	d := prop.Draft
	if d == nil || d.Pack.Pieces[editspec.PackTitleCard].Motion.Libs["acme-theme"] != lib || d.Ready || len(d.Waiting) != 1 || !d.Waiting[0].Library || d.Waiting[0].Name != "acme-theme" ||
		!strings.Contains(d.Why, "the shared library acme-theme") {
		t.Fatalf("the library is pinned and waits: %+v", d)
	}
	if _, err := callHuman[ContentPackOut](t, a, "content.pack.accept", ContentPackAcceptIn{}); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("not before the library is approved: %v", err)
	}
	if _, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), lib, motion.Approve, a.Actor, ""); err != nil {
		t.Fatal(err)
	}
	acc, err := callHuman[ContentPackOut](t, a, "content.pack.accept", ContentPackAcceptIn{})
	if err != nil || !acc.Own || acc.Pack.Pieces[editspec.PackTitleCard].Motion.Libs["acme-theme"] != lib {
		t.Fatalf("accepted with its library: %+v %v", acc, err)
	}

	// Another graphic of the pack uses the version the pack already pins, even when a newer one is approved.
	lib2, end := strings.Repeat("2b", 32), strings.Repeat("e7", 32)
	for _, c := range []models.MotionComponent{
		{Hash: lib2, Name: "acme-theme", Kind: models.MotionKindLibrary, Region: "full", SchemaJSON: `{}`},
		{Hash: end, Name: "acme-end", Libraries: []string{"acme-theme"}, Region: "full", MinDuration: 5, MaxDuration: 20,
			SchemaJSON: `{"type":"object","properties":{"headline":{"type":"string"}},"additionalProperties":false}`},
	} {
		c.WorkspaceID, c.ProjectID, c.Files, c.Imports, c.Check = a.WorkspaceID(), p.ID, []models.MotionFile{}, []models.MotionImport{}, models.MotionCheckInfo{By: a.Actor, At: time.Now().UTC()}
		if _, err := motion.SaveCheck(ctx, a.DB, &c); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range []string{end, lib2} {
		if _, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), h, motion.Approve, a.Actor, ""); err != nil {
			t.Fatal(err)
		}
	}
	both, err := callHuman[ContentPackOut](t, a, "content.pack.set", ContentPackSetIn{Role: "end-screen", Motion: "acme-end@" + end[:8]})
	if err != nil || both.Pack.Pieces[editspec.PackEndScreen].Motion.Libs["acme-theme"] != lib {
		t.Fatalf("the end screen uses the pack's version of the library: %+v %v", both.Pack.Pieces[editspec.PackEndScreen].Motion, err)
	}
	// The newer approved version is offered, and a person moves the whole pack to it.
	if u := both.LibraryUpdates; len(u) != 1 || u[0].To != lib2 || len(u[0].Roles) != 2 {
		t.Fatalf("the newer library is offered: %+v", u)
	}
	if err := callErr(a, "content.pack.set", ContentPackSetIn{Library: "acme-theme@" + lib2[:8]}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("moving a pack's code is a person's: %v", err)
	}
	moved, err := callHuman[ContentPackOut](t, a, "content.pack.set", ContentPackSetIn{Library: "acme-theme@" + lib2[:8]})
	if err != nil || moved.Pack.Pieces[editspec.PackTitleCard].Motion.Libs["acme-theme"] != lib2 || moved.Pack.Pieces[editspec.PackEndScreen].Motion.Libs["acme-theme"] != lib2 || len(moved.LibraryUpdates) != 0 {
		t.Fatalf("both graphics moved: %+v %v", moved, err)
	}
	// Approving a newer library offers the packs on an older version.
	lib3 := strings.Repeat("3c", 32)
	c := models.MotionComponent{WorkspaceID: a.WorkspaceID(), ProjectID: p.ID, Hash: lib3, Name: "acme-theme", Kind: models.MotionKindLibrary, Region: "full", SchemaJSON: `{}`,
		Files: []models.MotionFile{}, Imports: []models.MotionImport{}, Check: models.MotionCheckInfo{By: a.Actor, At: time.Now().UTC()}}
	if _, err := motion.SaveCheck(ctx, a.DB, &c); err != nil {
		t.Fatal(err)
	}
	dec, err := callHuman[MotionDecideOut](t, a, "motion.approve", MotionDecideIn{Ref: "acme-theme@" + lib3[:8]})
	if err != nil || len(dec.Packs) != 1 || dec.Packs[0].ProjectID != p.ID || !strings.Contains(dec.Summary, "uses an older version") {
		t.Fatalf("the approval offers the pack: %+v %v", dec, err)
	}

	// A card's pack is accepted as the card showed it: nothing is pinned at the click.
	unpinned := editspec.LayoutPack{Name: "Bare", Pieces: map[string]editspec.PackPiece{editspec.PackTitleCard: {Motion: &editspec.MotionRef{Name: "acme-title", Hash: gfx}}}}
	if _, err := callHuman[ContentPackOut](t, a, "content.pack.accept", ContentPackAcceptIn{Pack: &unpinned}); err == nil || !strings.Contains(err.Error(), "needs a version of its shared library") {
		t.Fatalf("a card's pack without its library pinned is refused: %v", err)
	}
}

// A checked change that names a part is saved only if the pack it was
// filled from is still the pack (the confirmation binds it).
func TestLayoutPackChangeBetweenCheckAndSave(t *testing.T) {
	a := testapp.New(t)
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Acme"}).Project
	as := talkAsset(t, a, p.ID)
	first := savePlan(t, a, fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"landscape-1080p","fps":30},"clips":[{"id":"a","assetId":%q,"in":1,"out":13}]}}`, p.ID, as.ID))
	plan := json.RawMessage(fmt.Sprintf(`{"baseRevisionId":%q,"ops":[{"op":"addOverlay","overlay":{"id":"ada","role":"lower-third","at":2,"duration":3,"params":{"name":"Ada"}}}]}`, first.Revision.ID))
	chk := call[EditCheckOut](t, a, "edit.check", EditCheckIn{Plan: plan})
	if !chk.Valid {
		t.Fatalf("check: %+v", chk.Errors)
	}
	call[ContentPackOut](t, a, "content.pack.set", ContentPackSetIn{Role: "lower-third", Template: editspec.TplLowerThirdCinematic})
	_, err := CallTyped[EditApplyOut](context.Background(), a, "edit.apply", EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken}, Caller{Human: true})
	if ae := apperr.From(err); ae.Code != apperr.ConfirmRequired {
		t.Fatalf("a pack changed since the check: %v", err)
	}
	again := call[EditCheckOut](t, a, "edit.check", EditCheckIn{Plan: plan})
	saved, err := CallTyped[EditApplyOut](context.Background(), a, "edit.apply", EditApplyIn{Plan: plan, ConfirmToken: again.ConfirmToken}, Caller{Human: true})
	if err != nil || overlayNamed(t, specOf(t, a, saved.Revision.ID), "ada").Template != editspec.TplLowerThirdCinematic {
		t.Fatalf("checked again, it saves with the pack as it is: %v", err)
	}
}

// An AI app that may only propose (D248): using the pack on the latest cut is
// a version draft; changing, proposing or setting aside a pack isn't kept as
// a draft, each with a plain reason; accepting is a person's.
func TestLayoutPackDrafts(t *testing.T) {
	for name, draftable := range map[string]bool{"content.pack.apply": true, "content.pack.set": false, "content.pack.propose": false, "content.pack.decline": false, "content.pack.accept": false} {
		op, ok := Get(name)
		if !ok {
			t.Fatalf("no op %s", name)
		}
		if Draftable(op) != draftable {
			t.Errorf("%s draftable = %v", name, !draftable)
		}
		if why := whyNotDrafted(op); !draftable && (why == "" || strings.Contains(why, "doesn't keep this kind of change")) {
			t.Errorf("%s needs a plain reason of its own: %q", name, why)
		}
	}
}
