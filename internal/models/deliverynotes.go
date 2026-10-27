package models

import "time"

// Delivery notes (D237): the words that go out with a version — a title (and
// other titles to choose from), a description, chapters and show notes —
// written by Claude Code on the person's own Claude plan (MetaRush makes no AI
// calls of its own) or by a person. Like a version's note they sit outside the
// spec and its hash: changing them never changes what the version plays.
// Delivery writes them into the delivery record and a Markdown file next to
// the video.

// Who wrote delivery notes (DeliveryNotes.WrittenBy).
const (
	WrittenByClaude = "claude"
	WrittenByPerson = "person"
	WrittenByAgent  = "agent" // another agent (through MCP or a terminal script)
)

// DeliveryNotes are a version's title, description, chapters and show notes.
type DeliveryNotes struct {
	Title string `bson:"title,omitempty" json:"title,omitempty" jsonschema:"maxLength=200,description=The title to post the video under."`
	// Titles are other titles to choose from.
	Titles      []string `bson:"titles,omitempty" json:"titles,omitempty" jsonschema:"maxItems=10,description=Other titles to choose from."`
	Description string   `bson:"description,omitempty" json:"description,omitempty" jsonschema:"maxLength=5000,description=The description (without the chapters: delivery adds them)."`
	// Chapters are where each part of the video starts (seconds into the
	// video), the first at 0:00.
	Chapters  []Chapter `bson:"chapters,omitempty" json:"chapters,omitempty" jsonschema:"maxItems=100"`
	ShowNotes string    `bson:"showNotes,omitempty" json:"showNotes,omitempty" jsonschema:"maxLength=20000,description=Show notes (Markdown): what is said, people and links mentioned."`
	// WrittenBy says who wrote them: claude (Claude Code, on the person's own
	// plan) or person.
	WrittenBy string    `bson:"writtenBy" json:"writtenBy" jsonschema:"enum=claude,enum=person,enum=agent"`
	By        Actor     `bson:"by" json:"by" jsonschema:"description=Who saved them (for Claude's: the person who kept them)."`
	At        time.Time `bson:"at" json:"at"`
}

// Chapter is where one part of a video starts.
type Chapter struct {
	At    float64 `bson:"at" json:"at" jsonschema:"minimum=0,description=Seconds into the video."`
	Title string  `bson:"title" json:"title" jsonschema:"maxLength=120"`
}

// Empty reports whether nothing is written.
func (n *DeliveryNotes) Empty() bool {
	return n == nil || (n.Title == "" && len(n.Titles) == 0 && n.Description == "" && len(n.Chapters) == 0 && n.ShowNotes == "")
}
