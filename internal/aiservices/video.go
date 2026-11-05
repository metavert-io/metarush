package aiservices

// Background removal for video (D505): a matte of a clip, or of a part of it —
// for each of its frames, how much of each pixel is a person — that MetaRush
// keeps once and uses to put something else behind the people in a cut. It
// changes the footage a person already has without making anything new, so it
// is declared as picture background removal is (KindInfo.Edit: an edit,
// enhanced, never AI-generated; D511).
//
// The request is provider-neutral: one footage input (RoleClip, a video —
// for a model on this computer the clip's own file and the stretch to read,
// Media.From/To; for a service elsewhere an MP4 of the part, picture only),
// no words. The answer is the matte as a video of the same frames at the same
// rate (OutputMatte), whose brightness is how much of each pixel is a person.
// Models on this computer report how far along they are through the Env
// (Env.Progress) and stop when the context ends; a service elsewhere is
// polled like any other.
const (
	// KindVideoBackground tells the people in footage from their background.
	KindVideoBackground = "video-background"
	// TaskMatte makes the matte.
	TaskMatte = "matte"
	// OutputMatte is a matte: a video the length of the footage sent, frame
	// for frame, its brightness how much of each pixel is a person.
	OutputMatte = "matte"
)

func init() {
	kinds = append(kinds, KindInfo{Kind: KindVideoBackground, Title: "Background removal for video", What: "a clip's cut-out",
		Output: OutputMatte, Tasks: []string{TaskMatte}, Reuse: true, Edit: true})
}

// FootageSeconds is how long the footage a request sends is (its stretch
// when it names one).
func (r *Request) FootageSeconds() float64 {
	total := 0.0
	for _, x := range r.InputsOf(RoleClip) {
		total += x.Seconds
	}
	return total
}
