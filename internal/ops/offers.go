package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"sync"

	"github.com/metavert-io/metarush/internal/app"
)

// An Offering is a first-party ability (D269): something MetaRush already
// does, as the menu (Library → Abilities, `metarush catalogue list`) and find
// show it. It keeps running as the operation it annotates — with Input's
// fields fixed when it is one way of using it ("steady" is asset.fixes.set
// with the steady fix ticked) — and says what it needs, so every surface can
// say whether it works on this computer now (D270).
//
// Each one is a small annotation next to its operation (Offer). The
// catalogue is generated from them (internal/catalog/firstparty.json, kept in
// step by its test), never written by hand.
type Offering struct {
	// ID is stable: receipts and saved runs name it ("steady-footage"). It is
	// never renamed or reused; one taken away is listed as retired.
	ID string
	// Group is where the menu shows it: understand, fix, make, bring, edit,
	// deliver (internal/catalog's Group* names).
	Group string
	// Title is its name in the menu ("Steady shaky footage"); Does says what
	// it does in a sentence, for people.
	Title, Does string
	// Input are fields of the operation's input it always runs with.
	Input map[string]any
	// Words are other words people use for it.
	Words []string
	// Needs is what it needs on a computer.
	Needs Needs
}

// Needs is what a first-party ability needs on a computer to work.
type Needs struct {
	// Works names what MetaRush's first run says works with no keys that it
	// depends on (setup.status, D256), every one needed: transcripts,
	// editing (FFmpeg), renders, checks, scratch-voice. Transcripts need
	// FFmpeg too, so they come with editing.
	Works []string
	// OS: the systems it works on (mac, linux); empty: both.
	OS []string
	// Partly says what it still does on a system OS leaves out ("only
	// plain-colour backgrounds"): there it works, in part, and says so.
	Partly string
	// Leaves says what leaves this computer when it runs ("words leave this
	// computer"); needed for anything that uses a key or Claude Code.
	Leaves string
	// Programs are allowlisted programs it runs that MetaRush doesn't set up
	// by itself (rclone, gh, git, yt-dlp…), each needed.
	Programs []string
	// Keys are the integrations it uses (elevenlabs, gemini); a key of any
	// one of them is enough.
	Keys []string
	// Claude: it uses the person's Claude Code, which counts toward their plan.
	Claude bool
	// Priced: each run is priced first and paid to the service that does it.
	Priced bool
	// Setup names a check another package registers (RegisterReady) for
	// something a person sets up on the computer first ("studio-voices").
	Setup string
}

// A ReadyCheck says whether something a first-party ability needs is set up
// on this computer and, when it isn't, what in a few words ("the studio
// voice cleaner, set up once by a person here").
type ReadyCheck func(ctx context.Context, a *app.App) (ok bool, needs string)

var readyChecks = map[string]ReadyCheck{}

// RegisterReady adds a check Needs.Setup can name (from an init function).
func RegisterReady(name string, fn ReadyCheck) {
	offerMu.Lock()
	defer offerMu.Unlock()
	if _, dup := readyChecks[name]; dup {
		panic("ops.RegisterReady: two checks named " + name)
	}
	readyChecks[name] = fn
}

// ReadyCheckFor is a registered check.
func ReadyCheckFor(name string) (ReadyCheck, bool) {
	offerMu.Lock()
	defer offerMu.Unlock()
	fn, ok := readyChecks[name]
	return fn, ok
}

var (
	offerMu sync.Mutex
	offered = map[string][]Offering{} // operation → its offerings
	offerOf = map[string]string{}     // ID → operation
	offerID = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
)

// Offer annotates an operation with the first-party abilities it is.
// Called from an init function next to the operation's Def; a repeated ID
// panics.
func Offer(op string, list ...Offering) {
	offerMu.Lock()
	defer offerMu.Unlock()
	for _, o := range list {
		if !offerID.MatchString(o.ID) || len(o.ID) > 40 {
			panic(fmt.Sprintf("ops.Offer(%s): %q is no ID (lowercase words joined by dashes)", op, o.ID))
		}
		if other, dup := offerOf[o.ID]; dup {
			panic(fmt.Sprintf("ops.Offer(%s): %s is already %s's", op, o.ID, other))
		}
		offerOf[o.ID] = op
		offered[op] = append(offered[op], o)
	}
}

// OpOffering is an offering with its operation.
type OpOffering struct {
	Op string
	Offering
}

// Offerings lists every first-party ability by ID.
func Offerings() []OpOffering {
	offerMu.Lock()
	defer offerMu.Unlock()
	var out []OpOffering
	for op, list := range offered {
		for _, o := range list {
			out = append(out, OpOffering{Op: op, Offering: o})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
