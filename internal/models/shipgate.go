package models

import "time"

// The ship gate (D298, D299): a version waits before it goes out to a client
// while the quality check found problems, hasn't checked it (or checked it
// before today's promises), or a sign-off is still waiting — until those are
// done, or a person sends it out anyway with a note.

// ShipGateSettings is the workspace's choice (owners, D298).
type ShipGateSettings struct {
	On        bool      `bson:"on" json:"on"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
	UpdatedBy Actor     `bson:"updatedBy" json:"updatedBy"`
}

// Gate modes of a set of Content Settings (GateSetting.Mode).
const (
	GateOn  = "on"  // their videos always wait
	GateOff = "off" // their videos never wait (reminders only)
)

// GateSetting is a Content Settings part (D298): the client's own word on
// the gate, over the workspace's.
type GateSetting struct {
	Mode      string    `bson:"mode" json:"mode" jsonschema:"enum=on,enum=off"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
	UpdatedBy Actor     `bson:"updatedBy" json:"updatedBy"`
}

// Kinds of things that hold a version back (ShipHold.Kind).
const (
	HoldApproval  = "approval"   // nobody approved it (delivery's own rule: --allow-draft; never overridden here)
	HoldRender    = "render"     // it has no finished render to send (nothing to override)
	HoldQAMissing = "qa-missing" // its render hasn't had a quality check
	HoldQARunning = "qa-running" // the quality check is waiting or running
	HoldQAFailed  = "qa-failed"  // the quality check couldn't finish
	HoldQAStale   = "qa-stale"   // checked before today's promises or description
	HoldQAErrors  = "qa-errors"  // the quality check found problems
	HoldSignoff   = "signoff"    // a sign-off is still waiting
	// HoldConsent: it plays words changed in someone's voice (or a mouth
	// matched to them) whose consent is no longer in force (D309): never
	// overridden, the words come out of the cut first.
	HoldConsent = "consent"
)

// ShipHold is one thing holding a version back, in plain words.
type ShipHold struct {
	// Key names exactly what holds it (the report, the render, the sign-off),
	// so a note sending it out anyway covers only what the person saw.
	Key  string `bson:"key" json:"key"`
	Kind string `bson:"kind" json:"kind" jsonschema:"enum=approval,enum=render,enum=qa-missing,enum=qa-running,enum=qa-failed,enum=qa-stale,enum=qa-errors,enum=signoff,enum=consent"`
	// Says what holds it, in plain words.
	Says string `bson:"says" json:"says"`
}

// ShipOverride is a person sending a version out although something held it
// (D299): people only, with a note, recorded on the version and written into
// the delivery record, its notes file and the project's receipt.
type ShipOverride struct {
	ID   string    `bson:"id" json:"id"`
	By   Actor     `bson:"by" json:"by"`
	At   time.Time `bson:"at" json:"at"`
	Note string    `bson:"note" json:"note" jsonschema:"minLength=3,maxLength=500"`
	// Holds are what held it when the person went ahead: the note covers
	// these, and anything holding it later asks again.
	Holds []ShipHold `bson:"holds" json:"holds"`
}

// ShipSent records a version going out past what held it (D299): when, by
// whom, the notes that let it out (their IDs) and where (a folder's name).
type ShipSent struct {
	At        time.Time `bson:"at" json:"at"`
	By        Actor     `bson:"by" json:"by"`
	Overrides []string  `bson:"overrides" json:"overrides"`
	What      string    `bson:"what" json:"what" jsonschema:"enum=delivery,enum=timeline,enum=post"`
	Folder    string    `bson:"folder,omitempty" json:"folder,omitempty" jsonschema:"description=The folder's name (not its path); for a post, the platform's name."`
}

// SentPost is a ShipSent of a version posted to a video platform (D305).
const SentPost = "post"

// Covers reports whether the override covers a hold.
func (o ShipOverride) Covers(key string) bool {
	for _, h := range o.Holds {
		if h.Key == key {
			return true
		}
	}
	return false
}

// Promise results in a quality check (QAPromise.Status, D300).
const (
	PromiseKept       = "kept"
	PromiseBroken     = "broken"
	PromiseUnmeasured = "unmeasured" // MetaRush couldn't measure it here: look yourself (a warning, it never holds)
	PromiseNA         = "notApplicable"
)

// QAPromise is what a quality check measured for one promise made to the
// client (D300): its words, what MetaRush measures, and the result.
type QAPromise struct {
	RuleID  string `bson:"ruleId" json:"ruleId"`
	Text    string `bson:"text" json:"text"`
	Measure string `bson:"measure" json:"measure"`
	// House: a promise of the Content Settings the project uses (HouseOf: whose).
	House   bool   `bson:"house,omitempty" json:"house,omitempty"`
	HouseOf string `bson:"houseOf,omitempty" json:"houseOf,omitempty"`
	// Says is what MetaRush measures, as a plain sentence.
	Says   string `bson:"says" json:"says"`
	Status string `bson:"status" json:"status" jsonschema:"enum=kept,enum=broken,enum=unmeasured,enum=notApplicable"`
	// Measured is the result in plain words ("It lasts 58 seconds.").
	Measured string `bson:"measured" json:"measured"`
	// Value and Limit are the numbers behind it, in Unit, where there are numbers.
	Value *float64 `bson:"value,omitempty" json:"value,omitempty"`
	Limit *float64 `bson:"limit,omitempty" json:"limit,omitempty"`
	Unit  string   `bson:"unit,omitempty" json:"unit,omitempty" jsonschema:"enum=,enum=seconds,enum=pixels,enum=fps,enum=percent,enum=steps,enum=clips"`
	Basis string   `bson:"basis" json:"basis" jsonschema:"enum=observed,enum=heuristic"`
	// FindingID is the problem (broken) or the warning (unmeasured) it made;
	// FindingIDs every one (a promise broken in several places makes several).
	FindingID  string   `bson:"findingId,omitempty" json:"findingId,omitempty"`
	FindingIDs []string `bson:"findingIds,omitempty" json:"findingIds,omitempty"`
	// Expected is what the version should carry to keep it, when MetaRush
	// writes it (the line disclosing what was made with AI).
	Expected string `bson:"expected,omitempty" json:"expected,omitempty"`
}
