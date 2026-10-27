package models

// Disclosure of AI-generated footage (D127). Platforms and the EU AI Act ask
// people to say when a video contains realistic AI-generated or altered
// footage. MetaRush flags generated clips everywhere and lists, with every
// export that uses one, what each place asks for. It is a reminder in plain
// words, not legal advice; the rules are the platforms' own and change.

// DisclosureItem is one place's ask, in plain words.
type DisclosureItem struct {
	Place string `json:"place" jsonschema:"description=Where it applies: YouTube\\, TikTok\\, Instagram and Facebook\\, the EU."`
	// What to do there.
	What string `json:"what"`
	// When it applies.
	When string `json:"when"`
}

// DisclosureChecklist is what to do when posting a video with AI-generated
// footage. likeness: it shows a real person's likeness (asked about more strictly).
func DisclosureChecklist(likeness bool) []DisclosureItem {
	realistic := "When the generated footage looks real (people, places or events)."
	if likeness {
		realistic = "Always for this video: it shows a real person's likeness made with AI."
	}
	return []DisclosureItem{
		{Place: "YouTube", What: "When you upload, answer Yes to the question about altered or synthetic content.", When: realistic},
		{Place: "TikTok", What: "Turn on the AI-generated content label in the post's settings.", When: realistic},
		{Place: "Instagram and Facebook", What: "Add the AI info label when you post.", When: realistic},
		{Place: "The EU (AI Act, article 50)", What: "Say clearly that the video contains AI-generated footage, for example in the description or on screen.",
			When: "When people in the EU can see it and the footage could pass for real."},
	}
}

// DisclosureNotice is the plain sentence for a description: "This video contains AI-generated footage."
func DisclosureNotice(likeness bool) string {
	if likeness {
		return "This video contains AI-generated footage, including a real person's likeness made with their consent."
	}
	return "This video contains AI-generated footage."
}

// PresenterNotice is the sentence for a video with an AI presenter (D277):
// likeness says it is a real person's presenter, made with their consent.
func PresenterNotice(likeness bool) string {
	if likeness {
		return "This video contains AI-generated footage, including a presenter made with AI from a real person's likeness, with their consent."
	}
	return "This video contains AI-generated footage, including an AI presenter."
}
