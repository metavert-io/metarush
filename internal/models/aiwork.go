package models

// How AI took part in something a video plays (D435): one answer, which
// Content Credentials, the delivery's record and the quality check all read,
// so they can't say different things about the same piece.
const (
	// AIWorkMade: a generative AI model made it (a generated shot, a
	// picture made from words, AI speech, music, a sound effect, a dub, a
	// presenter).
	AIWorkMade = "made"
	// AIWorkEdited: a generative AI model changed captured media by making
	// new content in it (a mouth matched to new words, a voice said again in
	// another).
	AIWorkEdited = "edited"
	// AIWorkEnhanced: an AI model changed it without making anything new (a
	// picture's background taken away, a picture made larger, a voice made
	// clearer): said in Content Credentials, not disclosed as AI-generated.
	AIWorkEnhanced = "enhanced"
)

// AIWorkOf is what a kind of AI work (Generation.Kind, aiservices' kinds:
// "" and "video" are generated video) does to what it makes; "" for work
// that isn't media a video plays (a voice copy, a presenter kept at a
// service, a transcript). edit marks a picture changed without making
// anything new, for pictures kept before their kind was known. A kind not
// listed here counts as made: saying too much is safer than too little.
func AIWorkOf(kind string, edit bool) string {
	switch kind {
	case "voice-copy", "presenter-make", "transcription":
		return ""
	case "voice-change", "lip-sync":
		return AIWorkEdited
	case "background-removal", "upscale":
		return AIWorkEnhanced
	}
	if edit {
		return AIWorkEnhanced
	}
	return AIWorkMade
}

// AIDisclosed reports whether people are told a piece was made with AI: made
// or edited by a generative model (an enhancement isn't, D279).
func AIDisclosed(work string) bool { return work == AIWorkMade || work == AIWorkEdited }
