package models

import "time"

// FaceTrack is one face followed through a shot of a clip by the on-screen
// index (collection faces, D73). A face is linked to a person by a person
// (ConfirmedBy): MetaRush asks "Is this Maya?" when this face was the only
// one on screen while Maya's voice spoke, or when it looks like faces a
// person confirmed are Maya (Candidate), and a person answers. Since D114 a
// face that looks very much like one person's confirmed faces, and clearly
// unlike everyone else's, is named automatically (Recognized) until a person
// says otherwise; automatic names never teach MetaRush what anyone looks like.
type FaceTrack struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	AssetID     string `bson:"assetId" json:"assetId"`
	// Track is the face's number within the clip's index.
	Track   int     `bson:"track" json:"track"`
	From    float64 `bson:"from" json:"from"`
	To      float64 `bson:"to" json:"to"`
	Samples int     `bson:"samples" json:"samples" jsonschema:"description=Frames it was seen in."`
	// At and Box are the frame that shows the face best and where it is in it
	// ([x, y, width, height] from the top-left, 0 to 1).
	At      float64   `bson:"at" json:"at"`
	Box     []float64 `bson:"box" json:"box"`
	Quality float64   `bson:"quality" json:"quality" jsonschema:"description=How usable the best frame is as a picture of the face (0 to 1)."`
	// PersonID and Name: whose face it is, as a person confirmed.
	PersonID    string     `bson:"personId,omitempty" json:"personId,omitempty"`
	Name        string     `bson:"name,omitempty" json:"name,omitempty"`
	ConfirmedBy *Actor     `bson:"confirmedBy,omitempty" json:"confirmedBy,omitempty"`
	ConfirmedAt *time.Time `bson:"confirmedAt,omitempty" json:"confirmedAt,omitempty"`
	// Naming says who gave the name — a person, or software acting for one
	// (D274); consent checks count only a person's.
	Naming *Naming `bson:"naming,omitempty" json:"naming,omitempty"`
	// Checked: a person was asked whether this confirmed face (which looks
	// unlike that person's other faces) really is them, and said yes.
	Checked bool `bson:"checked,omitempty" json:"checked,omitempty"`
	// Recognized is who MetaRush named this face by its looks (not confirmed).
	Recognized *FaceRecognized `bson:"recognized,omitempty" json:"recognized,omitempty"`
	// Candidate is who it might be (a question is open about it).
	Candidate *FaceCandidate `bson:"candidate,omitempty" json:"candidate,omitempty"`
	// Rejected are people someone said this face is not.
	Rejected []string `bson:"rejected,omitempty" json:"rejected,omitempty"`
	// Group is the face's question group in its clip (D155): the unnamed faces
	// of one person, asked about once ("fgr_…"). It is carried over when the
	// clip is indexed again, like a name.
	Group string `bson:"group,omitempty" json:"group,omitempty"`
	// Skipped: a person skipped the question about this face's group; it is
	// never asked about again (it can still be named by hand or by its looks).
	Skipped bool `bson:"skipped,omitempty" json:"skipped,omitempty"`
	// ConfirmedVia is how a person confirmed the name: "face" (this face) or
	// "group" (one answer for a group of faces, D155); ConfirmedGroup is that group.
	ConfirmedVia   string `bson:"confirmedVia,omitempty" json:"confirmedVia,omitempty" jsonschema:"enum=face,enum=group"`
	ConfirmedGroup string `bson:"confirmedGroup,omitempty" json:"confirmedGroup,omitempty"`
	// AsideFrom is the person a group answer named while this face was left
	// out of it (it looked less like the others); it is asked about on its own.
	AsideFrom string `bson:"asideFrom,omitempty" json:"asideFrom,omitempty"`
	// NotNames are names shown or said in the video (folded) that a person said
	// this face is not, for names nobody has in People yet.
	NotNames []string `bson:"notNames,omitempty" json:"-"`
	// Prints are the face's fingerprints (its best frames) and PrintModel the
	// model that made them (D114). Server side only: never sent to clients.
	Prints     []FacePrint `bson:"prints,omitempty" json:"-"`
	PrintModel string      `bson:"printModel,omitempty" json:"-"`
	CreatedAt  time.Time   `bson:"createdAt" json:"createdAt"`
	UpdatedAt  time.Time   `bson:"updatedAt" json:"updatedAt"`
}

// FacePrint is one face fingerprint: when in the clip, how usable the frame
// was, and the vector.
type FacePrint struct {
	At float64   `bson:"at"`
	Q  float64   `bson:"q"`
	V  []float32 `bson:"v"`
}

// Confirmed reports whether a person said whose face this is.
func (f FaceTrack) Confirmed() bool { return f.PersonID != "" && f.ConfirmedBy != nil }

// FaceCandidate is who a face might be, and why MetaRush thinks so.
type FaceCandidate struct {
	PersonID string  `bson:"personId" json:"personId"`
	Name     string  `bson:"name" json:"name"`
	Seconds  float64 `bson:"seconds" json:"seconds" jsonschema:"description=How long this face was the only one on screen while that person spoke."`
	Share    float64 `bson:"share" json:"share" jsonschema:"description=Of the time this face was alone on screen with someone speaking, the share that was this person."`
	// By is why: "voice" (alone on screen while they spoke; also when empty),
	// "looks" (it looks like faces a person confirmed are them), "both", or —
	// for a group of faces (D155, D156) — "shown" (the video shows the name
	// under the face), "said" (the name is said in the video) or "aside" (left
	// out when a person named the group's other faces).
	By string `bson:"by,omitempty" json:"by,omitempty" jsonschema:"enum=voice,enum=looks,enum=both,enum=shown,enum=said,enum=aside"`
	// Score is how much it looks like them (0–1), when looks count.
	Score float64 `bson:"score,omitempty" json:"score,omitempty"`
	// Recheck: the face was confirmed as this person but looks unlike their
	// other faces, so MetaRush asks again.
	Recheck bool `bson:"recheck,omitempty" json:"recheck,omitempty"`
}

// FaceRecognized is a name MetaRush gave a face by its looks (D114).
type FaceRecognized struct {
	PersonID string    `bson:"personId" json:"personId"`
	Name     string    `bson:"name" json:"name"`
	Score    float64   `bson:"score" json:"score" jsonschema:"description=How much it looks like the person's confirmed faces (0 to 1)."`
	At       time.Time `bson:"at" json:"at"`
}
