package editspec

import (
	"encoding/json"
	"strings"
	"testing"
)

// motionFacts adds checked motion graphics to the asset facts.
type motionFacts struct {
	fakeFacts
	motion map[string]MotionInfo
}

func (m motionFacts) Motion(h string) (MotionInfo, bool) { i, ok := m.motion[h]; return i, ok }

var (
	mapHash  = strings.Repeat("3f", 32)
	logoHash = strings.Repeat("9b", 32)
	newHash  = strings.Repeat("1c", 32)
)

const routeSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"from":{"type":"string","maxLength":40},"to":{"type":"string","maxLength":40}},"required":["from","to"],"additionalProperties":false}`

func withMotion(state string) motionFacts {
	return motionFacts{fakeFacts: facts(), motion: map[string]MotionInfo{
		mapHash:  {Name: "route-map", Hash: mapHash, State: state, Region: "center", MinDuration: 2, MaxDuration: 6, Schema: json.RawMessage(routeSchema)},
		logoHash: {Name: "logo-sting", Hash: logoHash, State: MotionApproved, Region: "lower", MinDuration: 1, MaxDuration: 4, Schema: json.RawMessage(`{"type":"object","additionalProperties":false}`)},
	}}
}

func codes(is []Issue, sev string) map[string]Issue {
	out := map[string]Issue{}
	for _, i := range is {
		if sev == "" || i.Severity == sev {
			out[i.Code] = i
		}
	}
	return out
}

func routeSpec(params string) *Spec {
	s := baseSpec()
	s.Overlays = []Overlay{{ID: "route", Motion: &MotionRef{Name: "route-map", Hash: mapHash}, At: 1, Duration: 4, Params: json.RawMessage(params)}}
	return s
}

func TestMotionOverlayValidation(t *testing.T) {
	// Approved, valid parameters: no errors.
	if errs := codes(Validate(routeSpec(`{"from":"Lisbon","to":"Porto"}`), withMotion(MotionApproved), nil, false), "error"); len(errs) > 0 {
		t.Fatalf("approved motion graphic: %v", errs)
	}
	// Parameters are checked against the schema its check recorded.
	is := Validate(routeSpec(`{"from":"Lisbon","too":"Porto"}`), withMotion(MotionApproved), nil, false)
	if e, ok := codes(is, "error")["schema"]; !ok || !strings.HasPrefix(e.Path, "/overlays/0/params") {
		t.Fatalf("params against the component's schema: %v", is)
	}
	// Not approved yet / withdrawn / turned down: an error that names the approval step.
	for state, want := range map[string]string{MotionWaiting: "Nobody has approved", MotionRevoked: "withdrawn", MotionRejected: "turned down"} {
		e, ok := codes(Validate(routeSpec(`{"from":"a","to":"b"}`), withMotion(state), nil, false), "error")["motion_not_approved"]
		if !ok || !strings.Contains(e.Message, want) || e.Path != "/overlays/0/motion" {
			t.Errorf("%s: %+v", state, e)
		}
	}
	// It says where a person approves it, in plain words (the command is in the skills).
	if m := codes(Validate(routeSpec(`{}`), withMotion(MotionWaiting), nil, false), "")["motion_not_approved"].Message; !strings.Contains(m, "under Motion graphics in the dashboard") ||
		!strings.Contains(m, "route-map (version 3f3f3f3f)") || strings.Contains(m, "metarush") {
		t.Errorf("the message says where to approve it: %q", m)
	}
	// Never checked in this workspace (or facts without motion graphics at all).
	s := routeSpec(`{}`)
	s.Overlays[0].Motion.Hash = newHash
	if _, ok := codes(Validate(s, withMotion(MotionApproved), nil, false), "error")["motion_unknown"]; !ok {
		t.Error("unknown version")
	}
	if _, ok := codes(Validate(routeSpec(`{}`), facts(), nil, false), "error")["motion_unknown"]; !ok {
		t.Error("facts without motion graphics")
	}
	// Both a template and a motion graphic.
	both := routeSpec(`{}`)
	both.Overlays[0].Template = TplTitleCard
	if _, ok := codes(Validate(both, withMotion(MotionApproved), nil, false), "error")["overlay_kind"]; !ok {
		t.Error("template and motion together")
	}
	// Duration hints come from its meta.
	long := routeSpec(`{"from":"a","to":"b"}`)
	long.Overlays[0].Duration = 9
	if _, ok := codes(Validate(long, withMotion(MotionApproved), nil, false), "warning")["overlay_long"]; !ok {
		t.Error("longer than meta.maxDuration")
	}
}

// Regions from the check take part in collision checks exactly like templates.
func TestMotionRegionsCollide(t *testing.T) {
	s := baseSpec()
	s.Captions = &Captions{Enabled: true, Position: "lower"}
	s.Overlays = []Overlay{
		{ID: "logo", Motion: &MotionRef{Name: "logo-sting", Hash: logoHash}, At: 0.5, Duration: 3, Params: json.RawMessage(`{}`)},
		{ID: "who", Template: TplLowerThirdClean, At: 1, Duration: 3, Params: json.RawMessage(`{"name":"Ada"}`)},
	}
	w := codes(Validate(s, withMotion(MotionApproved), nil, false), "warning")
	if !strings.Contains(w["overlay_collision"].Message, "logo and who") {
		t.Errorf("lower motion graphic vs lower third: %v", w)
	}
	if !strings.Contains(w["caption_collision"].Message, "logo") && !strings.Contains(w["caption_collision"].Message, "who") {
		t.Errorf("caption collision: %v", w)
	}
}

func TestMotionInThePlan(t *testing.T) {
	s := routeSpec(`{"from":"Lisbon","to":"Porto"}`)
	p := Resolve(s, withMotion(MotionApproved), "rev", "prj", "h")
	o := p.Overlays[0]
	if o.Motion == nil || o.Motion.Hash != mapHash || o.Motion.Region != "center" || o.Template != "" || o.TemplateVersion != "" {
		t.Fatalf("plan overlay %+v", o)
	}
	if string(o.Params) != `{"from":"Lisbon","to":"Porto"}` {
		t.Fatalf("params %s", o.Params)
	}
	if got := MotionHashes(s); len(got) != 1 || got[0] != mapHash {
		t.Fatalf("hashes %v", got)
	}
	// Templates are unchanged by the new field (old plans and specs hash the same).
	b, _ := json.Marshal(Overlay{ID: "who", Template: TplTitleCard, At: 1, Duration: 2, Params: json.RawMessage(`{}`)})
	if strings.Contains(string(b), "motion") || !strings.Contains(string(b), `"template":"title.card"`) {
		t.Fatalf("template overlay JSON %s", b)
	}
}

// A parameter schema is data: references to files or URLs are never loaded.
func TestMotionSchemaLoadsNothing(t *testing.T) {
	info := MotionInfo{Name: "x", Hash: newHash, Schema: json.RawMessage(`{"$ref":"file:///etc/passwd"}`)}
	is := ValidateMotionParams(info, json.RawMessage(`{}`), "/overlays/0/params")
	if len(is) == 0 || is[0].Code != "motion_schema" || !strings.Contains(is[0].Message, "can't be used") {
		t.Fatalf("external $ref: %v", is)
	}
}
