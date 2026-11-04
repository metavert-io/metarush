package models

// How AI took part in something a video plays (D435): one answer, which
// Content Credentials, the delivery's record, the quality check and the
// receipt all read, so they can't say different things about the same piece
// — for AI services' work by its kind, for abilities' outputs by what their
// manifests declare (D460).
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

// AIWorkNone is what an ability's manifest says of an output no AI took part
// in (ai: none); it is a declaration, never an answer ("" is).
const AIWorkNone = "none"

// AIDeclarations are what an ability's manifest may say an output is (D460).
var AIDeclarations = []string{AIWorkMade, AIWorkEdited, AIWorkEnhanced, AIWorkNone}

// AIWorkDeclared is what AI did to an ability's output, from what its
// manifest declares for that kind of output (ai: made, edited, enhanced or
// none; D460). Nothing declared — a manifest that doesn't say, or an output
// kept before manifests could — counts as made: saying too much is safer
// than too little.
func AIWorkDeclared(declared string) string {
	switch declared {
	case AIWorkNone:
		return ""
	case AIWorkEdited, AIWorkEnhanced:
		return declared
	}
	return AIWorkMade
}

// AIWorkStronger is the one of two answers that tells people more: made,
// then edited, then enhanced, then nothing.
func AIWorkStronger(a, b string) string {
	rank := func(w string) int {
		switch w {
		case AIWorkMade:
			return 3
		case AIWorkEdited:
			return 2
		case AIWorkEnhanced:
			return 1
		}
		return 0
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// AIWorkOfSource is what AI did to a clip, from where it came from: a
// generated clip by its kind of AI work (or what the ability that did the
// work declared, Generated.AIWork), a clip an ability brought in by what its
// manifest declares for that output (D460), else nothing. Content
// Credentials, the delivery's record, the quality check and the receipt all
// ask it.
func AIWorkOfSource(s *AssetSource) string {
	switch {
	case s == nil:
		return ""
	case s.IsGenerated():
		if g := s.Generated; g != nil {
			if g.AIWork != "" {
				return g.AIWork
			}
			return AIWorkOf(g.Kind, false)
		}
		return AIWorkMade
	case s.IsAbility():
		if s.MadeWith == nil {
			return AIWorkMade
		}
		return AIWorkDeclared(s.MadeWith.AI)
	}
	return ""
}

// AIWorkOfMadeWith is what AI did to a picture MetaRush keeps, from how it
// was made: an AI service's picture by its kind of AI work (kind, when the
// generation record had to say it; else AIKind; "picture" when neither) or
// what the ability that did the work declared (AIWork); an ability's output by
// what its manifest declares (D460); a code slide, diagram or card MetaRush
// drew isn't AI work.
func AIWorkOfMadeWith(mw *ResourceMadeWith, kind string) string {
	if mw == nil {
		return ""
	}
	switch mw.Kind {
	case "ai":
		if mw.AIWork != "" {
			return mw.AIWork
		}
		if kind == "" {
			kind = mw.AIKind
		}
		if kind == "" {
			kind = "picture"
		}
		return AIWorkOf(kind, mw.Edit)
	case "ability":
		if mw.Ability == nil {
			return AIWorkMade
		}
		return AIWorkDeclared(mw.Ability.AI)
	}
	return ""
}
