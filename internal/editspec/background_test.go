package editspec

import (
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
)

// matteFacts adds clips' cut-outs to the fake facts.
type matteFacts struct {
	fakeFacts
	mattes map[string]MatteInfo
}

func (f matteFacts) Matte(sha string) (MatteInfo, bool) { m, ok := f.mattes[sha]; return m, ok }

var (
	matteA     = strings.Repeat("1a", 32)
	matteOther = strings.Repeat("2b", 32)
	matteSoon  = strings.Repeat("3c", 32)
	matteShort = strings.Repeat("4d", 32)
)

func withMattes() matteFacts {
	return matteFacts{fakeFacts: facts(), mattes: map[string]MatteInfo{
		matteA:     {AssetID: astA, From: 0, To: 30, Ready: true, Model: "Apple Vision"},
		matteOther: {AssetID: astB, From: 0, To: 10, Ready: true},
		matteSoon:  {AssetID: astA, From: 0, To: 30},
		matteShort: {AssetID: astA, From: 2, To: 3, Ready: true, Offset: 2},
	}}
}

func backgroundSpec(b *ClipBackground) *Spec {
	s := baseSpec()
	s.Clips[0].Background = b
	return s
}

func bgIssueCodes(is []Issue) []string {
	var out []string
	for _, i := range is {
		out = append(out, i.Severity+" "+i.Code+" "+i.Path)
	}
	return out
}

// What goes behind the people is checked in full, and so is the cut-out the
// cut plays: this clip's, made, covering the clip, from the media the cut
// plays (D510).
func TestBackgroundIssues(t *testing.T) {
	cases := []struct {
		name string
		b    *ClipBackground
		want string // "" = no issue
	}{
		{"colour", &ClipBackground{With: BackgroundColour, Colour: "#00b140", Matte: matteA}, ""},
		{"blur", &ClipBackground{With: BackgroundBlur, Matte: matteA}, ""},
		{"footage", &ClipBackground{With: BackgroundFootage, Footage: &BackgroundClip{AssetID: astB, In: 1}, Matte: matteA}, ""},
		{"bad colour", &ClipBackground{With: BackgroundColour, Colour: "green", Matte: matteA}, "error background_colour /clips/0/background/colour"},
		{"unknown kind", &ClipBackground{With: "sky", Matte: matteA}, "error background_with /clips/0/background/with"},
		{"no picture", &ClipBackground{With: BackgroundPicture, Matte: matteA}, "error background_picture /clips/0/background/picture"},
		{"no footage", &ClipBackground{With: BackgroundFootage, Matte: matteA}, "error background_footage /clips/0/background/footage"},
		{"footage elsewhere", &ClipBackground{With: BackgroundFootage, Footage: &BackgroundClip{AssetID: "ast_01jabcdefghjkmnpqrstvwxyz9"}, Matte: matteA}, "error missing_asset /clips/0/background/footage/assetId"},
		{"footage too short", &ClipBackground{With: BackgroundFootage, Footage: &BackgroundClip{AssetID: astB, In: 8}, Matte: matteA}, "error background_footage_short /clips/0/background/footage/in"},
		{"blur too strong", &ClipBackground{With: BackgroundBlur, Blur: 200, Matte: matteA}, "error background_blur /clips/0/background/blur"},
		{"edges too soft", &ClipBackground{With: BackgroundBlur, Feather: 41, Matte: matteA}, "error background_feather /clips/0/background/feather"},
		{"matte not a hash", &ClipBackground{With: BackgroundBlur, Matte: "abc"}, "error background_matte_bad /clips/0/background/matte"},
		{"matte unknown", &ClipBackground{With: BackgroundBlur, Matte: strings.Repeat("0f", 32)}, "error background_matte_missing /clips/0/background/matte"},
		{"another clip's", &ClipBackground{With: BackgroundBlur, Matte: matteOther}, "error background_matte_other /clips/0/background/matte"},
		{"not made yet", &ClipBackground{With: BackgroundBlur, Matte: matteSoon}, "error background_matte_missing /clips/0/background/matte"},
		{"too short", &ClipBackground{With: BackgroundBlur, Matte: matteShort}, "error background_matte_short /clips/0/background/matte"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bgIssueCodes(backgroundIssues(backgroundSpec(tc.b), wrapFacts(baseSpec(), withMattes())))
			switch {
			case tc.want == "" && len(got) != 0:
				t.Fatalf("unexpected: %v", got)
			case tc.want != "" && !slices.Contains(got, tc.want):
				t.Fatalf("want %q, got %v", tc.want, got)
			}
		})
	}
	// The whole check names it too, and its fix is the command.
	is := Validate(backgroundSpec(&ClipBackground{With: BackgroundBlur, Matte: matteShort}), withMattes(), nil, false)
	var found bool
	for _, i := range is {
		if i.Code == "background_matte_short" {
			found = true
			if !strings.Contains(i.Command, "metarush cut background") || !strings.Contains(i.Message, "0:02") {
				t.Fatalf("issue %+v", i)
			}
		}
	}
	if !found {
		t.Fatalf("Validate: %v", bgIssueCodes(is))
	}
	// A cut-out made from the original, while the cut plays a fixed copy: it
	// plays as saved, with a warning.
	s := backgroundSpec(&ClipBackground{With: BackgroundBlur, Matte: matteA})
	s.Fixed = map[string]FixedPin{astA: {SHA256: shaOne}}
	got := bgIssueCodes(backgroundIssues(s, withMattes()))
	if !slices.Contains(got, "warning background_matte_stale /clips/0/background/matte") {
		t.Fatalf("stale: %v", got)
	}
	// Facts that know no cut-outs check only the spec's own words.
	if got := bgIssueCodes(backgroundIssues(backgroundSpec(&ClipBackground{With: BackgroundBlur, Matte: matteSoon}), facts())); len(got) != 0 {
		t.Fatalf("no matte facts: %v", got)
	}
}

// Conversations and nested cuts have no footage of their own to cut people
// out of; and the wrappers that make them look like footage still answer
// for the cut's own cut-outs.
func TestBackgroundOnlyForFootage(t *testing.T) {
	s := baseSpec()
	s.Conversations = map[string]ConversationPin{"call": {ID: "cnv_01jabcdefghjkmnpqrstvwxyz0", Members: []ConversationMember{{ID: "a", Name: "A", AssetID: astA}}}}
	s.Clips = append(s.Clips, Clip{ID: "talk", AssetID: ConvKey("call"), In: 0, Out: 2, Background: &ClipBackground{With: BackgroundBlur, Matte: matteA}})
	got := bgIssueCodes(backgroundIssues(s, withMattes()))
	if !slices.Contains(got, "error background_not_footage /clips/3/background") {
		t.Fatalf("conversation: %v", got)
	}
	w := wrapFacts(s, withMattes())
	if _, ok := w.(*convFacts); !ok {
		t.Fatalf("wrapped facts are %T", w)
	}
	if info, ok := matteInfo(w, matteA); !ok || !info.Ready {
		t.Fatalf("through the conversation wrapper: %+v %v", info, ok)
	}
	nf := &nestFacts{base: withMattes()}
	if info, ok := nf.Matte(matteA); !ok || info.AssetID != astA {
		t.Fatalf("through the nested wrapper: %+v %v", info, ok)
	}
	if _, ok := matteInfo(nil, matteA); ok {
		t.Fatal("no facts know no cut-outs")
	}
}

// The plan gives the renderer the background in its own pixels and the
// cut-out with where it starts against the clip's trim (a fraction of a
// frame when it starts between the cut's frames).
func TestPlanBackground(t *testing.T) {
	s := baseSpec() // vertical 1080x1920 at 30
	s.Clips[0].Background = &ClipBackground{With: BackgroundColour, Colour: "#00b140", Matte: matteShort}
	s.Clips[0].In, s.Clips[0].Out = 2.5, 3
	s.Clips[1].Background = &ClipBackground{With: BackgroundFootage, Footage: &BackgroundClip{AssetID: astA, In: 1.5}, Feather: 4, Matte: matteOther}
	s.Clips[2].Background = &ClipBackground{With: BackgroundBlur, Matte: matteA}
	plan := Resolve(s, withMattes(), "", "", "")
	c0, c1, c2 := plan.Clips[0].Background, plan.Clips[1].Background, plan.Clips[2].Background
	if c0 == nil || c1 == nil || c2 == nil {
		t.Fatalf("backgrounds %+v %+v %+v", c0, c1, c2)
	}
	if c0.With != BackgroundColour || c0.Colour != "#00b140" || c0.Feather != DefaultFeather || c0.CutOut != matteShort {
		t.Fatalf("colour %+v", c0)
	}
	// The clip starts 2.5 s in (frame 75); its cut-out's first frame shows at 2 s.
	if math.Abs(c0.CutOutTrimBefore-15) > 1e-9 || plan.Clips[0].TrimBefore != 75 {
		t.Fatalf("cut-out trim %v (clip %d)", c0.CutOutTrimBefore, plan.Clips[0].TrimBefore)
	}
	if f := c1.Footage; f == nil || f.AssetID != astA || f.TrimBefore != 45 || f.SourceWidth != 1920 || f.SourceHeight != 1080 || c1.Feather != 4 {
		t.Fatalf("footage %+v %+v", c1, f)
	}
	if c2.Blur != DefaultBlur || c2.CutOutTrimBefore != float64(plan.Clips[2].TrimBefore) {
		t.Fatalf("blur %+v", c2)
	}
	if len(plan.CutOutSources) != 3 {
		t.Fatalf("cut-out sources %v", plan.CutOutSources)
	}
	if ids := PlanSourceIDs(plan); !slices.Contains(ids, astA) {
		t.Fatalf("sources %v", ids)
	}
	// Sizes follow the cut's short side: a 720p cut's are two thirds.
	s.Output = Output{Width: 1280, Height: 720, FPS: 30}
	plan = Resolve(s, withMattes(), "", "", "")
	if got := plan.Clips[2].Background; math.Abs(got.Blur-DefaultBlur*2/3) > 1e-3 || math.Abs(got.Feather-DefaultFeather*2/3) > 1e-3 {
		t.Fatalf("720p sizes %+v", got)
	}
	if got := PlanCutOuts(plan); len(got) != 3 || !slices.Contains(got, matteA) {
		t.Fatalf("plan cut-outs %v", got)
	}
	// A cut-out not made yet, or one MetaRush doesn't have, stays pinned and
	// says it is missing: a version keeps what it was saved with, so a render
	// fails for it rather than playing the clip as recorded (the check
	// refuses saving such a cut). So it is without cut-out facts at all.
	for name, plan := range map[string]*RenderPlan{
		"not made": func() *RenderPlan {
			s.Clips[2].Background.Matte = matteSoon
			return Resolve(s, withMattes(), "", "", "")
		}(),
		"gone": func() *RenderPlan {
			s.Clips[2].Background.Matte = strings.Repeat("e", 64)
			return Resolve(s, withMattes(), "", "", "")
		}(),
		"no facts": Resolve(s, facts(), "", "", ""),
	} {
		b := plan.Clips[2].Background
		if b == nil || !b.Missing || b.CutOut != s.Clips[2].Background.Matte && name != "not made" || b.With != BackgroundBlur {
			t.Fatalf("%s: %+v", name, b)
		}
		if got := PlanMissingCutOuts(plan); !slices.Contains(got, b.CutOut) || slices.Contains(got, matteA) {
			t.Fatalf("%s: missing %v", name, got)
		}
		if _, listed := plan.CutOutSources[b.CutOut]; !listed {
			t.Fatalf("%s: the plan names it for the caller: %v", name, plan.CutOutSources)
		}
	}
	if Resolve(s, withMattes(), "", "", "").Clips[0].Background.Missing {
		t.Fatal("a cut-out MetaRush has isn't missing")
	}
	// Without a background the plan says nothing new (older plans stay the same).
	b, _ := json.Marshal(Resolve(baseSpec(), withMattes(), "", "", ""))
	if strings.Contains(string(b), "background\":{") || strings.Contains(string(b), "cutOutSources") {
		t.Fatalf("plan without backgrounds: %s", b)
	}
}

// A picture behind the people is one of the cut's pinned resources: listed
// where the cut uses resources, so it is checked and served like the others.
func TestBackgroundPictureIsAResourceUse(t *testing.T) {
	s := slideSpec()
	s.Clips[0].Background = &ClipBackground{With: BackgroundPicture, Picture: "homepage#2", Matte: matteA}
	var use *ResourceUse
	for _, u := range ResourceUses(s, slideFacts{}) {
		if u.Background == "a" {
			use = &u
		}
	}
	if use == nil || use.Name != "homepage" || use.Page != 2 || use.Path != "/clips/0/background/picture" {
		t.Fatalf("use %+v", use)
	}
	plan := Resolve(s, slideFacts{}, "", "", "")
	if !slices.Contains(ResourceFiles(plan, s, slideFacts{}), selftestSitePhone) {
		t.Fatalf("the page's picture isn't among the files: %v", ResourceFiles(plan, s, slideFacts{}))
	}
	// A name the cut doesn't pin is the resource check's to refuse.
	s.Clips[0].Background.Picture = "nope"
	if got := codesOf(validateResources(s, slideFacts{})); len(got) == 0 {
		t.Fatal("an unpinned picture passed")
	}
	// Clips shown behind people are footage the cut plays.
	f := baseSpec()
	f.Clips[0].Background = &ClipBackground{With: BackgroundFootage, Footage: &BackgroundClip{AssetID: "ast_01jabcdefghjkmnpqrstvwxyz5"}, Matte: matteA}
	if !slices.Contains(AssetIDs(f), "ast_01jabcdefghjkmnpqrstvwxyz5") || !slices.Equal(MatteSHAs(f), []string{matteA}) {
		t.Fatalf("asset ids %v / mattes %v", AssetIDs(f), MatteSHAs(f))
	}
}
