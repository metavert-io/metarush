// Package ids mints prefixed, sortable identifiers (prefix + "_" + lowercase ULID).
package ids

import (
	"crypto/rand"
	"regexp"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// Prefixes for every document type. IDs are portable across machines and never
// encode machine-local paths.
const (
	Workspace    = "ws"
	Installation = "ins"
	User         = "usr"
	Project      = "prj"
	Asset        = "ast"
	Ingest       = "ing"
	Segment      = "seg"
	Insight      = "fin"
	Revision     = "rev"
	Job          = "job"
	Render       = "rnd"
	Audit        = "aud"
	Comment      = "cmt"
	Upload       = "upl"
	Speaker      = "spk"
	Person       = "per"
	Todo         = "tdo"
	Run          = "run"
	Face         = "fac"
	// FaceGroup is one person's unnamed faces in a clip, asked about once (D155).
	FaceGroup = "fgr"
	// Resources (D79), linked folders (D81) and project links (D83).
	Resource     = "res"
	LinkedFolder = "fld"
	ProjectLink  = "lnk"
	// Collections of footage (D96).
	Collection = "col"
	// Conversations: recordings of one talk on one clock (D99).
	Conversation = "cnv"
	// Generative video (D123–D127): generation records, looks and consent records.
	Generation = "gen"
	Look       = "lok"
	Consent    = "cst"
	// Remotion sources: whole Remotion projects kept in a project (D131).
	RemotionSource = "rms"
	// A refresh of a Remotion source from its own steps (D173) and a push of
	// its changes back to its repository (D176).
	RemotionRefresh = "rmf"
	RemotionPush    = "rmp"
	// Transcript fixes (D145–D149): a fix, a batch of fixes made together, a
	// kept machine transcript, a snapshot of a version's words, a spelling list entry.
	TranscriptFix      = "tfx"
	FixBatch           = "tfb"
	TranscriptOriginal = "tor"
	TranscriptSnapshot = "tsn"
	Spelling           = "spl"
	// What a project keeps (D166): a rule or check, a sign-off, a kept (or
	// declined) thing a conversation brought in, a file dropped into the chat.
	Rule       = "rul"
	Signoff    = "sgn"
	Keep       = "kep"
	Attachment = "att"
	// A project's own Content Settings (D192).
	ContentSettings = "cnt"
	// Recipes and their runs (D201–D204).
	Recipe    = "rcp" // Recipes
	RecipeRun = "rrn" // Recipes' runs
	// A run of an ability (D234).
	AbilityRun = "abr"
	// A draft an AI app proposed, awaiting a person's review (D248).
	Draft = "drf"
	// A person's own presenter kept at an AI service (D273), and a record
	// that a service's training opt-out is set (D276).
	Presenter      = "prs"
	TrainingOptOut = "opt"
	// SpokenChange is a word changed in the speaker's own voice (D308).
	SpokenChange = "swc"
	// A screen recording made in the dashboard: its screen, camera and voice (D260).
	Recording = "rcd"
	// A person sending a version out although something held it (D299).
	ShipOverride = "sho"
	// A review link for people outside the workspace (D314).
	ReviewLink = "rvl"
)

var pattern = regexp.MustCompile(`^([a-z]{2,4})_([0-9a-z]{26})$`)

// New returns a fresh ID with the given prefix.
func New(prefix string) string {
	return prefix + "_" + strings.ToLower(ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String())
}

// Valid reports whether id is well formed and carries the given prefix.
func Valid(prefix, id string) bool {
	m := pattern.FindStringSubmatch(id)
	return m != nil && m[1] == prefix
}

// Prefix returns the prefix of id, or "" when malformed.
func Prefix(id string) string {
	m := pattern.FindStringSubmatch(id)
	if m == nil {
		return ""
	}
	return m[1]
}
