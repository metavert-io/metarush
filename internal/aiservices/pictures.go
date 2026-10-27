package aiservices

// Kinds of work on what a person already has (D279): a picture's background
// taken away, a picture made larger, a clip's lips matched to new sound. Both take one picture and make one picture;
// models for them run on this computer first (D278), and any provider or
// ability may add its own through the same contract.
const (
	// KindBackground takes a picture's background away, keeping its subject
	// (a PNG whose background is see-through).
	KindBackground = "background-removal"
	// KindUpscale makes a picture larger, with detail a model fills in.
	KindUpscale = "upscale"
	// KindLipSync matches the lips of the people in a clip to new sound
	// (a word changed, a line said again): a likeness, so consent applies.
	KindLipSync = "lip-sync"
)

// Tasks of the picture kinds.
const (
	TaskCutOut  = "cut-out"
	TaskUpscale = "upscale"
	TaskLipSync = "sync"
)

func init() {
	kinds = append(kinds,
		KindInfo{Kind: KindBackground, Title: "Background removal", What: "a picture without its background", Output: OutputPicture, Tasks: []string{TaskCutOut}, Reuse: true, Edit: true},
		KindInfo{Kind: KindUpscale, Title: "Larger pictures", What: "a larger picture", Output: OutputPicture, Tasks: []string{TaskUpscale}, Reuse: true},
		KindInfo{Kind: KindLipSync, Title: "Lip sync", What: "a clip with its lips matched to new sound", Output: OutputMedia, Tasks: []string{TaskLipSync}, Reuse: true, Likeness: true},
	)
}

// OnePicture reports whether a kind changes one picture a person gives it.
func OnePicture(kind string) bool { return kind == KindBackground || kind == KindUpscale }
