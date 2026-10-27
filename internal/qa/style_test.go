package qa

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// The house style (D199) in the quality check: captions missing when it says
// they're on, captions in another look (a note, D245), a name bar that
// animates when it says never, a name bar taller than it says — each with the
// change that fixes it; nothing when the version follows it, and the check
// skipped when there's nothing it can measure.
func TestHouseStyleCheck(t *testing.T) {
	on := true
	style := &editspec.HouseStyle{
		Captions: &editspec.StyleCaptions{On: &on, Look: editspec.CaptionBoxed, Size: editspec.CaptionLarge, Position: "bottom"},
		NameBar:  &editspec.StyleNameBar{Height: editspec.NameBarHalf, Animate: editspec.NameBarStill},
	}
	spec := &editspec.Spec{Output: editspec.Output{Width: 1920, Height: 1080, FPS: 30}, Overlays: []editspec.Overlay{
		{ID: "names", Template: editspec.TplNameBar, Span: &editspec.OverlaySpan{}, Params: json.RawMessage(`{"name":"Ada","animate":"slide"}`)},
	}}
	newRun := func(s *editspec.Spec, h *editspec.HouseStyle) *run {
		return &run{in: Input{Spec: s, Style: h, StyleOf: "fernbrook"}, fps: 30, rep: &models.QAReport{}, plan: &editspec.RenderPlan{FPS: 30, DurationInFrames: 300}}
	}
	r := newRun(spec, style)
	r.houseStyle()
	if len(r.pend) != 2 {
		t.Fatalf("findings: %+v", r.pend)
	}
	caps, bar := r.pend[0], r.pend[1]
	if caps.Kind != "style_captions" || caps.Severity != models.QAWarning || !strings.Contains(caps.Message, "“fernbrook”'s settings has captions on") ||
		caps.Fix == nil || len(caps.Fix.Ops) != 1 || caps.Fix.Ops[0].Captions == nil || caps.Fix.Ops[0].Captions.Style != editspec.CaptionBoxed || caps.Fix.Ops[0].Captions.Size != editspec.CaptionLarge {
		t.Errorf("captions finding: %+v %+v", caps, caps.Fix)
	}
	if bar.Kind != "style_name_bar" || !strings.Contains(bar.Message, "slides names in although the house style says it never animates") ||
		!strings.Contains(bar.Message, "taller than the house style's half height") || bar.From != 0 || bar.To != 10 || len(bar.Fix.Ops) != 1 {
		t.Errorf("name bar finding: %+v", bar)
	}
	var set struct {
		Params editspec.NameBarParams `json:"params"`
	}
	_ = json.Unmarshal(bar.Fix.Ops[0].Set, &set)
	if set.Params.Animate != editspec.NameBarStill || set.Params.Height != editspec.NameBarHalf || set.Params.Name != "Ada" {
		t.Errorf("the fix: %s", bar.Fix.Ops[0].Set)
	}
	if got := r.rep.Checks[0]; got.Name != "house_style" || got.Status != "warning" {
		t.Errorf("check: %+v", got)
	}

	// Captions in another look, size and place are a note (a cut may choose
	// its own, D245) with the change that follows the house style.
	other := &editspec.Spec{Output: spec.Output, Captions: &editspec.Captions{Enabled: true, Style: editspec.CaptionClean, MaxWordsPerLine: 6}}
	r = newRun(other, &editspec.HouseStyle{Captions: style.Captions})
	r.houseStyle()
	if len(r.pend) != 1 || r.pend[0].Kind != "style_caption_look" || r.pend[0].Severity != models.QAInfo || r.rep.Checks[0].Status != "pass" ||
		!strings.Contains(r.pend[0].Message, "they look “Clean” where the house style has “Boxed”") || !strings.Contains(r.pend[0].Message, "at the very bottom") {
		t.Fatalf("another look: %+v %+v", r.pend, r.rep.Checks)
	}
	if nc := r.pend[0].Fix.Ops[0].Captions; nc.Style != editspec.CaptionBoxed || nc.Size != editspec.CaptionLarge || nc.Position != "bottom" || !nc.Enabled {
		t.Errorf("the change: %+v", nc)
	}
	// A version that follows it passes.
	fine := &editspec.Spec{Output: spec.Output, Captions: &editspec.Captions{Enabled: true, Style: editspec.CaptionBoxed, Size: editspec.CaptionLarge, Position: "bottom"}, Overlays: []editspec.Overlay{
		{ID: "names", Template: editspec.TplNameBar, Span: &editspec.OverlaySpan{}, Params: json.RawMessage(`{"name":"Ada","animate":"never","height":"half"}`)},
	}}
	r = newRun(fine, style)
	r.houseStyle()
	if len(r.pend) != 0 || r.rep.Checks[0].Status != "pass" {
		t.Errorf("a version that follows the style: %+v %+v", r.pend, r.rep.Checks)
	}
	// Locked captions: the finding says to unlock them, with no change.
	locked := &editspec.Spec{Output: spec.Output, Captions: &editspec.Captions{Locked: true}}
	r = newRun(locked, &editspec.HouseStyle{Captions: style.Captions})
	r.houseStyle()
	if len(r.pend) != 1 || len(r.pend[0].Fix.Ops) != 0 || !strings.Contains(r.pend[0].Fix.Summary, "unlock") {
		t.Errorf("locked captions: %+v", r.pend)
	}
	// Nothing to measure, or no style: skipped.
	for _, h := range []*editspec.HouseStyle{nil, {Framing: editspec.FramingFollow}} {
		r = newRun(spec, h)
		r.houseStyle()
		if len(r.pend) != 0 || r.rep.Checks[0].Status != "skipped" {
			t.Errorf("style %+v: %+v %+v", h, r.pend, r.rep.Checks)
		}
	}
}

// The layout pack in the house style check (D267): a role the pack doesn't
// have and a graphic that doesn't follow the pack are notes with their fix;
// a cut whose graphics name roles is checked even without a house style.
func TestHousePackNotes(t *testing.T) {
	spec := &editspec.Spec{Output: editspec.Output{Width: 1920, Height: 1080, FPS: 30}, Overlays: []editspec.Overlay{
		{ID: "intro", Role: editspec.PackTitleCard, Template: editspec.TplTitleCard, At: 0, Duration: 3, Params: json.RawMessage(`{"title":"Hi"}`)},
		{ID: "ada", Role: editspec.PackLowerThird, Template: editspec.TplLowerThirdClean, At: 4, Duration: 3, Params: json.RawMessage(`{"name":"Ada","align":"left"}`)},
		{ID: "q", Role: editspec.PackQuoteCard, Template: editspec.TplSlideQuote, At: 8, Duration: 1, Params: json.RawMessage(`{"text":"Yes"}`), Locked: true},
	}}
	own := &editspec.LayoutPack{Pieces: map[string]editspec.PackPiece{
		editspec.PackTitleCard:  {Template: editspec.TplSlideTitle},
		editspec.PackLowerThird: {Template: editspec.TplLowerThirdClean, Fields: json.RawMessage(`{"align":"right"}`)},
	}}
	drifts := editspec.PackDrifts(spec, editspec.Packs{Own: own, Starter: editspec.StarterPack(nil), Of: "fernbrook"}, nil)
	r := &run{in: Input{Spec: spec, PackDrifts: drifts, PackOf: "fernbrook"}, fps: 30, rep: &models.QAReport{}, plan: &editspec.RenderPlan{FPS: 30, DurationInFrames: 300}}
	r.houseStyle()
	if len(r.pend) != 3 {
		t.Fatalf("findings: %+v", r.pend)
	}
	byID := map[string]*models.QAFinding{}
	for _, f := range r.pend {
		byID[f.OverlayIDs[0]] = f
		if f.Severity != models.QAInfo {
			t.Errorf("a cut may choose its own: notes only, %+v", f)
		}
	}
	if f := byID["intro"]; f.Kind != "pack_override" || !strings.Contains(f.Message, "the title card here is drawn with another graphic than the pack's") || len(f.Fix.Ops) != 1 {
		t.Errorf("intro: %+v", f)
	}
	if f := byID["ada"]; f.Kind != "pack_override" || !strings.Contains(f.Message, "the lower third here changes how the pack draws it (align)") || !strings.Contains(f.Message, "“fernbrook”'s settings") {
		t.Errorf("ada: %+v", f)
	}
	if f := byID["q"]; f.Kind != "pack_role" || !strings.Contains(f.Message, "has no quote card") {
		t.Errorf("q: %+v", f)
	}
	if c := r.rep.Checks[0]; c.Name != "house_style" || c.Status != "pass" {
		t.Errorf("notes don't fail or warn: %+v", c)
	}
	// Roles that follow the pack: checked, nothing noted.
	r = &run{in: Input{Spec: spec}, fps: 30, rep: &models.QAReport{}, plan: &editspec.RenderPlan{FPS: 30, DurationInFrames: 300}}
	r.houseStyle()
	if len(r.pend) != 0 || r.rep.Checks[0].Status != "pass" {
		t.Errorf("a cut that follows the pack: %+v %+v", r.pend, r.rep.Checks)
	}
}
