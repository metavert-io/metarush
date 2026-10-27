package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Logo files, the house style and house sign-offs (D198–D200) are Content
// Settings parts: they follow like the others (the default's, another
// project's, its own), edits through a follower are refused, and new work
// and the latest cuts get them.

// talkAsset is a 20-second clip in a project, for cuts.
func talkAsset(t *testing.T, a *app.App, projectID string) models.Asset {
	t.Helper()
	now := time.Now().UTC()
	as := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{projectID}, Kind: "video", Name: "talk.mov",
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: 20, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now})
	insert(t, a, store.CAssets, as)
	return as
}

// firstCut saves a project's first cut: one 12-second clip with a title card
// showing the brand's main logo, and a name bar.
func firstCut(t *testing.T, a *app.App, projectID string, as models.Asset, withLogo bool) *EditApplyOut {
	t.Helper()
	overlays := `[{"id":"names","template":"name.bar","span":{},"at":0,"duration":0,"params":{"name":"Ada"}}]`
	if withLogo {
		overlays = `[{"id":"title","template":"title.card","at":0.5,"duration":3,"params":{"title":"Hello","logo":"logo"}},` +
			`{"id":"names","template":"name.bar","span":{},"at":0,"duration":0,"params":{"name":"Ada"}}]`
	}
	return savePlan(t, a, fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"title":"Why we volunteer","output":{"preset":"landscape-1080p","fps":30},`+
		`"clips":[{"id":"a","assetId":%q,"in":1,"out":13}],"overlays":%s}}`, projectID, as.ID, overlays))
}

func TestLogoFilesFollowContentSettings(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	fernbrook := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "fernbrook"}).Project
	client := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Client"}).Project

	// A kit as it was before: its logo is the main one, its logo for dark backgrounds stays one.
	main := addResource(t, a, svgFile(t, "fernbrook-logo.svg", "#2E7D50"), resources.AddOptions{ProjectID: fernbrook.ID, FileName: "fernbrook-logo.svg"})
	white := addResource(t, a, svgFile(t, "fernbrook-white.svg", "#F7F5F0"), resources.AddOptions{ProjectID: fernbrook.ID, FileName: "fernbrook-white.svg"})
	m, w := main.Name, white.Name
	call[BrandShowOut](t, a, "brand.set", BrandSetIn{Logo: &m, LogoDark: &w})
	show := call[BrandShowOut](t, a, "brand.show", BrandShowIn{ProjectID: client.ID})
	if len(show.Logos) != 5 || show.Logos[0].Which != models.LogoMain || show.Logos[0].Resource == nil || show.Logos[0].Resource.ID != main.ID ||
		show.Logos[1].Label != "For dark backgrounds" || show.Logos[1].Resource == nil || show.Logos[1].Resource.ID != white.ID ||
		show.Logos[2].Resource != nil || show.Logos[3].Label != "Square icon" || show.Logos[4].Pin != BrandLogoWordmarkPin || !show.ReadOnly {
		t.Fatalf("the named logos as a follower sees them: %+v", show.Logos)
	}

	// The other three, each a picture through the resource pipeline, named in the default's settings.
	byWhich := map[string]*models.Resource{models.LogoMain: main, models.LogoDark: white}
	for _, x := range []struct{ which, fill string }{{"light", "#111111"}, {"square", "#E0A458"}, {"wordmark", "#1B1B1F"}} {
		r := addResource(t, a, svgFile(t, "fernbrook-"+x.which+".svg", x.fill), resources.AddOptions{ProjectID: fernbrook.ID, FileName: "fernbrook-" + x.which + ".svg"})
		out := call[BrandShowOut](t, a, "content.logo.set", ContentLogoSetIn{Which: x.which, Resource: r.Name})
		which := LogoWhich(x.which)
		byWhich[which] = r
		if !strings.Contains(out.Message, "is the "+logoWords(which)+" of “fernbrook”'s brand now") || !strings.Contains(out.Message, "These are “fernbrook”'s settings, which new projects use.") {
			t.Fatalf("%s: %s", x.which, out.Message)
		}
	}
	// Through a follower: refused, with the way to give it its own.
	err := callErr(a, "content.logo.set", ContentLogoSetIn{ProjectID: client.ID, Which: "icon", Resource: main.ID})
	if ae := apperr.From(err); ae.Code != apperr.Conflict || !strings.Contains(ae.Command(), "content own") {
		t.Fatalf("a follower can't change the logos: %v", err)
	}
	if err := callErr(a, "content.logo.set", ContentLogoSetIn{Which: "favicon", Resource: main.ID}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("an unknown logo: %v", err)
	}
	// A font isn't a logo.
	fp := filepath.Join(t.TempDir(), "Brand.woff2")
	if err := os.WriteFile(fp, append([]byte("wOF2"), []byte(strings.Repeat("f", 300))...), 0o644); err != nil {
		t.Fatal(err)
	}
	font := addResource(t, a, fp, resources.AddOptions{ProjectID: fernbrook.ID, FileName: "Brand.woff2"})
	if err := callErr(a, "content.logo.set", ContentLogoSetIn{Which: "icon", Resource: font.ID}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("a font as a logo: %v", err)
	}
	// Nor is a web page snapshot (its logos are pictures of their own).
	page, _, _, _ := siteWithBrand(t, a, fernbrook.ID)
	if err := callErr(a, "content.logo.set", ContentLogoSetIn{Which: "wordmark", Resource: page.ID}); apperr.From(err).Code != apperr.InvalidInput || !strings.Contains(err.Error(), "not a picture") {
		t.Fatalf("a page snapshot as a logo: %v", err)
	}
	// The part says how many; every logo is visible to a project using the settings.
	cs := call[ContentShowOut](t, a, "content.show", ContentShowIn{ProjectID: client.ID})
	if p := partOf(cs, "brand"); !strings.Contains(p.Summary, "5 logos") {
		t.Fatalf("brand part: %+v", p)
	}
	visible := kitResourceIDs(ctx, a, client.ID)
	for which, r := range byWhich {
		if !visible[r.ID] {
			t.Errorf("the %s logo isn't visible to a follower", which)
		}
	}
	spec := &editspec.Spec{Resources: map[string]editspec.ResourcePin{BrandLogoIconPin: {ID: byWhich[models.LogoIcon].ID, SHA256: byWhich[models.LogoIcon].Current}}}
	rf, err := loadResourceFacts(ctx, a, client.ID, &projectlinks.View{Names: map[string]string{}}, spec)
	if err != nil {
		t.Fatal(err)
	}
	if ri, ok := rf.info(byWhich[models.LogoIcon].ID, byWhich[models.LogoIcon].Current); !ok || !ri.Visible {
		t.Fatalf("an uploaded logo is a resource followers may pin: %+v %v", ri, ok)
	}

	// A new cut pins them all, and its title card shows the logo made for its background.
	as := talkAsset(t, a, client.ID)
	rev := firstCut(t, a, client.ID, as, true)
	s := specOf(t, a, rev.Revision.ID)
	for _, pin := range []string{BrandLogoPin, BrandLogoDarkPin, BrandLogoLightPin, BrandLogoIconPin, BrandLogoWordmarkPin} {
		if _, ok := s.Resources[pin]; !ok {
			t.Errorf("the new cut doesn't pin %s: %v", pin, s.Resources)
		}
	}
	if s.Brand == nil || s.Brand.Logo != BrandLogoPin || s.Brand.LogoDark != BrandLogoDarkPin || s.Brand.LogoLight != BrandLogoLightPin {
		t.Fatalf("the new cut's brand: %+v", s.Brand)
	}
	plan := call[editspec.RenderPlan](t, a, "revision.plan", RevisionPlanIn{ID: rev.Revision.ID})
	want := BrandLogoLightPin
	if dark, _ := editspec.DarkBackground(plan.Brand.Background); dark {
		want = BrandLogoDarkPin
	}
	for _, o := range plan.Overlays {
		if o.ID == "title" && !strings.Contains(string(o.Params), `"logo":"`+want+`"`) {
			t.Errorf("the title card on %s shows %s, want %s", plan.Brand.Background, o.Params, want)
		}
	}

	// A new version of a logo: the latest cut is offered it, and taking the offer pins it.
	light2 := addResource(t, a, svgFile(t, "fernbrook-light-2.svg", "#222222"), resources.AddOptions{ProjectID: fernbrook.ID, FileName: "fernbrook-light-2.svg"})
	out := call[BrandShowOut](t, a, "content.logo.set", ContentLogoSetIn{Which: "light", Resource: light2.ID})
	if len(out.LatestCuts) != 1 || out.LatestCuts[0].ProjectID != client.ID || !strings.Contains(out.Message, "can use the new brand on its latest cut") {
		t.Fatalf("the offer: %+v %s", out.LatestCuts, out.Message)
	}
	chkBrand := call[CutEditOut](t, a, "brand.apply", BrandApplyIn{CutEditCommon: CutEditCommon{ProjectID: client.ID, DryRun: true}})
	if chkBrand.Check == nil || chkBrand.Check.ConfirmToken == "" {
		t.Fatalf("brand.apply check: %+v", chkBrand)
	}
	applied := call[CutEditOut](t, a, "brand.apply", BrandApplyIn{CutEditCommon: CutEditCommon{ProjectID: client.ID, ConfirmToken: chkBrand.Check.ConfirmToken}})
	if applied.Applied == nil {
		t.Fatalf("brand.apply: %+v", applied)
	}
	if s2 := specOf(t, a, applied.Applied.Revision.ID); s2.Resources[BrandLogoLightPin].ID != light2.ID {
		t.Fatalf("the latest cut pins the new light logo: %+v", s2.Resources[BrandLogoLightPin])
	}

	// Taking one away: the picture stays; saved versions keep their pins.
	rm := call[BrandShowOut](t, a, "content.logo.remove", ContentLogoRemoveIn{Which: "wordmark"})
	if rm.Logos[4].Resource != nil || !strings.Contains(rm.Message, "has no wordmark now") {
		t.Fatalf("removed: %+v %s", rm.Logos[4], rm.Message)
	}
	var still models.Resource
	if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), byWhich[models.LogoWordmark].ID, &still); err != nil || still.Removed {
		t.Fatalf("the picture stays: %v %+v", err, still.Removed)
	}
	if k, _ := KitFor(ctx, a, client.ID); k.LogoWordmark != "" || k.LogoIcon == "" {
		t.Fatalf("the follower's brand: %+v", k)
	}
}

func partOf(cs *ContentShowOut, name string) ContentPartOut {
	for _, p := range cs.Parts {
		if p.Name == name {
			return p
		}
	}
	return ContentPartOut{}
}

func TestHouseStyleNewCutsAndTheLatestCut(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	fernbrook := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "fernbrook"}).Project
	client := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Client"}).Project
	plain := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Plain"}).Project
	call[ContentShowOut](t, a, "content.own", ContentOwnIn{ProjectID: plain.ID})

	// With nothing set, a new cut is what it always was.
	as := talkAsset(t, a, plain.ID)
	s0 := specOf(t, a, firstCut(t, a, plain.ID, as, false).Revision.ID)
	want := s0
	want.Overlays = append([]editspec.Overlay(nil), s0.Overlays...)
	if s0.Captions != nil || s0.Output.Framing != editspec.FramingFaces || s0.Mix == nil || !s0.LevelsSpeech() || len(s0.Overlays) != 1 ||
		strings.Contains(string(s0.Overlays[0].Params), "animate") {
		t.Fatalf("a new cut without a house style: %+v", s0)
	}

	// Client has a cut from before the style.
	cas := talkAsset(t, a, client.ID)
	before := firstCut(t, a, client.ID, cas, false)

	cin, half, never, blend, off := "cinematic", "half", "never", 0.2, "off"
	on, boxed, large, top, follow := "on", "boxed", "large", "top", "follow"
	set := call[ContentStyleOut](t, a, "content.style.set", ContentStyleSetIn{Captions: &on, CaptionLook: &boxed, CaptionSize: &large, CaptionPosition: &top,
		NameBarHeight: &half, NameBarAnimate: &never, LowerThird: &cin, Framing: &follow, CutBlend: &blend, LevelSpeech: &off,
		Intro: &editspec.StyleCard{Template: editspec.TplTitleCard}, EndCard: &editspec.StyleCard{Template: editspec.TplEndCard, Title: "Volunteer with us", Handle: "@fernbrook"}})
	if set.ProjectID != fernbrook.ID || set.Empty || len(set.Lines) < 6 || !strings.Contains(set.Summary, "These are “fernbrook”'s settings, which new projects use.") {
		t.Fatalf("the default's house style: %+v", set)
	}
	if len(set.LatestCuts) != 1 || set.LatestCuts[0].ProjectID != client.ID || len(set.LatestCuts[0].Changes) == 0 || !strings.Contains(set.Summary, "can use the house style on its latest cut") {
		t.Fatalf("the latest cut is offered the style: %+v %s", set.LatestCuts, set.Summary)
	}
	// Through a follower: refused. The part shows it.
	if err := callErr(a, "content.style.set", ContentStyleSetIn{ProjectID: client.ID, Captions: &off}); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("a follower can't change the house style: %v", err)
	}
	if err := callErr(a, "content.style.set", ContentStyleSetIn{CaptionSize: &never}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("a size that isn't one: %v", err)
	}
	cs := call[ContentShowOut](t, a, "content.show", ContentShowIn{ProjectID: client.ID})
	if p := partOf(cs, "style"); p.Empty || !strings.Contains(p.Summary, "Captions: on, boxed, large, at the top") || p.Label != "House style" {
		t.Fatalf("the part: %+v", p)
	}
	if st := call[ContentStyleOut](t, a, "content.style.show", ContentStyleIn{ProjectID: client.ID}); !st.ReadOnly || st.ProjectID != fernbrook.ID || st.UsedBy != 0 {
		t.Fatalf("a follower sees the default's style: %+v", st)
	}
	// Plain has its own settings: its new cuts don't get it.
	if st, _, _ := content.StyleFor(ctx, a, plain.ID); st != nil {
		t.Fatalf("Plain uses its own settings: %+v", st)
	}

	// A new cut of a project using the default starts with the house style.
	third := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Third"}).Project
	tas := talkAsset(t, a, third.ID)
	s := specOf(t, a, firstCut(t, a, third.ID, tas, false).Revision.ID)
	if c := s.Captions; c == nil || !c.Enabled || c.Style != "boxed" || c.Size != "large" || c.Position != "top" {
		t.Errorf("captions: %+v", s.Captions)
	}
	if s.Output.Framing != editspec.FramingFollow || s.Mix == nil || s.LevelsSpeech() || s.Mix.CutBlend == nil || *s.Mix.CutBlend != 0.2 {
		t.Errorf("framing and sound: %q %+v", s.Output.Framing, s.Mix)
	}
	ids := map[string]editspec.Overlay{}
	for _, o := range s.Overlays {
		ids[o.ID] = o
	}
	if nb := ids["names"]; !strings.Contains(string(nb.Params), `"height":"half"`) || !strings.Contains(string(nb.Params), `"animate":"never"`) {
		t.Errorf("name bar: %s", nb.Params)
	}
	if in, ok := ids["intro"]; !ok || in.Template != editspec.TplTitleCard || !strings.Contains(string(in.Params), "Why we volunteer") {
		t.Errorf("intro: %+v", ids)
	}
	if end, ok := ids["end-card"]; !ok || end.At != 8 || !strings.Contains(string(end.Params), `"handle":"@fernbrook"`) {
		t.Errorf("end card: %+v", ids["end-card"])
	}

	// Use the house style on Client's latest cut: checked first, then saved; again is no change.
	chk := call[CutEditOut](t, a, "content.style.apply", ContentStyleApplyIn{CutEditCommon: CutEditCommon{ProjectID: client.ID, DryRun: true}})
	if chk.Saved || chk.Check == nil || chk.Check.ConfirmToken == "" || len(chk.Changes) == 0 {
		t.Fatalf("the check: %+v", chk)
	}
	done := call[CutEditOut](t, a, "content.style.apply", ContentStyleApplyIn{CutEditCommon: CutEditCommon{ProjectID: client.ID, ConfirmToken: chk.Check.ConfirmToken}})
	if !done.Saved || done.Applied == nil || done.Applied.Revision.Number != before.Revision.Number+1 {
		t.Fatalf("saved: %+v", done)
	}
	cs2 := specOf(t, a, done.Applied.Revision.ID)
	if cs2.Captions == nil || !cs2.Captions.Enabled || cs2.Output.Framing != editspec.FramingFollow {
		t.Fatalf("the latest cut follows the style: %+v", cs2)
	}
	if again := call[CutEditOut](t, a, "content.style.apply", ContentStyleApplyIn{CutEditCommon: CutEditCommon{ProjectID: client.ID}}); !again.NoChange {
		t.Fatalf("again: %+v", again)
	}
	// Claude's context says it.
	if ctxLine := StyleContext(ctx, a, client.ID); !strings.Contains(ctxLine, "House style from “fernbrook”'s settings") || !strings.Contains(ctxLine, "never animates") {
		t.Fatalf("Claude's context: %s", ctxLine)
	}

	// "Its own, starting as a copy" carries the house style along (a copy: changes stay apart).
	copied := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Copied"}).Project
	call[ContentShowOut](t, a, "content.own", ContentOwnIn{ProjectID: copied.ID, Copy: true})
	if st, owner, _ := content.StyleFor(ctx, a, copied.ID); st == nil || owner != copied.ID || st.Captions == nil || st.Captions.Size != "large" {
		t.Fatalf("a copy of the settings has the house style: %+v (%s)", st, owner)
	}
	// Cleared: new cuts get MetaRush's own again; apply says there's nothing to follow.
	cl := call[ContentStyleOut](t, a, "content.style.set", ContentStyleSetIn{Clear: []string{"all"}})
	if !cl.Empty || len(cl.LatestCuts) != 0 {
		t.Fatalf("cleared: %+v", cl)
	}
	if err := callErr(a, "content.style.apply", ContentStyleApplyIn{CutEditCommon: CutEditCommon{ProjectID: client.ID}}); apperr.From(err).Code != apperr.NotConfigured {
		t.Fatalf("no style to follow: %v", err)
	}
	_ = json.Valid
}

func TestHouseSignoffs(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "fernbrook"})
	client := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Client"}).Project
	third := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Third"}).Project
	as := talkAsset(t, a, client.ID)
	rev := firstCut(t, a, client.ID, as, false)
	call[SignoffsOut](t, a, "cut.signoffs.add", SignoffAddIn{SignoffsIn: SignoffsIn{ProjectID: client.ID}, Text: "Lena OKs the final version"})

	add := call[HouseSignoffsOut](t, a, "content.signoffs.add", HouseSignoffAddIn{Text: "Legal OKs it before export"})
	if len(add.Signoffs) != 1 || !strings.Contains(add.Summary, "Added the house sign-off “Legal OKs it before export” to “fernbrook”'s settings") ||
		!strings.Contains(add.Summary, "These are “fernbrook”'s settings, which new projects use.") {
		t.Fatalf("the default's house sign-off: %+v", add)
	}
	if again := call[HouseSignoffsOut](t, a, "content.signoffs.add", HouseSignoffAddIn{Text: "legal OKs it before export."}); len(again.Signoffs) != 1 || !strings.Contains(again.Summary, "already") {
		t.Fatalf("the same words again: %+v", again)
	}
	if err := callErr(a, "content.signoffs.add", HouseSignoffAddIn{ProjectID: client.ID, Text: "Sam OKs the music"}); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("a follower can't add one: %v", err)
	}
	// A cut's checklist: its own, then the house's.
	list := call[SignoffsOut](t, a, "cut.signoffs.list", SignoffsIn{ProjectID: client.ID})
	if len(list.Signoffs) != 2 || list.Signoffs[1].Text != "Legal OKs it before export" || !list.Signoffs[1].House || list.Signoffs[1].HouseOf != "fernbrook" || list.Open != 2 {
		t.Fatalf("Client's checklist: %+v", list)
	}
	// Approving reminds of both; it never locks.
	ok, err := CallTyped[RevisionSummary](ctx, a, "revision.approve", RevisionApproveIn{ID: rev.Revision.ID}, Caller{Human: true})
	if err != nil || len(ok.Waiting) != 2 || !strings.Contains(ok.Reminder, "“Legal OKs it before export”") {
		t.Fatalf("the reminder: %+v %v", ok, err)
	}
	// Ticked in Client, still waiting in Third.
	done := call[SignoffsOut](t, a, "cut.signoffs.done", SignoffDecideIn{SignoffsIn: SignoffsIn{ProjectID: client.ID}, Signoff: "Legal OKs it before export", Note: "by email"})
	if done.Open != 1 || !done.Signoffs[1].Done || done.Signoffs[1].DoneBy == nil || done.Signoffs[1].Note != "by email" || done.Signoffs[1].Version != rev.Revision.Number {
		t.Fatalf("ticked: %+v", done)
	}
	if tl := call[SignoffsOut](t, a, "cut.signoffs.list", SignoffsIn{ProjectID: third.ID}); tl.Open != 1 || tl.Signoffs[0].Done {
		t.Fatalf("another project's OK is its own: %+v", tl)
	}
	if ps := call[ProjectShowOut](t, a, "project.show", ProjectShowIn{ID: client.ID}); len(ps.HouseSignoffs) != 1 || !ps.HouseSignoffs[0].Done {
		t.Fatalf("project.show: %+v", ps.HouseSignoffs)
	}
	undo := call[SignoffsOut](t, a, "cut.signoffs.undo", SignoffDecideIn{SignoffsIn: SignoffsIn{ProjectID: client.ID}, Signoff: "2"})
	if undo.Open != 2 {
		t.Fatalf("unticked: %+v", undo)
	}
	// Taking it off a cut is refused: it goes from the settings.
	err = callErr(a, "cut.signoffs.remove", SignoffDecideIn{SignoffsIn: SignoffsIn{ProjectID: client.ID}, Signoff: "2"})
	if ae := apperr.From(err); ae.Code != apperr.Conflict || !strings.Contains(ae.Command(), "content signoffs remove") {
		t.Fatalf("a house sign-off off one cut: %v", err)
	}
	// A sign-off kept for every project using the settings is a house one; for the project, its own.
	kept := call[KeepAddOut](t, a, "keep.add", KeepAddIn{ProjectID: third.ID, Kind: models.KeepSignoff, Text: "Ana OKs the subtitles", Where: "settings"})
	if !strings.Contains(kept.Summary, "house sign-off") {
		t.Fatalf("kept for every project: %+v", kept)
	}
	if hs := call[HouseSignoffsOut](t, a, "content.signoffs.list", HouseSignoffsIn{ProjectID: third.ID}); len(hs.Signoffs) != 2 || !hs.ReadOnly {
		t.Fatalf("the house sign-offs: %+v", hs)
	}
	call[KeepAddOut](t, a, "keep.add", KeepAddIn{ProjectID: third.ID, Kind: models.KeepSignoff, Text: "Ola OKs the music"})
	if p := mustProject(t, a, third.ID); len(p.Signoffs) != 1 || p.Signoffs[0].Text != "Ola OKs the music" {
		t.Fatalf("kept for the project: %+v", p.Signoffs)
	}
	// Taken out of the settings: gone from every checklist, with the OKs given for it.
	call[SignoffsOut](t, a, "cut.signoffs.done", SignoffDecideIn{SignoffsIn: SignoffsIn{ProjectID: client.ID}, Signoff: "Legal OKs it before export"})
	rm := call[HouseSignoffsOut](t, a, "content.signoffs.remove", HouseSignoffRemoveIn{Signoff: "1"})
	if len(rm.Signoffs) != 1 || !strings.Contains(rm.Summary, "Took the house sign-off “Legal OKs it before export” out") {
		t.Fatalf("removed: %+v", rm)
	}
	if p := mustProject(t, a, client.ID); len(p.HouseSignoffs) != 0 {
		t.Fatalf("the OKs went with it: %+v", p.HouseSignoffs)
	}
	if l := call[SignoffsOut](t, a, "cut.signoffs.list", SignoffsIn{ProjectID: client.ID}); len(l.Signoffs) != 2 || l.Signoffs[1].Text != "Ana OKs the subtitles" {
		t.Fatalf("Client's checklist now: %+v", l.Signoffs)
	}
	cs := call[ContentShowOut](t, a, "content.show", ContentShowIn{ProjectID: client.ID})
	if p := partOf(cs, "signoffs"); p.Summary != "1 house sign-off" || p.Label != "House sign-offs" {
		t.Fatalf("the part: %+v", p)
	}
	// "Its own, starting as a copy" carries the house sign-offs along, as sign-offs of its own settings.
	fifth := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Fifth"}).Project
	call[ContentShowOut](t, a, "content.own", ContentOwnIn{ProjectID: fifth.ID, Copy: true})
	hs := call[HouseSignoffsOut](t, a, "content.signoffs.list", HouseSignoffsIn{ProjectID: fifth.ID})
	if hs.ProjectID != fifth.ID || hs.ReadOnly || len(hs.Signoffs) != 1 || hs.Signoffs[0].Text != "Ana OKs the subtitles" || hs.Signoffs[0].ID == rm.Signoffs[0].ID {
		t.Fatalf("a copy of the settings has the house sign-offs: %+v", hs)
	}
}
