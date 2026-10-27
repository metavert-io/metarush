package models

import (
	"slices"
	"time"
)

// What may leave this computer (D288): a Content Settings part that names,
// for every project using the settings (a client), which of their things may
// leave this computer and to whom. Nothing set keeps today's behaviour:
// everything MetaRush offers may be used.

// Kinds of a client's things that can leave this computer.
const (
	// LeaveFootage: video and sound files (clips, renders, recordings).
	LeaveFootage = "footage"
	// LeaveVoices: people's voices — speech cut from footage, voice samples,
	// recordings for a voice change, a dub or a transcript made elsewhere.
	LeaveVoices = "voices"
	// LeaveFaces: pictures that can show people — frames of footage, looks,
	// contact sheets.
	LeaveFaces = "faces"
	// LeaveWords: transcripts, scripts, captions, notes and other words.
	LeaveWords = "words"
	// LeavePictures: pictures, logos, slides and frames.
	LeavePictures = "pictures"
)

// LeaveKinds lists the kinds in the order people see them.
var LeaveKinds = []string{LeaveFootage, LeaveVoices, LeaveFaces, LeaveWords, LeavePictures}

// Places a client's things can go.
const (
	// PlaceAI: AI services (by provider).
	PlaceAI = "ai"
	// PlaceAbilities: abilities' runs (by the host each request goes to).
	PlaceAbilities = "abilities"
	// PlaceClaude: Claude, through the Claude Code account of the computer
	// that answers in the panel (by its terms: consumer, commercial, cloud).
	PlaceClaude = "claude"
	// PlaceStorage: a cloud bucket holding the workspace's media, or a
	// database on a server elsewhere holding its words.
	PlaceStorage = "storage"
	// PlaceDelivery: files MetaRush writes for people elsewhere — delivered
	// videos and their folders, timeline exports, Recipes' files.
	PlaceDelivery = "delivery"
	// PlaceReviewers: people outside the workspace a person sends a review
	// link to (D317): the version's review copy, and its words when the link
	// shows them.
	PlaceReviewers = "reviewers"
)

// LeavePlaces lists the places in the order people see them.
var LeavePlaces = []string{PlaceAI, PlaceAbilities, PlaceClaude, PlaceStorage, PlaceDelivery, PlaceReviewers}

// LeaveRules are what may leave this computer for every project using a
// project's Content Settings (D288). Empty rules change nothing.
type LeaveRules struct {
	// Nothing: nothing of these projects leaves this computer, wherever it
	// would go.
	Nothing bool `bson:"nothing,omitempty" json:"nothing,omitempty"`
	// Places say, per place (ai, abilities, claude, storage, delivery,
	// reviewers), what
	// stays on this computer and who may receive the rest.
	Places map[string]LeavePlace `bson:"places,omitempty" json:"places,omitempty"`
	// Note says why, in the person's own words ("the client's contract").
	Note      string    `bson:"note,omitempty" json:"note,omitempty" jsonschema:"maxLength=500"`
	UpdatedAt time.Time `bson:"updatedAt,omitempty" json:"updatedAt,omitempty"`
	UpdatedBy *Actor    `bson:"updatedBy,omitempty" json:"updatedBy,omitempty"`
}

// LeavePlace is what one place may receive.
type LeavePlace struct {
	// Keep lists the kinds that never go there (footage, voices, faces,
	// words, pictures): they stay on this computer.
	Keep []string `bson:"keep,omitempty" json:"keep,omitempty" jsonschema:"uniqueItems=true"`
	// Only lists who may receive the rest: providers for AI services
	// (gemini, elevenlabs), hosts for abilities (api.example.com,
	// *.example.com), terms for Claude (commercial, cloud). Empty: anyone
	// the place offers.
	Only []string `bson:"only,omitempty" json:"only,omitempty" jsonschema:"maxItems=32"`
}

// Empty reports a place with no rule.
func (p LeavePlace) Empty() bool { return len(p.Keep) == 0 && len(p.Only) == 0 }

// Empty reports rules that change nothing.
func (r *LeaveRules) Empty() bool {
	if r == nil || r.Nothing {
		return r == nil
	}
	for _, p := range r.Places {
		if !p.Empty() {
			return false
		}
	}
	return true
}

// Place is a place's rule (empty when none).
func (r *LeaveRules) Place(name string) LeavePlace {
	if r == nil || r.Places == nil {
		return LeavePlace{}
	}
	return r.Places[name]
}

// Keeps reports whether a kind stays on this computer instead of going to a
// place.
func (r *LeaveRules) Keeps(place, kind string) bool {
	if r == nil {
		return false
	}
	return r.Nothing || slices.Contains(r.Place(place).Keep, kind)
}
