package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/transcripts"
)

// Fixes queue up; renders don't pile up (D148): a saved version keeps the
// words it was made with; fixes made since wait until someone makes one new
// version (and one review copy) for all of them. Nothing renders by itself.
// Captions files follow the fixed words without a new render.

// TranscriptPendingIn names a cut: a project's latest version (on a branch), or one version.
type TranscriptPendingIn struct {
	ProjectID string `json:"projectId" jsonschema:"description=The project (ID or name)."`
	Branch    string `json:"branch,omitempty"`
	Revision  string `json:"revision,omitempty" jsonschema:"description=A version (its ID or rN); default the latest."`
}

// TranscriptPendingOut lists the fixes waiting for a new version.
type TranscriptPendingOut struct {
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	RevisionID  string `json:"revisionId,omitempty"`
	Number      int    `json:"number,omitempty"`
	Branch      string `json:"branch,omitempty"`
	// Latest: the version is the newest on its branch (a new version can be made from it).
	Latest bool `json:"latest"`
	// Count is the fixes waiting; Transcribed the clips transcribed again since.
	Count       int                      `json:"count"`
	Transcribed []string                 `json:"transcribed"`
	Clips       int                      `json:"clips"`
	Places      []transcripts.PendingFix `json:"places"`
	Summary     string                   `json:"summary"`
	Note        string                   `json:"note,omitempty"`
}

// TranscriptPendingApplyIn makes one new version with the fixes (and one review copy).
type TranscriptPendingApplyIn struct {
	ProjectID string `json:"projectId"`
	Branch    string `json:"branch,omitempty"`
	// NoRender skips the review copy.
	NoRender bool   `json:"noRender,omitempty"`
	Preset   string `json:"preset,omitempty" jsonschema:"enum=vertical-1080p,enum=landscape-1080p,enum=square-1080"`
}

// TranscriptPendingApplyOut is the new version (and its review copy).
type TranscriptPendingApplyOut struct {
	Revision RevisionSummary `json:"revision"`
	Fixes    int             `json:"fixes"`
	RenderID string          `json:"renderId,omitempty"`
	URL      string          `json:"url"`
	Summary  string          `json:"summary"`
}

// CaptionsCheckIn names a version (default the project's latest).
type CaptionsCheckIn struct {
	ProjectID string `json:"projectId"`
	Branch    string `json:"branch,omitempty"`
	Revision  string `json:"revision,omitempty" jsonschema:"description=A version (its ID or rN); default the latest."`
}

// CaptionsCheckOut is the quick review list of a version's captions.
type CaptionsCheckOut struct {
	RevisionID string                     `json:"revisionId"`
	Number     int                        `json:"number"`
	Shows      bool                       `json:"shows" jsonschema:"description=The video draws captions (otherwise the list is about its captions file)."`
	Issues     []transcripts.CaptionIssue `json:"issues"`
	Summary    string                     `json:"summary"`
	Note       string                     `json:"note,omitempty"`
}

// CaptionsFileIn asks for a version's captions file.
type CaptionsFileIn struct {
	ProjectID string `json:"projectId"`
	Branch    string `json:"branch,omitempty"`
	Revision  string `json:"revision,omitempty" jsonschema:"description=A version (its ID or rN); default the latest."`
	Format    string `json:"format,omitempty" jsonschema:"enum=srt,enum=vtt,description=srt (default) or vtt."`
	// Words: "now" (default) has every fix made since the version; "version"
	// has the words the version was made with (as its video shows them).
	Words string `json:"words,omitempty" jsonschema:"enum=now,enum=version"`
	// Language: the captions translated into it (D238; a code such as es).
	Language string `json:"language,omitempty" jsonschema:"description=The captions in another language they are translated into (es\\, pt-BR); default the captions' own."`
	// MarkChanged shows words said in a speaker's voice copy in place of what
	// they said in italics (D311). A WebVTT file always names them in a note.
	MarkChanged bool `json:"markChanged,omitempty" jsonschema:"description=Show words changed with AI in the speaker's own voice copy in italics."`
}

// CaptionsFileOut is the file's name and text.
type CaptionsFileOut struct {
	RevisionID string `json:"revisionId"`
	Number     int    `json:"number"`
	Format     string `json:"format"`
	Words      string `json:"words"`
	FileName   string `json:"fileName"`
	Content    string `json:"content"`
	Lines      int    `json:"lines"`
	// Fixes is how many fixes made since the version the file has (words now).
	Fixes int `json:"fixes"`
	// BurnedIn: the video draws captions too (they keep the version's words).
	BurnedIn bool `json:"burnedIn"`
	// Language and the rest: a file in another language (D238).
	Language          string `json:"language,omitempty"`
	LanguageName      string `json:"languageName,omitempty"`
	MachineTranslated bool   `json:"machineTranslated,omitempty"`
	Untranslated      int    `json:"untranslated,omitempty" jsonschema:"description=Lines not translated yet (in the original language in the file)."`
	Outdated          int    `json:"outdated,omitempty" jsonschema:"description=Lines changed in the transcript since they were translated (their old translation is used)."`
	Summary           string `json:"summary"`
	Note              string `json:"note,omitempty"`
}

// DeliverCaptionsIn names delivered videos whose captions files to update.
type DeliverCaptionsIn struct {
	Path string `json:"path" jsonschema:"description=A delivered video's .json file or the video itself\\, or the folder they are in (every delivered video in it)."`
}

// DeliverCaptionsFile is one video's updated captions files.
type DeliverCaptionsFile struct {
	Video      string   `json:"video"`
	Files      []string `json:"files"`
	RevisionID string   `json:"revisionId"`
	Number     int      `json:"number"`
	Fixes      int      `json:"fixes"`
	BurnedIn   bool     `json:"burnedIn"`
}

// DeliverCaptionsOut says which captions files were updated.
type DeliverCaptionsOut struct {
	Files   []DeliverCaptionsFile `json:"files"`
	Skipped []string              `json:"skipped"`
	// LeftAlone are files next to the videos that this delivery didn't make:
	// they are never written over (a person's own captions file).
	LeftAlone []string `json:"leftAlone,omitempty"`
	Summary   string   `json:"summary"`
	Note      string   `json:"note,omitempty"`
}

// pendingSummary says what waits for a new version, in plain words.
func pendingSummary(p *transcripts.Pending, number int, title string) (count int, transcribed []string, summary string) {
	transcribed = []string{}
	for _, f := range p.Fixes {
		if f.Kind == models.FixTranscribed {
			if !slices.Contains(transcribed, f.Clip) {
				transcribed = append(transcribed, f.Clip)
			}
			continue
		}
		count++
	}
	cut := fmt.Sprintf("version %d of %s", number, q(title))
	switch {
	case count == 0 && len(transcribed) == 0:
		summary = fmt.Sprintf("Nothing is waiting: %s has every text fix.", cut)
	case count == 0:
		summary = fmt.Sprintf("%s was transcribed again since %s.", q(andList(transcribed)), cut)
	default:
		summary = fmt.Sprintf("%s since %s.", count1(count, "text fix", "text fixes"), cut)
		if len(transcribed) > 0 {
			summary = fmt.Sprintf("%s since %s, and %s was transcribed again.", count1(count, "text fix", "text fixes"), cut, q(andList(transcribed)))
		}
	}
	return count, transcribed, summary
}

func count1(n int, one, many string) string {
	s := count(n, one, many)
	return strings.ToUpper(s[:1]) + s[1:]
}

// cutTitle is a cut's title for a sentence.
func cutTitle(r *models.Revision, p *models.Project) string {
	if t := strings.TrimSpace(r.Spec.Title); t != "" {
		return t
	}
	return p.Name
}

// captionCues makes the lines of a version's captions file, with the words the
// version was made with (pinned) or with today's.
func captionCues(ctx context.Context, a *app.App, r *models.Revision, pinned bool) ([]transcripts.Cue, *editspec.RenderPlan, error) {
	spec := transcripts.CaptionsSpec(&r.Spec)
	var pins *transcripts.Pins
	if pinned {
		pins = transcripts.PinsOf(r)
	}
	facts, err := loadFactsPinned(ctx, a, r.ProjectID, spec, pins)
	if err != nil {
		return nil, nil, err
	}
	plan := editspec.Resolve(spec, facts, r.ID, r.ProjectID, r.SpecHash)
	return transcripts.Cues(plan.Captions, plan.FPS), plan, nil
}

// burnedIn reports whether a version's video draws its captions.
func burnedIn(r *models.Revision) bool { return r.Spec.Captions != nil && r.Spec.Captions.Enabled }

func init() {
	Def("transcript.pending", "Text fixes made since a version of a cut that touch its words: they wait for a new version (nothing renders by itself)", Flags{},
		func(ctx context.Context, a *app.App, in *TranscriptPendingIn) (*TranscriptPendingOut, error) {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			r, head, err := resolveRevision(ctx, a, p, in.Revision, in.Branch)
			if err != nil {
				return nil, err
			}
			out := &TranscriptPendingOut{ProjectID: p.ID, ProjectName: p.Name, Places: []transcripts.PendingFix{}, Transcribed: []string{}}
			if r == nil {
				out.Summary = fmt.Sprintf("%s has no cut yet, so no fixes are waiting.", q(p.Name))
				return out, nil
			}
			out.RevisionID, out.Number, out.Branch, out.Latest = r.ID, r.Number, r.Branch, r.ID == head
			pend, err := transcripts.PendingFor(ctx, a, r)
			if err != nil {
				return nil, err
			}
			out.Places, out.Clips = pend.Fixes, pend.Clips
			if len(out.Places) > 300 {
				out.Places = out.Places[:300]
			}
			out.Count, out.Transcribed, out.Summary = pendingSummary(pend, r.Number, p.Name)
			if out.Count > 0 || len(out.Transcribed) > 0 {
				if out.Latest {
					out.Note = "Make a new version and a review copy to use them; the version you have keeps its words, and nothing renders by itself."
				} else {
					out.Note = "This isn't the latest version; a new version is made from the latest one."
				}
			}
			return out, nil
		})

	Def("transcript.pending.apply", "Make one new version of a cut with every text fix made since its latest version, and one review copy (instead of rendering after each fix)", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *TranscriptPendingApplyIn) (*TranscriptPendingApplyOut, error) {
			ctx, cancel := keepGoing(ctx)
			defer cancel()
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			r, _, err := resolveRevision(ctx, a, p, "", in.Branch)
			if err != nil {
				return nil, err
			}
			if r == nil {
				return nil, invalid("%s has no cut yet.", q(p.Name))
			}
			pend, err := transcripts.PendingFor(ctx, a, r)
			if err != nil {
				return nil, err
			}
			n, transcribed, _ := pendingSummary(pend, r.Number, p.Name)
			if n == 0 && len(transcribed) == 0 {
				return nil, apperr.New(apperr.InvalidInput, "Version %d of %s already has every text fix; there's no new version to make.", r.Number, q(p.Name))
			}
			spec := editspec.Clone(&r.Spec)
			editspec.Normalize(spec)
			facts, err := loadFacts(ctx, a, p.ID, spec)
			if err != nil {
				return nil, err
			}
			instruction := fmt.Sprintf("Text fixes: %s", count(n, "fix", "fixes"))
			for _, f := range pend.Fixes {
				if f.Kind == models.FixWords && f.AfterText != "" {
					instruction = fmt.Sprintf("Text fixes: “%s” → “%s”", f.BeforeText, f.AfterText)
					if n > 1 {
						instruction += fmt.Sprintf(" and %d more", n-1)
					}
					break
				}
			}
			// The same cut with the text fixed: its changed words stay as they were, in their place (D308).
			if err := saidAllowed(ctx, a, p.ID, &r.Spec, spec); err != nil {
				return nil, err
			}
			prep := &prepared{plan: editspec.Plan{BaseRevisionID: r.ID, Instruction: instruction, Origin: "transcript-fixes:" + a.Actor.Via},
				project: *p, branch: r.Branch, head: r.ID, base: r, parentID: r.ID, spec: spec, facts: facts, errors: []editspec.Issue{}, warnings: []editspec.Issue{}}
			for _, is := range editspec.Validate(spec, facts, &r.Spec, false) {
				if is.Severity == "error" {
					prep.errors = append(prep.errors, is)
				} else {
					prep.warnings = append(prep.warnings, is)
				}
			}
			if len(prep.errors) > 0 {
				return nil, apperr.New(apperr.Validation, "The cut can't be saved again as it is: %s", prep.errors[0].Message).
					WithHint("Fix that in the cut, then make the new version.").WithDetails(map[string]any{"errors": prep.errors})
			}
			d := editspec.Compare(&r.Spec, spec)
			prep.diff = &d
			saved, err := commitRevision(ctx, a, prep)
			if err != nil {
				return nil, err
			}
			out := &TranscriptPendingApplyOut{Revision: saved.Revision, Fixes: n, URL: saved.URL}
			out.Summary = fmt.Sprintf("Saved version %d of %s with %s.", saved.Revision.Number, q(p.Name), count(n, "text fix", "text fixes"))
			if n == 0 {
				out.Summary = fmt.Sprintf("Saved version %d of %s with the new transcripts.", saved.Revision.Number, q(p.Name))
			}
			if !in.NoRender {
				rd, err := CallTyped[RenderOut](ctx, a, "render.submit", RenderSubmitIn{RevisionID: saved.Revision.ID, Quality: "review", Preset: in.Preset}, callerOf(ctx))
				if err != nil {
					out.Summary += " The review copy didn't start: " + plainErr(err)
				} else {
					out.RenderID = rd.Render.ID
					out.Summary += " A review copy is on its way."
				}
			}
			a.Audit(ctx, "transcript.pending.apply", "revision", saved.Revision.ID, p.ID, map[string]any{"fixes": n, "from": r.ID, "render": out.RenderID})
			return out, nil
		})

	Def("captions.check", "Check a version's captions before it goes out: words the computer wasn't sure of, names not on the spelling lists, words fixed since the version", Flags{},
		func(ctx context.Context, a *app.App, in *CaptionsCheckIn) (*CaptionsCheckOut, error) {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			r, _, err := resolveRevision(ctx, a, p, in.Revision, in.Branch)
			if err != nil {
				return nil, err
			}
			if r == nil {
				return nil, invalid("%s has no cut yet.", q(p.Name))
			}
			_, plan, err := captionCues(ctx, a, r, true)
			if err != nil {
				return nil, err
			}
			issues, err := transcripts.CheckCaptions(ctx, a, plan, []string{p.ID})
			if err != nil {
				return nil, err
			}
			out := &CaptionsCheckOut{RevisionID: r.ID, Number: r.Number, Shows: burnedIn(r), Issues: issues}
			if len(issues) == 0 {
				out.Summary = fmt.Sprintf("The captions of version %d look right: no unsure words, and every name is on the spelling list.", r.Number)
			} else {
				out.Summary = fmt.Sprintf("%s to look at in the captions of version %d.", count1(len(issues), "word", "words"), r.Number)
			}
			if !out.Shows {
				out.Note = "The video doesn't draw captions; this is about its captions file."
			}
			return out, nil
		})

	Def("captions.file", "A version's captions as a file (SubRip or WebVTT), with every text fix made since (no new render), or with the version's own words", Flags{},
		func(ctx context.Context, a *app.App, in *CaptionsFileIn) (*CaptionsFileOut, error) {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			r, _, err := resolveRevision(ctx, a, p, in.Revision, in.Branch)
			if err != nil {
				return nil, err
			}
			if r == nil {
				return nil, invalid("%s has no cut yet.", q(p.Name))
			}
			format := strings.ToLower(in.Format)
			if format == "" {
				format = "srt"
			}
			words := in.Words
			if words == "" {
				words = "now"
			}
			cues, _, err := captionCues(ctx, a, r, words == "version")
			if err != nil {
				return nil, err
			}
			out := &CaptionsFileOut{RevisionID: r.ID, Number: r.Number, Format: format, Words: words, Lines: len(cues), BurnedIn: burnedIn(r)}
			out.FileName = fmt.Sprintf("%s_r%d.%s", orSlug(slug(cutTitle(r, p), 48), "cut"), r.Number, format)
			if in.Language != "" {
				return translatedFileOut(ctx, a, p, r, in.Language, words, format, out)
			}
			if in.MarkChanged {
				cues = transcripts.Marked(cues)
			}
			if format == "vtt" {
				out.Content = transcripts.VTTWithNote(cues, changedNoteOf(ctx, a, r))
			} else {
				out.Content = transcripts.SRT(cues)
			}
			if words == "now" {
				if pend, err := transcripts.PendingFor(ctx, a, r); err == nil {
					out.Fixes, _, _ = pendingSummary(pend, r.Number, "")
				}
			}
			switch {
			case len(cues) == 0:
				out.Summary = fmt.Sprintf("Version %d has no words to caption.", r.Number)
			case words == "now" && out.Fixes > 0:
				out.Summary = fmt.Sprintf("Captions file for version %d with %s made since.", r.Number, count(out.Fixes, "text fix", "text fixes"))
			default:
				out.Summary = fmt.Sprintf("Captions file for version %d.", r.Number)
			}
			if out.BurnedIn && out.Fixes > 0 && words == "now" {
				out.Note = "The captions drawn into the video still have the old words; make a new version for those."
			}
			return out, nil
		})

	Def("deliver.captions", "Update the captions files of delivered videos with the text fixes made since (no new render; captions drawn into the video need a new version)", Flags{Mutating: true, LocalOnly: true},
		func(ctx context.Context, a *app.App, in *DeliverCaptionsIn) (*DeliverCaptionsOut, error) {
			ctx, cancel := keepGoing(ctx)
			defer cancel()
			sidecars, err := deliveredSidecars(in.Path)
			if err != nil {
				return nil, err
			}
			out := &DeliverCaptionsOut{Files: []DeliverCaptionsFile{}, Skipped: []string{}}
			anyBurned := false
			total := 0
			outdated, outdatedIn := 0, []string{} // translated lines fixed since (D238)
			for _, side := range sidecars {
				b, err := os.ReadFile(side)
				if err != nil {
					out.Skipped = append(out.Skipped, filepath.Base(side))
					continue
				}
				var sc DeliverySidecar
				if json.Unmarshal(b, &sc) != nil || sc.Format != "metarush.delivery/1" || sc.Revision.ID == "" {
					out.Skipped = append(out.Skipped, filepath.Base(side))
					continue
				}
				r, _, err := getRevision(ctx, a, sc.Revision.ID)
				if err != nil {
					out.Skipped = append(out.Skipped, filepath.Base(side))
					continue
				}
				if !transcripts.CaptionFileAllowed(r.Spec.Output.Platform) && sc.Captions == nil {
					out.Skipped = append(out.Skipped, filepath.Base(side))
					continue
				}
				cues, _, err := captionCues(ctx, a, r, false)
				if err != nil {
					return nil, err
				}
				if len(cues) == 0 {
					out.Skipped = append(out.Skipped, filepath.Base(side))
					continue
				}
				dir, base := filepath.Dir(side), strings.TrimSuffix(filepath.Base(side), ".json")
				// Only the files this delivery's record lists are written over;
				// any other file is made only where nothing is (D238): a person's
				// own file of the same name is left as it is, and said.
				owned := map[string]bool{}
				if old := sc.Captions; old != nil {
					for _, f := range old.Files {
						owned[f] = true
					}
					for _, t := range old.Translations {
						for _, f := range t.Files {
							owned[f] = true
						}
					}
				}
				srt, vtt := filepath.Join(dir, base+".srt"), filepath.Join(dir, base+".vtt")
				wrote, err := writeOwned(owned, []byte(transcripts.SRT(cues)), srt, []byte(transcripts.VTTWithNote(cues, changedNoteOf(ctx, a, r))), vtt)
				if err != nil {
					return nil, err
				}
				if !wrote {
					out.LeftAlone = append(out.LeftAlone, filepath.Base(srt), filepath.Base(vtt))
					out.Skipped = append(out.Skipped, filepath.Base(side))
					continue
				}
				fixes := 0
				if pend, err := transcripts.PendingFor(ctx, a, r); err == nil {
					fixes, _, _ = pendingSummary(pend, r.Number, "")
				}
				now := time.Now().UTC()
				sc.Captions = &DeliveryCaptions{Files: []string{filepath.Base(srt), filepath.Base(vtt)}, Words: "now", BurnedIn: burnedIn(r), Fixes: fixes, UpdatedAt: &now}
				// Captions in other languages follow the fixes too (D238), and
				// languages translated since the delivery join it.
				langs, err := projectLanguages(ctx, a, r.ProjectID)
				if err != nil {
					return nil, err
				}
				files := []string{filepath.Base(srt), filepath.Base(vtt)}
				for _, l := range langs {
					tf, err := translatedCaptions(ctx, a, r, l, false)
					if err != nil {
						return nil, err
					}
					if tf == nil {
						continue
					}
					lsrt, lvtt := filepath.Join(dir, base+"."+l+".srt"), filepath.Join(dir, base+"."+l+".vtt")
					wrote, err := writeOwned(owned, []byte(tf.SRT), lsrt, []byte(tf.VTT), lvtt)
					if err != nil {
						return nil, err
					}
					if !wrote {
						out.LeftAlone = append(out.LeftAlone, filepath.Base(lsrt), filepath.Base(lvtt))
						continue
					}
					d := translationRecord(ctx, a, r, l, []string{filepath.Base(lsrt), filepath.Base(lvtt)}, tf.Lines)
					sc.Captions.Translations = append(sc.Captions.Translations, d)
					files = append(files, d.Files...)
					outdated += d.Outdated
					if d.Outdated > 0 && !slices.Contains(outdatedIn, d.Name) {
						outdatedIn = append(outdatedIn, d.Name)
					}
				}
				nb, _ := json.MarshalIndent(sc, "", "  ")
				if err := writeFileAtomic(side, append(nb, '\n'), true); err != nil {
					return nil, err
				}
				anyBurned = anyBurned || burnedIn(r)
				total += fixes
				out.Files = append(out.Files, DeliverCaptionsFile{Video: sc.File, Files: files, RevisionID: r.ID, Number: r.Number, Fixes: fixes, BurnedIn: burnedIn(r)})
			}
			switch len(out.Files) {
			case 0:
				out.Summary = "No delivered video there has a captions file to update."
			case 1:
				out.Summary = fmt.Sprintf("Updated the captions files of %s", q(out.Files[0].Video))
			default:
				out.Summary = fmt.Sprintf("Updated the captions files of %s", count(len(out.Files), "video", "videos"))
			}
			if len(out.Files) > 0 {
				if total > 0 {
					out.Summary += fmt.Sprintf(" with %s.", count(total, "text fix", "text fixes"))
				} else {
					out.Summary += "; they already had every fix."
				}
			}
			if anyBurned && total > 0 {
				out.Note = "The captions drawn into the video still have the old words; make a new version (and deliver it) for those."
			}
			if len(out.LeftAlone) > 0 {
				line := fmt.Sprintf("%s %s there already and %s made by this delivery, so MetaRush left %s as %s.", andList(quoteAll(out.LeftAlone)),
					map[bool]string{true: "was", false: "were"}[len(out.LeftAlone) == 1], map[bool]string{true: "wasn't", false: "weren't"}[len(out.LeftAlone) == 1],
					map[bool]string{true: "it", false: "them"}[len(out.LeftAlone) == 1], map[bool]string{true: "it is", false: "they are"}[len(out.LeftAlone) == 1])
				out.Note = strings.TrimSpace(out.Note + " " + line)
			}
			if outdated > 0 {
				line := fmt.Sprintf("%s of the %s captions changed since %s translated and kept %s old translation: ask Claude to translate %s again.", count1(outdated, "line", "lines"),
					andList(outdatedIn), map[bool]string{true: "it was", false: "they were"}[outdated == 1], map[bool]string{true: "its", false: "their"}[outdated == 1], map[bool]string{true: "it", false: "them"}[outdated == 1])
				out.Note = strings.TrimSpace(out.Note + " " + line)
			}
			a.Audit(ctx, "deliver.captions", "delivery", in.Path, "", map[string]any{"files": len(out.Files), "fixes": total})
			return out, nil
		})
}

// deliveredSidecars finds the delivery records a path names.
func deliveredSidecars(path string) ([]string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, invalid("Say which delivered video (or folder) to update.")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, apperr.New(apperr.NotFound, "There's nothing at %s.", q(filepath.Base(abs))).WithDetails(map[string]any{"path": abs})
	}
	if !st.IsDir() {
		if strings.HasSuffix(abs, ".json") {
			return []string{abs}, nil
		}
		side := strings.TrimSuffix(abs, filepath.Ext(abs)) + ".json"
		if _, err := os.Stat(side); err != nil {
			return nil, apperr.New(apperr.NotFound, "%s has no delivery record next to it, so MetaRush can't tell which version it is.", q(filepath.Base(abs))).WithDetails(map[string]any{"path": abs})
		}
		return []string{side}, nil
	}
	matches, _ := filepath.Glob(filepath.Join(abs, "*.json"))
	return matches, nil
}

// translatedFileOut is captions.file in another language (D238).
func translatedFileOut(ctx context.Context, a *app.App, p *models.Project, r *models.Revision, language, words, format string, out *CaptionsFileOut) (*CaptionsFileOut, error) {
	from := captionsFrom(ctx, a, r)
	lang, err := translationLanguage(language, from)
	if err != nil {
		return nil, err
	}
	tf, err := translatedCaptions(ctx, a, r, lang, words == "version")
	if err != nil {
		return nil, err
	}
	out.Language, out.LanguageName = lang, transcripts.LanguageName(lang)
	if tf == nil {
		return nil, apperr.New(apperr.NotFound, "The captions of version %d aren't translated into %s yet.", r.Number, out.LanguageName).
			WithHint("Ask Claude to translate them (on the version's page, or in the Claude Code panel).").
			WithCommand(fmt.Sprintf("metarush captions lines %s --to %s --version %d --json", p.ID, lang, r.Number))
	}
	out.FileName = fmt.Sprintf("%s_r%d.%s.%s", orSlug(slug(cutTitle(r, p), 48), "cut"), r.Number, lang, format)
	out.Content = tf.SRT
	if format == "vtt" {
		out.Content = tf.VTT
	}
	t := tf.Lines
	out.Lines, out.MachineTranslated, out.Untranslated, out.Outdated = len(t.Cues), t.Machine, t.Missing, t.Outdated
	out.Summary = fmt.Sprintf("%s captions file for version %d", out.LanguageName, r.Number)
	if t.Machine {
		out.Summary += ", machine-translated by Claude"
	}
	out.Summary += "."
	var notes []string
	if t.Missing > 0 {
		notes = append(notes, fmt.Sprintf("%s in %s: not translated yet.", count1(t.Missing, "line is", "lines are"), transcripts.LanguageName(from)))
	}
	if t.Outdated > 0 {
		notes = append(notes, fmt.Sprintf("%s changed in the transcript since %s translated and %s the old translation.", count1(t.Outdated, "line", "lines"),
			map[bool]string{true: "it was", false: "they were"}[t.Outdated == 1], map[bool]string{true: "keeps", false: "keep"}[t.Outdated == 1]))
	}
	if t.Followed > 0 {
		notes = append(notes, fmt.Sprintf("%s fixed in the transcript since; the fix was carried into the translation.", count1(t.Followed, "line was", "lines were")))
	}
	out.Note = strings.Join(notes, " ")
	return out, nil
}

// writeOwned writes a pair of captions files: over the ones a delivery's
// record lists (owned, by file name), and otherwise only where no file is.
// wrote is false when either was there already and isn't the delivery's:
// both are left as they are.
func writeOwned(owned map[string]bool, a []byte, pa string, b []byte, pb string) (wrote bool, err error) {
	for _, p := range []string{pa, pb} {
		if owned[filepath.Base(p)] {
			continue
		}
		if _, err := os.Lstat(p); err == nil {
			return false, nil
		}
	}
	for _, f := range []struct {
		p string
		b []byte
	}{{pa, a}, {pb, b}} {
		if owned[filepath.Base(f.p)] {
			if err := writeFileAtomic(f.p, f.b, true); err != nil {
				return false, err
			}
			continue
		}
		if err := writeExclusive(f.p, f.b); err != nil {
			return false, err
		}
	}
	return true, nil
}

// quoteAll marks file names for a sentence.
func quoteAll(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = q(x)
	}
	return out
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	Offer("captions.check", Offering{ID: "caption-check", Group: "deliver", Title: "Caption check",
		Does:  "Lists the words in a version's captions worth a look before it goes out.",
		Words: []string{"proofread captions", "subtitle mistakes"}})
	Offer("captions.file", Offering{ID: "captions-files", Group: "deliver", Title: "Captions files",
		Does:  "A version's captions as a file for any player or platform, with every text fix made since.",
		Words: []string{"srt", "vtt", "subtitle file"}})
}
