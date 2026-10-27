package editspec

import (
	"encoding/json"
	"strings"
	"testing"
)

// slideFacts: the self-test's footage and resources, plus a repository and a
// motion graphic whose code draws "logo" by name (D107).
type slideFacts struct{ selftestFacts }

var (
	slideRepoSHA   = strings.Repeat("c9", 32)
	slideMotionSHA = strings.Repeat("9c", 32)
)

func (f slideFacts) Resource(id, sha string) (ResourceInfo, bool) {
	if id == "res_repoaaaaaaaaaaaaaaaaaaaaaaaa" {
		return ResourceInfo{ID: id, Name: "widget", Title: "acme/widget", Kind: ResourceKindRepo, SHA256: slideRepoSHA, Known: sha == slideRepoSHA, Visible: true, Available: true}, true
	}
	return f.selftestFacts.Resource(id, sha)
}

func (slideFacts) Motion(hash string) (MotionInfo, bool) {
	if hash != slideMotionSHA {
		return MotionInfo{}, false
	}
	return MotionInfo{Name: "card-reveal", Hash: hash, State: MotionApproved, Region: "full", MinDuration: 1, MaxDuration: 8,
		Schema: json.RawMessage(`{"type":"object","properties":{"shot":{"type":"string","format":"metarush-resource"}}}`), Resources: []string{"logo"}}, true
}

func slideSpec(overlays ...Overlay) *Spec {
	s := &Spec{Output: Output{Width: 1280, Height: 720, FPS: 30, Platform: "youtube"}, Clips: []Clip{{ID: "a", AssetID: "blue", In: 0.5, Out: 5.5}},
		Resources: map[string]ResourcePin{
			"logo":     {ID: "res_selftestlogozzzzzzzzzzzzzz", SHA256: selftestLogoSHA},
			"homepage": {ID: "res_selftestsiteaaaaaaaaaaaaaa", SHA256: selftestSiteSHA},
			"widget":   {ID: "res_repoaaaaaaaaaaaaaaaaaaaaaaaa", SHA256: slideRepoSHA},
		},
		Overlays: overlays}
	Normalize(s)
	return s
}

func codesOf(is []Issue) []string {
	var out []string
	for _, i := range is {
		if i.Severity == "error" {
			out = append(out, i.Code+" "+i.Path)
		}
	}
	return out
}

func TestSlidesAndPageSnapshots(t *testing.T) {
	ok := slideSpec(
		Overlay{ID: "phone", Template: TplSlideScreenshot, At: 0, Duration: 2, Params: json.RawMessage(`{"image":"homepage#2","frame":"phone"}`)},
		Overlay{ID: "bullets", Template: TplSlideBullets, At: 2, Duration: 3, Params: json.RawMessage(`{"title":"Why","bullets":["Fast","Local"]}`)},
	)
	if errs := codesOf(Validate(ok, slideFacts{}, nil, false)); len(errs) != 0 {
		t.Fatalf("valid slides: %v", errs)
	}
	plan := Resolve(Clone(ok), slideFacts{}, "rev", "prj", Hash(ok))
	files := ResourceFiles(plan, ok, slideFacts{})
	if len(files) != 1 || files[0] != selftestSitePhone || plan.Resources["homepage"].URL != "https://acme.example/" {
		t.Fatalf("the phone picture is the file drawn, and the page's address travels with it: %v %+v", files, plan.Resources["homepage"])
	}
	for name, c := range map[string]struct {
		o    Overlay
		want string
	}{
		"no #4 on a snapshot":       {Overlay{ID: "x", Template: TplSlideScreenshot, At: 0, Duration: 2, Params: json.RawMessage(`{"image":"homepage#4"}`)}, "resource_page"},
		"a repository isn't drawn":  {Overlay{ID: "x", Template: TplSlideCode, At: 0, Duration: 3, Params: json.RawMessage(`{"code":"widget"}`)}, "resource_kind"},
		"a bullet slide needs some": {Overlay{ID: "x", Template: TplSlideBullets, At: 0, Duration: 3, Params: json.RawMessage(`{"title":"Why","bullets":[]}`)}, "schema"},
		"seven bullets are too many": {Overlay{ID: "x", Template: TplSlideBullets, At: 0, Duration: 3,
			Params: json.RawMessage(`{"title":"Why","bullets":["a","b","c","d","e","f","g"]}`)}, "schema"},
	} {
		errs := codesOf(Validate(slideSpec(c.o), slideFacts{}, nil, false))
		if len(errs) == 0 || !strings.Contains(strings.Join(errs, ","), c.want) {
			t.Errorf("%s: %v", name, errs)
		}
	}
}

// A motion graphic's code that draws "logo" by name makes the cut pin it.
func TestMotionResourcesArePinned(t *testing.T) {
	m := Overlay{ID: "card", Motion: &MotionRef{Name: "card-reveal", Hash: slideMotionSHA}, At: 0, Duration: 3, Params: json.RawMessage(`{"shot":"homepage"}`)}
	s := slideSpec(m)
	if errs := codesOf(Validate(s, slideFacts{}, nil, false)); len(errs) != 0 {
		t.Fatalf("pinned: %v", errs)
	}
	plan := Resolve(Clone(s), slideFacts{}, "rev", "prj", Hash(s))
	files := ResourceFiles(plan, s, slideFacts{})
	if strings.Join(files, ",") != selftestSiteSHA+","+selftestLogoSHA && strings.Join(files, ",") != selftestLogoSHA+","+selftestSiteSHA {
		t.Fatalf("the plan serves the parameter's picture and the name the code uses: %v", files)
	}
	delete(s.Resources, "logo")
	errs := codesOf(Validate(s, slideFacts{}, nil, false))
	if len(errs) != 1 || !strings.HasPrefix(errs[0], "resource_unpinned /overlays/0/motion") {
		t.Fatalf("unpinned: %v", errs)
	}
}
