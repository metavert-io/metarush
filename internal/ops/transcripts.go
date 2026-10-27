package ops

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
	"github.com/metavert-io/metarush/internal/transcripts"
)

// Fixing transcripts (D145–D149, docs/transcripts.md): retype words, find and
// replace across videos, who says a stretch, a clip's history and undo, and
// spelling lists. Every change is a fix with who and when; the machine's
// transcript is kept; saved versions keep their words (fixes wait for a new
// version, transcript.pending).

// q quotes a person's (or the transcript's) words in a sentence.
func q(s string) string { return plainwords.TheirsOpen + s + plainwords.TheirsClose }

// at is a source time for a sentence ("0:12.4").
func at(v float64) string { return cutsheet.Clock(math.Round(v*10) / 10) }

// TranscriptFindIn is what to look for, and where (one clip, a project, everywhere).
type TranscriptFindIn struct {
	Query string `json:"query" jsonschema:"minLength=1,maxLength=200,description=The words to find."`
	// AssetID, ProjectID or Everywhere: where to look (a clip or a project by ID or name).
	AssetID    string `json:"assetId,omitempty" jsonschema:"description=One clip (ID or title)."`
	ProjectID  string `json:"projectId,omitempty" jsonschema:"description=Every clip of a project (ID or name)."`
	Everywhere bool   `json:"everywhere,omitempty" jsonschema:"description=Every clip in the workspace."`
	// PartOfWords also finds the words inside longer words ("Brook" in "FernBrook");
	// by default only whole words match.
	PartOfWords bool `json:"partOfWords,omitempty"`
	MatchCase   bool `json:"matchCase,omitempty"`
	// With previews a replace: every place says what it would become.
	With  *string `json:"with,omitempty" jsonschema:"maxLength=200"`
	Limit int     `json:"limit,omitempty" jsonschema:"minimum=0,maximum=1000"`
}

// TranscriptFindOut lists every place, with the words around it.
type TranscriptFindOut struct {
	Hits  []transcripts.Hit `json:"hits"`
	Total int               `json:"total"`
	Clips int               `json:"clips"`
	// More: there are more places than hits lists (raise limit).
	More    bool   `json:"more"`
	Summary string `json:"summary"`
	Note    string `json:"note,omitempty"`
}

// TranscriptReplaceIn changes words wherever they are said (or only the ticked places).
type TranscriptReplaceIn struct {
	Query       string `json:"query" jsonschema:"minLength=1,maxLength=200"`
	With        string `json:"with" jsonschema:"maxLength=200,description=What to write instead (empty takes the words out of the transcript; the sound stays)."`
	AssetID     string `json:"assetId,omitempty"`
	ProjectID   string `json:"projectId,omitempty"`
	Everywhere  bool   `json:"everywhere,omitempty"`
	PartOfWords bool   `json:"partOfWords,omitempty"`
	MatchCase   bool   `json:"matchCase,omitempty"`
	// Only lists the places to change (ids from transcript.find); empty changes every place.
	Only []string `json:"only,omitempty" jsonschema:"maxItems=5000"`
	// Spelling also puts the new words on the spelling list (the project's, or
	// the workspace's when replacing everywhere), so new transcripts get them right.
	Spelling bool `json:"spelling,omitempty"`
}

// TranscriptReplaceOut says what changed.
type TranscriptReplaceOut struct {
	BatchID string                 `json:"batchId"`
	Changed int                    `json:"changed"`
	Clips   int                    `json:"clips"`
	Fixes   []models.TranscriptFix `json:"fixes"`
	// Gone are ticked places that say something else now (not changed).
	Gone       []string                `json:"gone"`
	Spelling   *models.SpellingEntry   `json:"spelling,omitempty"`
	Suggestion *transcripts.Suggestion `json:"suggestion,omitempty"`
	Summary    string                  `json:"summary"`
	Note       string                  `json:"note,omitempty"`
}

// TranscriptFixIn retypes what is said in a stretch of a clip.
type TranscriptFixIn struct {
	AssetID string  `json:"assetId" jsonschema:"description=The clip (ID or title)."`
	From    float64 `json:"from" jsonschema:"minimum=0,description=Where the words start (source seconds)."`
	To      float64 `json:"to" jsonschema:"minimum=0,description=Where they end (source seconds)."`
	Text    string  `json:"text" jsonschema:"maxLength=4000,description=What is really said there."`
	// Expect is what the stretch says now, as you saw it: nothing changes if
	// someone changed it since.
	Expect    string `json:"expect,omitempty" jsonschema:"maxLength=4000"`
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=The project you are working in (for spelling suggestions)."`
}

// TranscriptFixOut says what changed.
type TranscriptFixOut struct {
	AssetID    string                  `json:"assetId"`
	Rev        int                     `json:"rev"`
	BatchID    string                  `json:"batchId,omitempty"`
	Fixes      []models.TranscriptFix  `json:"fixes"`
	Suggestion *transcripts.Suggestion `json:"suggestion,omitempty"`
	Summary    string                  `json:"summary"`
}

// TranscriptSpeakerIn says who really says a stretch (recorded in the clip's history).
type TranscriptSpeakerIn struct {
	SpeakerAssignIn
	ProjectID string `json:"projectId,omitempty"`
}

// TranscriptSpeakerOut is footage.speakers.assign's answer and the fix it recorded.
type TranscriptSpeakerOut struct {
	SpeakerAssignOut
	FixID string `json:"fixId,omitempty"`
}

// TranscriptHistoryIn names a clip.
type TranscriptHistoryIn struct {
	AssetID string `json:"assetId"`
	Limit   int    `json:"limit,omitempty" jsonschema:"minimum=0,maximum=2000"`
}

// TranscriptHistoryEntry is one change in a clip's history.
type TranscriptHistoryEntry struct {
	transcripts.HistoryEntry
	// Says is the change in plain words; Who made it.
	Says string `json:"says"`
	Who  string `json:"who"`
	// BatchSize is how many fixes were made together with it (a find and replace).
	BatchSize int `json:"batchSize"`
}

// TranscriptOriginal says where the machine's transcript of a clip came from.
type TranscriptOriginal struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	KeptAt   string `json:"keptAt"`
}

// TranscriptHistoryOut is a clip's fixes, newest first.
type TranscriptHistoryOut struct {
	AssetID string                   `json:"assetId"`
	Title   string                   `json:"title"`
	Rev     int                      `json:"rev"`
	Base    int                      `json:"base"`
	Fixes   int                      `json:"fixes" jsonschema:"description=Fixes in force."`
	Entries []TranscriptHistoryEntry `json:"entries"`
	// Original is set once something changed: the machine's transcript is kept.
	Original *TranscriptOriginal `json:"original,omitempty"`
	Carry    *models.CarryReport `json:"carry,omitempty"`
	Summary  string              `json:"summary"`
}

// TranscriptUndoIn names a fix, or a batch (a whole find and replace).
type TranscriptUndoIn struct {
	FixID   string `json:"fixId,omitempty"`
	BatchID string `json:"batchId,omitempty"`
}

// TranscriptSkip is a fix that stayed, and why.
type TranscriptSkip struct {
	FixID   string `json:"fixId"`
	AssetID string `json:"assetId"`
	Why     string `json:"why"`
}

// TranscriptUndoOut says what was taken back.
type TranscriptUndoOut struct {
	Undone  int              `json:"undone"`
	Clips   int              `json:"clips"`
	Skipped []TranscriptSkip `json:"skipped"`
	Summary string           `json:"summary"`
}

// SpellingListIn names a project: the spelling list of the Content Settings it
// uses (D193). Empty: the default's (what new projects and footage in no project use).
type SpellingListIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or name). Empty: the default's list."`
}

// SpellingListOut is the spelling list a project uses.
type SpellingListOut struct {
	ProjectID string                 `json:"projectId,omitempty"`
	Entries   []models.SpellingEntry `json:"entries"`
	// From is the project whose Content Settings hold the list (nil: none set up).
	From *ContentProjectRef `json:"from,omitempty"`
	// ReadOnly: the project uses another project's settings; change the list there.
	ReadOnly bool `json:"readOnly"`
	// UsedBy counts the other projects using the same list.
	UsedBy  int    `json:"usedBy"`
	Summary string `json:"summary"`
}

// SpellingAddIn puts a word on a list.
type SpellingAddIn struct {
	// ProjectID: the list of the project's own Content Settings; empty: the default's.
	ProjectID string   `json:"projectId,omitempty" jsonschema:"description=A project (ID or name) with its own Content Settings: their spelling list (every project using them gets it). Empty: the default's."`
	Word      string   `json:"word" jsonschema:"minLength=1,maxLength=80,description=How it is always written: FernBrook\\, Elmwood\\, Maple Grove."`
	HeardAs   []string `json:"heardAs,omitempty" jsonschema:"maxItems=20,description=Other ways transcripts write it that differ by more than capitals\\, spaces or punctuation."`
	Note      string   `json:"note,omitempty" jsonschema:"maxLength=200"`
	// FixExisting also corrects the transcripts that already write it another
	// way (every clip using the list: the projects using these settings, and
	// footage in no project when they are the default's).
	FixExisting bool `json:"fixExisting,omitempty"`
}

// SpellingAddOut says what the list holds now and what it changed.
type SpellingAddOut struct {
	Entry   models.SpellingEntry `json:"entry"`
	Created bool                 `json:"created"`
	// Waiting counts places in transcripts the entry would change (when not fixing them now).
	Waiting int    `json:"waiting"`
	Fixed   int    `json:"fixed"`
	Clips   int    `json:"clips"`
	BatchID string `json:"batchId,omitempty"`
	Summary string `json:"summary"`
}

// SpellingRemoveIn takes a word off a list.
type SpellingRemoveIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or name) with its own Content Settings. Empty: the default's."`
	Word      string `json:"word" jsonschema:"minLength=1,maxLength=80"`
}

// SpellingRemoveOut says what was removed.
type SpellingRemoveOut struct {
	Removed models.SpellingEntry `json:"removed"`
	Summary string               `json:"summary"`
}

// SpellingApplyIn corrects existing transcripts by the spelling lists.
type SpellingApplyIn struct {
	AssetID    string `json:"assetId,omitempty"`
	ProjectID  string `json:"projectId,omitempty"`
	Everywhere bool   `json:"everywhere,omitempty"`
	// SettingsOf corrects every clip using the spelling list of a project's
	// settings: the projects using them, and footage in no project when they
	// are the default's ("Fix existing transcripts too" after a change).
	SettingsOf string   `json:"settingsOf,omitempty" jsonschema:"description=A project (ID or name): every clip using the spelling list of the settings it uses (the projects using them\\, and footage in no project when they are the default's)."`
	DryRun     bool     `json:"dryRun,omitempty" jsonschema:"description=Only list the places."`
	Only       []string `json:"only,omitempty" jsonschema:"maxItems=5000"`
}

// SpellingApplyOut lists the places (and what changed).
type SpellingApplyOut struct {
	Hits    []transcripts.Hit `json:"hits"`
	Total   int               `json:"total"`
	Changed int               `json:"changed"`
	Clips   int               `json:"clips"`
	BatchID string            `json:"batchId,omitempty"`
	Summary string            `json:"summary"`
}

// SpellingSuggestionsIn names a project (empty: anywhere).
type SpellingSuggestionsIn struct {
	ProjectID string `json:"projectId,omitempty"`
}

// SpellingSuggestionsOut lists words people keep fixing the same way.
type SpellingSuggestionsOut struct {
	Suggestions []transcripts.Suggestion `json:"suggestions"`
	Summary     string                   `json:"summary"`
}

// transcriptScope turns ids or names into a scope.
func transcriptScope(ctx context.Context, a *app.App, assetRef, projectRef string, everywhere bool) (transcripts.Scope, string, error) {
	n := 0
	for _, set := range []bool{assetRef != "", projectRef != "" && assetRef == "", everywhere} {
		if set {
			n++
		}
	}
	if n == 0 {
		return transcripts.Scope{}, "", invalid("Say where to look: one clip, a project, or everywhere.")
	}
	if everywhere && (assetRef != "" || projectRef != "") {
		return transcripts.Scope{}, "", invalid("Look everywhere, or in one clip or project, not both.")
	}
	projectID := ""
	if projectRef != "" {
		p, err := resolveProject(ctx, a, projectRef)
		if err != nil {
			return transcripts.Scope{}, "", err
		}
		projectID = p.ID
	}
	if assetRef != "" {
		as, err := findAsset(ctx, a, assetRef, projectID)
		if err != nil {
			return transcripts.Scope{}, "", err
		}
		return transcripts.Scope{AssetID: as.ID}, projectID, nil
	}
	if everywhere {
		return transcripts.Scope{All: true}, "", nil
	}
	return transcripts.Scope{ProjectID: projectID}, projectID, nil
}

// fixErr says a transcript error in plain words.
func fixErr(err error) error {
	var after *transcripts.AfterError
	switch {
	case errors.As(err, &after):
		// A fix of who speaks (or its undo) is in the transcript and its
		// history; only learning the voices again from it failed.
		return apperr.Wrap(apperr.Internal, after.Err, "The change is saved, but the voices couldn't be learned again from it.").
			WithHint("Use Rebuild voice on the People page to learn them again.").WithCommand("metarush people rebuild --all").
			WithDetails(map[string]any{"saved": true})
	case errors.Is(err, transcripts.ErrNoTranscript):
		return apperr.New(apperr.InvalidInput, "This clip has no transcript yet, so there's nothing to fix. It appears once the clip is transcribed.")
	case errors.Is(err, transcripts.ErrNoWords):
		return apperr.New(apperr.InvalidInput, "No words are said there.").WithHint("Pick the words in the transcript, or give the times they are said at.")
	case errors.Is(err, store.ErrNotFound):
		return notFound("asset", "")
	}
	return speakerErr(err)
}

func places(n int) string { return count(n, "place", "places") }
func videos(n int) string { return count(n, "video", "videos") }

func init() {
	Def("transcript.find", "Find words in transcripts (one clip, a project or everywhere): every place with the words around it, to listen to before changing anything", Flags{},
		func(ctx context.Context, a *app.App, in *TranscriptFindIn) (*TranscriptFindOut, error) {
			sc, _, err := transcriptScope(ctx, a, in.AssetID, in.ProjectID, in.Everywhere)
			if err != nil {
				return nil, err
			}
			res, err := transcripts.Find(ctx, a, transcripts.FindInput{Query: in.Query, With: in.With, Scope: sc,
				Options: transcripts.MatchOptions{Parts: in.PartOfWords, MatchCase: in.MatchCase}, Limit: in.Limit})
			if err != nil {
				return nil, err
			}
			out := &TranscriptFindOut{Hits: res.Hits, Total: res.Total, Clips: res.Clips, More: res.More}
			switch {
			case res.Total == 0:
				out.Summary = fmt.Sprintf("%s isn't said anywhere you looked.", q(transcripts.Space(in.Query)))
				if !in.PartOfWords {
					out.Note = "Only whole words were looked for; parts of words would find it inside longer words too."
				}
			case in.With != nil:
				same := 0
				for _, h := range res.Hits {
					if h.Same {
						same++
					}
				}
				out.Summary = fmt.Sprintf("%s → %s would change %s in %s.", q(transcripts.Space(in.Query)), q(transcripts.Space(*in.With)), places(res.Total-same), videos(res.Clips))
			default:
				out.Summary = fmt.Sprintf("%s is said in %s in %s.", q(transcripts.Space(in.Query)), places(res.Total), videos(res.Clips))
			}
			if res.More {
				out.Note = strings.TrimSpace(out.Note + fmt.Sprintf(" The first %d are listed.", len(res.Hits)))
			}
			return out, nil
		})

	Def("transcript.replace", "Change words wherever they are said in transcripts (one clip, a project or everywhere), or only the places you ticked; joined or split words keep their timing", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *TranscriptReplaceIn) (*TranscriptReplaceOut, error) {
			ctx, cancel := keepGoing(ctx)
			defer cancel()
			sc, projectID, err := transcriptScope(ctx, a, in.AssetID, in.ProjectID, in.Everywhere)
			if err != nil {
				return nil, err
			}
			if in.Spelling {
				if why := transcripts.CheckWord(in.With); why != "" {
					return nil, invalid("%s", why)
				}
			}
			res, err := transcripts.Replace(ctx, a, transcripts.ReplaceInput{Query: in.Query, With: in.With, Scope: sc, Only: in.Only, ProjectID: projectID,
				Options: transcripts.MatchOptions{Parts: in.PartOfWords, MatchCase: in.MatchCase}})
			if err != nil {
				return nil, fixErr(err)
			}
			out := &TranscriptReplaceOut{BatchID: res.Batch, Changed: len(res.Fixes), Clips: res.Clips, Fixes: res.Fixes, Gone: res.Gone}
			if out.Fixes == nil {
				out.Fixes = []models.TranscriptFix{}
			}
			if out.Gone == nil {
				out.Gone = []string{}
			}
			if len(out.Fixes) > 200 {
				out.Fixes = out.Fixes[:200]
			}
			from, to := q(transcripts.Space(in.Query)), q(transcripts.Space(in.With))
			if out.Changed == 0 {
				out.Summary = fmt.Sprintf("Nothing changed: %s isn't said that way anywhere you looked.", from)
			} else {
				out.Summary = fmt.Sprintf("Changed %s to %s in %s in %s.", from, to, places(out.Changed), videos(out.Clips))
			}
			if in.Spelling {
				// The list of the settings the project uses (D193; no project: the default's).
				g, r, err := content.Resolve(ctx, a, projectID)
				if err != nil {
					return nil, err
				}
				if r.Owner == "" {
					return nil, apperr.New(apperr.NotConfigured, "No project's settings are used there, so there is no spelling list to put it on.").
						WithCommand("metarush content default --project <project>")
				}
				e, _, err := transcripts.AddEntry(ctx, a, transcripts.AddInput{ProjectID: r.Owner, Word: in.With, HeardAs: []string{in.Query}})
				if err != nil {
					return nil, err
				}
				out.Spelling = e
				list := fmt.Sprintf("the spelling list of %s's settings", q(g.Name(r.Owner)))
				out.Summary += fmt.Sprintf(" %s is on %s now, so new transcripts get it right.", to, list)
			}
			if len(out.Gone) > 0 {
				out.Note = fmt.Sprintf("%s you picked say something else now, so they weren't changed.", places(len(out.Gone)))
			}
			if out.Changed > 0 {
				a.Audit(ctx, "transcript.replace", "transcript", res.Batch, projectID, map[string]any{"query": in.Query, "with": in.With, "changed": out.Changed, "clips": out.Clips})
				if !in.Spelling {
					out.Suggestion = suggestSpelling(ctx, a, projectID, in.With)
				}
			}
			return out, nil
		})

	Def("transcript.fix", "Retype what is said in a stretch of a clip's transcript (the words keep their timing; the machine's transcript is kept, and the fix can be undone)", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *TranscriptFixIn) (*TranscriptFixOut, error) {
			ctx, cancel := keepGoing(ctx)
			defer cancel()
			as, err := findAsset(ctx, a, in.AssetID, "")
			if err != nil {
				return nil, err
			}
			projectID := ""
			if in.ProjectID != "" {
				p, err := resolveProject(ctx, a, in.ProjectID)
				if err != nil {
					return nil, err
				}
				projectID = p.ID
			}
			if len(strings.Fields(in.Text)) > 400 {
				return nil, invalid("Fix up to 400 words at a time.")
			}
			ch, err := transcripts.FixWords(ctx, a, transcripts.FixInput{AssetID: as.ID, From: in.From, To: in.To, Text: in.Text, Expect: in.Expect, ProjectID: projectID})
			if err != nil {
				return nil, fixErr(err)
			}
			out := &TranscriptFixOut{AssetID: as.ID, Rev: ch.Rev, Fixes: ch.Fixes}
			if out.Fixes == nil {
				out.Fixes = []models.TranscriptFix{}
			}
			switch len(ch.Fixes) {
			case 0:
				out.Summary = "Nothing to change: that's what it says already."
			case 1:
				f := ch.Fixes[0]
				out.BatchID = f.BatchID
				if f.AfterText == "" {
					out.Summary = fmt.Sprintf("Took %s out of the transcript of %s (the sound stays).", q(f.BeforeText), q(as.DisplayTitle()))
				} else {
					out.Summary = fmt.Sprintf("Fixed: %s is %s now.", q(f.BeforeText), q(f.AfterText))
				}
			default:
				out.BatchID = ch.Fixes[0].BatchID
				out.Summary = fmt.Sprintf("Fixed %s in the transcript of %s.", places(len(ch.Fixes)), q(as.DisplayTitle()))
			}
			if len(ch.Fixes) > 0 {
				a.Audit(ctx, "transcript.fix", "asset", as.ID, projectID, map[string]any{"fixes": len(ch.Fixes), "rev": ch.Rev})
				out.Suggestion = suggestSpelling(ctx, a, projectID, in.Text)
			}
			return out, nil
		})

	Def("transcript.speaker", "Say who really says a stretch of a clip (\"this part is Lena, not Maya\"); recorded in the clip's transcript history so it can be undone", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *TranscriptSpeakerIn) (*TranscriptSpeakerOut, error) {
			out, fixID, err := assignSpeaker(ctx, a, &in.SpeakerAssignIn, in.ProjectID)
			if err != nil {
				return nil, err
			}
			return &TranscriptSpeakerOut{SpeakerAssignOut: *out, FixID: fixID}, nil
		})

	Def("transcript.history", "A clip's transcript fixes, newest first: who changed what and when, which can still be undone, and the machine's own transcript", Flags{},
		func(ctx context.Context, a *app.App, in *TranscriptHistoryIn) (*TranscriptHistoryOut, error) {
			as, err := findAsset(ctx, a, in.AssetID, "")
			if err != nil {
				return nil, err
			}
			h, err := transcripts.LoadHistory(ctx, a, as, in.Limit)
			if err != nil {
				return nil, err
			}
			var fixes []models.TranscriptFix
			for _, e := range h.Entries {
				fixes = append(fixes, e.TranscriptFix)
			}
			sizes := transcripts.BatchCounts(fixes)
			out := &TranscriptHistoryOut{AssetID: as.ID, Title: as.DisplayTitle(), Rev: h.State.Rev, Base: h.State.Base, Fixes: h.State.Fixes,
				Entries: make([]TranscriptHistoryEntry, 0, len(h.Entries)), Carry: h.State.Carry}
			if h.Original != nil {
				out.Original = &TranscriptOriginal{Provider: h.Original.Provider, Model: h.Original.Model, KeptAt: h.Original.KeptAt.Format("2006-01-02T15:04:05Z")}
			}
			for _, e := range h.Entries {
				out.Entries = append(out.Entries, TranscriptHistoryEntry{HistoryEntry: e, Says: fixSays(e.TranscriptFix), Who: fixWho(e.TranscriptFix), BatchSize: sizes[e.BatchID][0]})
			}
			switch {
			case len(out.Entries) == 0:
				out.Summary = fmt.Sprintf("Nobody has fixed the transcript of %s: it is as the computer wrote it.", q(as.DisplayTitle()))
			case h.State.Fixes == 0:
				out.Summary = fmt.Sprintf("No fixes are in force in %s now (%s in its history).", q(as.DisplayTitle()), count(len(out.Entries), "change", "changes"))
			default:
				out.Summary = fmt.Sprintf("%s in %s; the computer's own transcript is kept.", count(h.State.Fixes, "fix is in force", "fixes are in force"), q(as.DisplayTitle()))
			}
			return out, nil
		})

	Def("transcript.undo", "Undo a transcript fix, or a whole find and replace (every place it changed)", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *TranscriptUndoIn) (*TranscriptUndoOut, error) {
			ctx, cancel := keepGoing(ctx)
			defer cancel()
			res, err := transcripts.Undo(ctx, a, in.FixID, in.BatchID)
			if err != nil {
				return nil, fixErr(err)
			}
			out := &TranscriptUndoOut{Undone: len(res.Undone), Clips: len(res.Changes), Skipped: []TranscriptSkip{}}
			for _, s := range res.Skipped {
				out.Skipped = append(out.Skipped, TranscriptSkip{FixID: s.Fix.ID, AssetID: s.Fix.AssetID, Why: s.Why})
			}
			switch {
			case out.Undone == 0 && len(out.Skipped) > 0:
				out.Summary = "Not undone: " + out.Skipped[0].Why
			case out.Undone == 1 && len(res.Undone[0].AfterText) > 0 && res.Undone[0].Kind == models.FixWords:
				out.Summary = fmt.Sprintf("Undone: it says %s again.", q(res.Undone[0].BeforeText))
			case out.Undone == 1 && res.Undone[0].Kind == models.FixSpeaker && res.Undone[0].BeforeText != "":
				out.Summary = fmt.Sprintf("Undone: %s says those words again.", q(res.Undone[0].BeforeText))
			case out.Undone == 1:
				out.Summary = "Undone."
			default:
				out.Summary = fmt.Sprintf("Undid %s in %s.", count(out.Undone, "fix", "fixes"), videos(out.Clips))
			}
			if out.Undone > 0 && len(out.Skipped) > 0 {
				out.Summary += fmt.Sprintf(" %s stayed: %s", count(len(out.Skipped), "fix", "fixes"), out.Skipped[0].Why)
			}
			if out.Undone > 0 {
				target := in.FixID
				if target == "" {
					target = in.BatchID
				}
				a.Audit(ctx, "transcript.undo", "transcript", target, "", map[string]any{"undone": out.Undone, "skipped": len(out.Skipped)})
			}
			return out, nil
		})

	Def("transcript.spelling.list", "The spelling list a project uses — part of its Content Settings (its own, another project's or the default's): how names and terms are always written", Flags{},
		func(ctx context.Context, a *app.App, in *SpellingListIn) (*SpellingListOut, error) {
			pid, err := projectRefID(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			g, r, err := content.Resolve(ctx, a, pid)
			if err != nil {
				return nil, err
			}
			out := &SpellingListOut{ProjectID: pid, Entries: []models.SpellingEntry{}, From: refOf(g, r.Owner), ReadOnly: pid != "" && r.Owner != pid}
			if r.Owner != "" {
				list, err := transcripts.LoadList(ctx, a, r.Owner)
				if err != nil {
					return nil, err
				}
				if list != nil {
					out.Entries = list
				}
				for _, id := range g.UsedBy(r.Owner) {
					if id != pid {
						out.UsedBy++
					}
				}
			}
			n := len(out.Entries)
			switch {
			case r.Owner == "":
				out.Summary = "No project's settings are used here yet, so there is no spelling list."
			case n == 0:
				out.Summary = fmt.Sprintf("The spelling list of %s's settings is empty. Add the names and terms the transcripts get wrong; new transcripts are corrected by it.", q(g.Name(r.Owner)))
			default:
				out.Summary = fmt.Sprintf("%s on the spelling list of %s's settings.", count(n, "word", "words"), q(g.Name(r.Owner)))
			}
			if out.ReadOnly {
				out.Summary += fmt.Sprintf(" %s uses them: change the list there, or give %s its own settings.", q(g.Name(pid)), q(g.Name(pid)))
			}
			return out, nil
		})

	Def("transcript.spelling.add", "Put a word on the spelling list of a project's own Content Settings (\"always write FernBrook\"; without a project, the default's): new transcripts are corrected by it and the transcriber is told to expect it", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *SpellingAddIn) (*SpellingAddOut, error) {
			ctx, cancel := keepGoing(ctx)
			defer cancel()
			pid, err := projectRefID(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			t, g, err := content.EditTarget(ctx, a, pid)
			if err != nil {
				return nil, err
			}
			e, created, err := transcripts.AddEntry(ctx, a, transcripts.AddInput{ProjectID: t.ProjectID, Word: in.Word, HeardAs: in.HeardAs, Note: in.Note})
			if err != nil {
				return nil, err
			}
			out := &SpellingAddOut{Entry: *e, Created: created}
			list := fmt.Sprintf("the spelling list of %s's settings", q(t.ProjectName))
			sc, err := settingsScope(ctx, a, t.ProjectID)
			if err != nil {
				return nil, err
			}
			if created {
				out.Summary = fmt.Sprintf("%s is on %s.", q(e.Word), list)
			} else {
				out.Summary = fmt.Sprintf("%s was on %s already; it's written that way now.", q(e.Word), list)
			}
			if note := t.Note(g); note != "" {
				out.Summary += " " + note
			}
			res, err := spellingApply(ctx, a, sc, []models.SpellingEntry{*e}, !in.FixExisting, nil)
			if err != nil {
				return nil, err
			}
			if in.FixExisting {
				out.Fixed, out.Clips, out.BatchID = res.Changed, res.Clips, res.BatchID
				if res.Changed > 0 {
					out.Summary += fmt.Sprintf(" Fixed %s in %s.", places(res.Changed), videos(res.Clips))
				}
			} else if out.Waiting = res.Total; res.Total > 0 {
				out.Summary += fmt.Sprintf(" It's written another way in %s in %s; fixing those is one more step.", places(res.Total), videos(res.Clips))
			}
			a.Audit(ctx, "transcript.spelling.add", "spelling", e.ID, t.ProjectID, map[string]any{"word": e.Word, "created": created, "fixed": out.Fixed})
			closeSpellingQuestion(ctx, a, pid, e.Key)
			if pid != t.ProjectID {
				closeSpellingQuestion(ctx, a, t.ProjectID, e.Key)
			}
			return out, nil
		})

	Def("transcript.spelling.remove", "Take a word off the spelling list of a project's own Content Settings (without a project, the default's); transcripts it corrected stay as they are", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *SpellingRemoveIn) (*SpellingRemoveOut, error) {
			ctx, cancel := keepGoing(ctx)
			defer cancel()
			pid, err := projectRefID(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			t, g, err := content.EditTarget(ctx, a, pid)
			if err != nil {
				return nil, err
			}
			e, err := transcripts.RemoveEntry(ctx, a, t.ProjectID, in.Word)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return nil, apperr.New(apperr.NotFound, "%s isn't on the spelling list of %s's settings.", q(transcripts.Space(in.Word)), q(t.ProjectName))
				}
				return nil, err
			}
			a.Audit(ctx, "transcript.spelling.remove", "spelling", e.ID, t.ProjectID, map[string]any{"word": e.Word})
			s := fmt.Sprintf("%s is off the spelling list of %s's settings. Transcripts it corrected stay as they are.", q(e.Word), q(t.ProjectName))
			if note := t.Note(g); note != "" {
				s += " " + note
			}
			return &SpellingRemoveOut{Removed: *e, Summary: s}, nil
		})

	Def("transcript.spelling.apply", "Correct existing transcripts by the spelling lists (one clip, a project, every clip using a project's settings, or everywhere); dryRun only lists the places", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *SpellingApplyIn) (*SpellingApplyOut, error) {
			ctx, cancel := keepGoing(ctx)
			defer cancel()
			var sc transcripts.Scope
			var err error
			if strings.TrimSpace(in.SettingsOf) != "" {
				if in.AssetID != "" || in.ProjectID != "" || in.Everywhere {
					return nil, invalid("Correct the clips using one project's settings, or one clip, project or everywhere — not both.")
				}
				pid, err := projectRefID(ctx, a, in.SettingsOf)
				if err != nil {
					return nil, err
				}
				_, r, err := content.Resolve(ctx, a, pid)
				if err != nil {
					return nil, err
				}
				if sc, err = settingsScope(ctx, a, r.Owner); err != nil {
					return nil, err
				}
			} else if sc, _, err = transcriptScope(ctx, a, in.AssetID, in.ProjectID, in.Everywhere); err != nil {
				return nil, err
			}
			res, err := spellingApply(ctx, a, sc, nil, in.DryRun, in.Only)
			if err != nil {
				return nil, err
			}
			switch {
			case res.Total == 0 && res.Changed == 0:
				res.Summary = "The transcripts already follow the spelling lists."
			case in.DryRun:
				res.Summary = fmt.Sprintf("The spelling lists would change %s in %s.", places(res.Total), videos(res.Clips))
			default:
				res.Summary = fmt.Sprintf("Corrected %s in %s by the spelling lists.", places(res.Changed), videos(res.Clips))
				a.Audit(ctx, "transcript.spelling.apply", "transcript", res.BatchID, in.ProjectID, map[string]any{"changed": res.Changed, "clips": res.Clips})
			}
			return res, nil
		})

	Def("transcript.spelling.suggestions", "Words people keep fixing the same way that aren't on a spelling list yet", Flags{},
		func(ctx context.Context, a *app.App, in *SpellingSuggestionsIn) (*SpellingSuggestionsOut, error) {
			projectID := ""
			if in.ProjectID != "" {
				p, err := resolveProject(ctx, a, in.ProjectID)
				if err != nil {
					return nil, err
				}
				projectID = p.ID
			}
			s, err := transcripts.Suggestions(ctx, a, projectID)
			if err != nil {
				return nil, err
			}
			out := &SpellingSuggestionsOut{Suggestions: s}
			if out.Suggestions == nil {
				out.Suggestions = []transcripts.Suggestion{}
			}
			if len(s) == 0 {
				out.Summary = "Nothing to suggest: no word was fixed the same way again and again."
			} else {
				out.Summary = fmt.Sprintf("%s fixed the same way again and again could go on the spelling list.", count(len(s), "word", "words"))
			}
			return out, nil
		})
}

// settingsScope is every clip using the spelling list of a project's own
// settings: the projects using them, and footage in no project when they are
// the default's (D193).
func settingsScope(ctx context.Context, a *app.App, owner string) (transcripts.Scope, error) {
	if owner == "" {
		return transcripts.Scope{}, invalid("No project's settings are used there, so there is no spelling list to correct by.")
	}
	users, inbox, err := contentUsers(ctx, a, owner)
	if err != nil {
		return transcripts.Scope{}, err
	}
	return transcripts.Scope{Projects: users, NoProject: inbox}, nil
}

// keepGoing lets a change finish once it has started, even when the person
// who asked closes the page or the connection drops: words changed without
// the record of the fix, or a new version without its review copy, would be
// worse than finishing (D145).
func keepGoing(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
}

// assignSpeaker is footage.speakers.assign and transcript.speaker: the fix,
// recorded in the clip's transcript history.
func assignSpeaker(ctx context.Context, a *app.App, in *SpeakerAssignIn, projectRef string) (*SpeakerAssignOut, string, error) {
	ctx, cancel := keepGoing(ctx)
	defer cancel()
	if in.To <= in.From {
		return nil, "", invalid("The end must be after the start.")
	}
	if !in.Unsure && strings.TrimSpace(in.Person) == "" && in.PersonID == "" {
		return nil, "", invalid("Say who it is, or that you're not sure.")
	}
	projectID := ""
	if projectRef != "" {
		p, err := resolveProject(ctx, a, projectRef)
		if err != nil {
			return nil, "", err
		}
		projectID = p.ID
	}
	pin := speakers.AssignInput{AssetID: in.AssetID, From: in.From, To: in.To, Unsure: in.Unsure, NewPerson: in.NewPerson, Name: in.Person, PersonID: in.PersonID}
	if !in.Unsure && !in.NewPerson && in.PersonID == "" {
		// A name means someone already named (a unique start is enough); new people need newPerson.
		p, err := resolvePerson(ctx, a, in.Person)
		if err != nil {
			return nil, "", err
		}
		pin.PersonID = p.ID
	}
	res, fix, err := transcripts.AssignSpeaker(ctx, a, pin, projectID)
	if err != nil {
		return nil, "", fixErr(err)
	}
	sp, err := speakersOut(ctx, a, in.AssetID)
	if err != nil {
		return nil, "", err
	}
	all, _ := speakers.Voices(ctx, a, in.AssetID, true)
	out := &SpeakerAssignOut{Speakers: *sp, Voice: speakerRows([]models.AssetSpeaker{res.Voice}, all)[0], Moved: res.Moved, From: res.From, Relearned: res.Relearned,
		Dropped: []speakers.ClipVoice{}, Summary: res.Describe()}
	if res.Library != nil {
		out.Dropped = res.Library.Dropped
	}
	if !res.Relearned {
		out.Note = "Voice matching is not installed on this machine, so the voices were not learned again from these words."
	}
	afterSpeakerChange(ctx, a, res.Library)
	fixID := ""
	if fix != nil {
		fixID = fix.ID
		out.Summary += " It can be undone from the clip's transcript history."
	}
	return out, fixID, nil
}

// spellingApply corrects (or, dry, lists) the places in a scope the entries
// would change (nil entries: each clip's own lists).
func spellingApply(ctx context.Context, a *app.App, sc transcripts.Scope, entries []models.SpellingEntry, dry bool, only []string) (*SpellingApplyOut, error) {
	out := &SpellingApplyOut{Hits: []transcripts.Hit{}}
	clips, err := transcripts.Clips(ctx, a, sc)
	if err != nil {
		return nil, err
	}
	var pick map[string]bool
	if len(only) > 0 {
		pick = map[string]bool{}
		for _, id := range only {
			pick[id] = true
		}
	}
	batch := ""
	for i := range clips {
		as := &clips[i]
		es := entries
		if es == nil {
			if es, err = transcripts.ListsFor(ctx, a, as.ProjectIDs); err != nil {
				return nil, err
			}
		}
		if len(es) == 0 {
			continue
		}
		if dry {
			segs, err := transcripts.LoadSegments(ctx, a, as.ID)
			if err != nil {
				return nil, err
			}
			hits := transcripts.SpellingHits(as, segs, es)
			if len(hits) > 0 {
				out.Clips++
				out.Total += len(hits)
				for _, h := range hits {
					if len(out.Hits) < 300 {
						out.Hits = append(out.Hits, h)
					}
				}
			}
			continue
		}
		var n int
		ch, err := transcripts.Edit(ctx, a, as.ID, transcripts.EditOptions{Batch: batch, ProjectID: sc.ProjectID}, func(c *transcripts.Clip) error {
			n = len(c.ApplySpelling(es, pick))
			return nil
		})
		if err != nil {
			if errors.Is(err, transcripts.ErrNoTranscript) {
				continue
			}
			return nil, err
		}
		if n > 0 {
			out.Changed += n
			out.Clips++
			if batch == "" && len(ch.Fixes) > 0 {
				batch = ch.Fixes[0].BatchID
			}
		}
	}
	out.BatchID = batch
	return out, nil
}

// suggestSpelling asks (once, as a question in the To-dos) whether a word
// people keep fixing the same way should go on the project's spelling list,
// and returns the suggestion for the words just written, if any.
func suggestSpelling(ctx context.Context, a *app.App, projectID, written string) *transcripts.Suggestion {
	if projectID == "" {
		return nil
	}
	sugs, err := transcripts.Suggestions(ctx, a, projectID)
	if err != nil || len(sugs) == 0 {
		return nil
	}
	var p models.Project
	_ = a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), projectID, &p)
	var mine *transcripts.Suggestion
	for i := range sugs {
		s := sugs[i]
		question := fmt.Sprintf("You fixed %s %s. Add %s to the spelling list?", q(firstOf(s.Heard, s.Word)), count(s.Times, "time", "times"), q(s.Word))
		if s.Times < 3 {
			question = fmt.Sprintf("You fixed %s in %s. Add %s to the spelling list?", q(firstOf(s.Heard, s.Word)), places(s.Places), q(s.Word))
		}
		_, _ = todos.Upsert(ctx, a, models.Todo{Kind: models.TodoSpelling, Scope: "project", ProjectID: projectID, ProjectIDs: []string{projectID},
			GroupKey: spellingQuestion(projectID, s.Key), Question: question,
			Detail:      "New transcripts in this project will write it that way, and the transcriber is told to expect it.",
			Context:     models.TodoContext{ProjectName: p.Name, Field: s.Word},
			Suggestions: []models.TodoSuggestion{{Label: "Add " + q(s.Word) + " to the spelling list", Value: "add"}}, Priority: 3})
		if mine == nil && strings.Contains(transcripts.Norm(written), s.Key) {
			mine = &sugs[i]
		}
	}
	return mine
}

func firstOf(xs []string, def string) string {
	if len(xs) > 0 {
		return xs[0]
	}
	return def
}

func spellingQuestion(projectID, key string) string { return "spelling:" + projectID + ":" + key }

// closeSpellingQuestion closes the question about a word once it is on a list.
func closeSpellingQuestion(ctx context.Context, a *app.App, projectID, key string) {
	if projectID != "" {
		_, _ = todos.Close(ctx, a, spellingQuestion(projectID, key), models.ResolvedElsewhere, "")
		return
	}
	_, _ = todos.CloseStale(ctx, a, "spelling:", nil)
}

// fixSays says a fix in plain words.
func fixSays(f models.TranscriptFix) string {
	switch f.Kind {
	case models.FixSpeaker:
		return fmt.Sprintf("Who says %s–%s: %s → %s", at(f.From), at(f.To), q(f.BeforeText), q(f.AfterText))
	case models.FixTranscribed:
		return "Transcribed again"
	}
	switch {
	case f.AfterText == "":
		return fmt.Sprintf("%s taken out at %s", q(f.BeforeText), at(f.From))
	default:
		return fmt.Sprintf("%s → %s at %s", q(f.BeforeText), q(f.AfterText), at(f.From))
	}
}

// fixWho says who made a fix.
func fixWho(f models.TranscriptFix) string {
	switch f.Source {
	case models.FixBySpelling:
		return "the spelling list"
	case models.FixCarried:
		return "carried over"
	case models.FixByMachine:
		return "MetaRush"
	}
	if f.By.UserName != "" {
		return f.By.UserName
	}
	if f.By.Via == models.ViaAssistant {
		return "Claude"
	}
	return "someone"
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	Offer("transcript.replace", Offering{ID: "find-and-replace", Group: "edit", Title: "Find and replace in transcripts",
		Does:  "Changes words wherever they are said; each keeps its timing, and it can be undone.",
		Words: []string{"fix a misspelled name", "correct the transcript"}})
}
