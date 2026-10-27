package editspec

import (
	"strings"
	"testing"
)

// A clip MetaRush rendered from a Remotion source plays only while its
// version is approved (D136); other footage is never affected.
func TestRemotionClipsNeedAnApprovedVersion(t *testing.T) {
	f := facts()
	a := f.assets[astB]
	a.Remotion = &RemotionFacts{SourceTitle: "Teaser", Composition: "Intro", VersionNumber: 3, State: MotionApproved}
	f.assets[astB] = a
	s := baseSpec()
	for _, is := range Validate(s, f, nil, false) {
		if is.Code == "remotion_not_approved" {
			t.Fatalf("approved: %v", is)
		}
	}
	a.Remotion.State = MotionRevoked
	f.assets[astB] = a
	var hit *Issue
	for _, is := range Validate(s, f, nil, false) {
		if is.Code == "remotion_not_approved" {
			x := is
			hit = &x
		}
	}
	if hit == nil || hit.Severity != "error" || hit.Path != "/clips/1/assetId" || !strings.Contains(hit.Message, "version 3 of the Remotion project “Teaser”") ||
		!strings.Contains(hit.Message, "its approval was withdrawn") {
		t.Fatalf("withdrawn: %+v", hit)
	}
}
