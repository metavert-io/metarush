package sound

import (
	"math"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

const (
	convJ = "ast_01jabcdefghjkmnpqrstvwxyj0"
	convS = "ast_01jabcdefghjkmnpqrstvwxys0"
)

type convFacts struct {
	assets map[string]editspec.AssetFacts
	tx     map[string][]editspec.TranscriptSegment
}

func (f convFacts) Asset(id string) (editspec.AssetFacts, bool) { a, ok := f.assets[id]; return a, ok }
func (f convFacts) Transcript(id string) []editspec.TranscriptSegment {
	return f.tx[id]
}

// A conversation's members are the people Sound lists: each recording is one
// person's microphone, so every stretch of it carries their name (whatever the
// speaker detection called it), and the stretch Sound works on is the one the
// cut plays from that recording.
func TestConversationMembersAreSoundsPeople(t *testing.T) {
	asset := func(id, name string) editspec.AssetFacts {
		return editspec.AssetFacts{ID: id, Name: name, Kind: "video", State: "available", InProject: true, Duration: 20,
			HasVideo: true, HasAudio: true, Width: 1280, Height: 720, FPS: 30, TranscriptPrecision: "word"}
	}
	f := convFacts{
		assets: map[string]editspec.AssetFacts{convJ: asset(convJ, "maya.mp4"), convS: asset(convS, "sam.mp4")},
		tx: map[string][]editspec.TranscriptSegment{
			convJ: {{From: 1, To: 2.5, Text: "So tell me", Speaker: "speaker_0",
				Words: []editspec.TranscriptWord{{Text: "So", From: 1, To: 1.3}, {Text: "tell", From: 1.4, To: 1.8}, {Text: "me", From: 1.9, To: 2.5}}}},
			convS: {{From: 2.5, To: 4.5, Text: "We built it", Speaker: "Speaker 1",
				Words: []editspec.TranscriptWord{{Text: "We", From: 2.5, To: 2.9}, {Text: "built", From: 3, To: 3.6}, {Text: "it", From: 3.7, To: 4.5}}}},
		},
	}
	s := &editspec.Spec{Output: editspec.Output{Preset: editspec.PresetLandscape},
		Clips: []editspec.Clip{{ID: "talk", AssetID: editspec.ConvKey("call"), In: 0, Out: 8,
			Shots: []editspec.Shot{{At: 0, Member: "maya"}, {At: 3, Member: "sam"}}}},
		Conversations: map[string]editspec.ConversationPin{"call": {ID: "cnv_01jabcdefghjkmnpqrstvwxyz0", Members: []editspec.ConversationMember{
			{ID: "maya", Name: "Maya", AssetID: convJ},
			{ID: "sam", Name: "Sam", AssetID: convS, Start: 0.5},
		}}},
	}
	editspec.Normalize(s)
	if is := editspec.Validate(s, f, nil, false); editspec.HasErrors(is) {
		t.Fatalf("unexpected problems: %v", is)
	}
	plan := editspec.Resolve(editspec.Clone(s), f, "rev_x", "prj_x", editspec.Hash(s))
	c := &cut{spec: s, facts: f, plan: plan, members: memberVoices(s)}

	order, secs := c.speakers()
	if len(order) != 2 || order[0] != "Maya" || order[1] != "Sam" {
		t.Fatalf("people heard: %v", order)
	}
	// Each microphone is its person's for the whole stretch the cut plays.
	if math.Abs(secs["Maya"]-8) > 0.05 || math.Abs(secs["Sam"]-7.5) > 0.05 {
		t.Fatalf("speaking time: %v", secs)
	}
	var sam *use
	for _, u := range c.uses() {
		if u.asset == editspec.ConvKey("call") {
			t.Fatalf("the conversation itself is not a recording: %+v", u)
		}
		if u.asset == convS {
			u := u
			sam = &u
		}
	}
	// Sam started recording half a second into the call, so the cut's 8
	// seconds play 0–7.5 of Sam's file.
	if sam == nil || math.Abs(sam.from) > 0.04 || math.Abs(sam.to-7.5) > 0.04 {
		t.Fatalf("Sam's stretch: %+v", sam)
	}
}
