package ops

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Delivery notes (D237): a version's title, description, chapters and show
// notes. Claude Code writes them on the person's own Claude plan — in a
// terminal through the video-deliver skill, in the dashboard's panel as a card
// the person keeps — and people write or change them by hand; MetaRush makes
// no AI calls of its own. They sit on the version outside its spec, and
// delivery writes them into the delivery record and a Markdown file next to
// the video.

// Limits of the places videos go (YouTube's are the tightest).
const (
	notesTitleMax       = 100
	notesDescriptionMax = 5000
	notesShowNotesMax   = 20000
	notesChapterMin     = 10 // seconds a chapter should last at least (YouTube)
)

// DeliveryNotesIn names the version whose notes to show.
type DeliveryNotesIn struct {
	ProjectID string `json:"projectId" jsonschema:"description=The project (ID or name)."`
	Revision  string `json:"revision,omitempty" jsonschema:"description=A version (rN or its ID); default the latest."`
	Branch    string `json:"branch,omitempty"`
}

// DeliveryNotesDraft is what someone wrote: chapters as times into the video.
type DeliveryNotesDraft struct {
	Title       string           `json:"title,omitempty" jsonschema:"maxLength=200"`
	Titles      []string         `json:"titles,omitempty" jsonschema:"maxItems=10"`
	Description string           `json:"description,omitempty" jsonschema:"maxLength=5000"`
	Chapters    []models.Chapter `json:"chapters,omitempty" jsonschema:"maxItems=100"`
	ShowNotes   string           `json:"showNotes,omitempty" jsonschema:"maxLength=20000"`
}

// DeliveryNotesSetIn saves (or clears) a version's delivery notes.
type DeliveryNotesSetIn struct {
	ProjectID string             `json:"projectId" jsonschema:"description=The project (ID or name)."`
	Revision  string             `json:"revision,omitempty" jsonschema:"description=A version (rN or its ID); default the latest."`
	Branch    string             `json:"branch,omitempty"`
	Notes     DeliveryNotesDraft `json:"notes"`
	// WrittenBy: claude (Claude Code wrote them), person or agent — only a
	// person says it (at the dashboard or typing in a terminal: what they
	// paste may be Claude's). Everywhere else the channel says, whatever is
	// given: Claude in the panel or Claude Code in a terminal, an agent
	// through MCP or a script.
	WrittenBy string `json:"writtenBy,omitempty" jsonschema:"enum=,enum=claude,enum=person,enum=agent,description=Who wrote them: claude\\, person or agent. Only a person says it; otherwise the channel says (Claude in the panel or a terminal\\, an agent through MCP or a script)."`
	// Clear removes the version's notes.
	Clear bool `json:"clear,omitempty"`
}

// DeliveryNotesOut is a version's delivery notes and how delivery writes them.
type DeliveryNotesOut struct {
	ProjectID   string                `json:"projectId"`
	ProjectName string                `json:"projectName"`
	RevisionID  string                `json:"revisionId"`
	Number      int                   `json:"number"`
	Duration    float64               `json:"duration" jsonschema:"description=How long the version is (seconds)."`
	Notes       *models.DeliveryNotes `json:"notes,omitempty"`
	// Markdown is the file delivery writes next to the video (.notes.md):
	// the title, other titles, the description with its chapters, the show notes.
	Markdown string `json:"markdown,omitempty"`
	// Chapters are the chapter lines as a description lists them ("0:00 Intro").
	Chapters []string `json:"chapters,omitempty"`
	// Warnings are what the places videos go may not take, in plain words.
	Warnings []string `json:"warnings"`
	Summary  string   `json:"summary"`
}

func init() {
	Def("delivery.notes", "A version's delivery notes: its title (and other titles), description, chapters and show notes, as delivery writes them", Flags{},
		func(ctx context.Context, a *app.App, in *DeliveryNotesIn) (*DeliveryNotesOut, error) {
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
			out := notesOut(p, r, r.DeliveryNotes)
			if r.DeliveryNotes.Empty() {
				out.Summary = fmt.Sprintf("Version %d has no delivery notes yet.", r.Number)
			} else {
				out.Summary = fmt.Sprintf("Delivery notes of version %d: %s.", r.Number, notesParts(r.DeliveryNotes))
			}
			return out, nil
		})

	Def("delivery.notes.set", "Save a version's delivery notes — title, other titles, description, chapters, show notes — written by Claude Code on your own plan or by a person; delivery writes them next to the video. The version itself never changes", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *DeliveryNotesSetIn) (*DeliveryNotesOut, error) {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			r, _, err := resolveRevision(ctx, a, p, in.Revision, in.Branch)
			if err != nil {
				return nil, err
			}
			if r == nil {
				return nil, invalid("%s has no cut yet, so there's no version to write delivery notes for.", q(p.Name))
			}
			if in.Clear {
				if err := a.DB.Update(ctx, store.CRevisions, a.WorkspaceID(), r.ID, bson.M{"$unset": bson.M{"deliveryNotes": ""}}); err != nil {
					return nil, err
				}
				a.Audit(ctx, "delivery.notes.set", "revision", r.ID, p.ID, map[string]any{"cleared": true})
				out := notesOut(p, r, nil)
				out.Summary = fmt.Sprintf("Version %d has no delivery notes now.", r.Number)
				return out, nil
			}
			if w := in.WrittenBy; w != "" && w != models.WrittenByClaude && w != models.WrittenByPerson && w != models.WrittenByAgent {
				return nil, invalid("Say who wrote them: claude, person or agent.")
			}
			// Only a person says who wrote them; an agent saying "a person"
			// would keep Auto from ever writing over them.
			by := writtenByChannel(ctx, a)
			if by == models.WrittenByPerson && in.WrittenBy != "" {
				by = in.WrittenBy
			}
			notes, err := cleanNotes(in.Notes, r.Duration)
			if err != nil {
				return nil, err
			}
			if notes.Empty() {
				return nil, invalid("There's nothing in those notes: give a title, a description, chapters or show notes.")
			}
			notes.WrittenBy, notes.By, notes.At = by, a.Actor, time.Now().UTC()
			if err := a.DB.Update(ctx, store.CRevisions, a.WorkspaceID(), r.ID, bson.M{"$set": bson.M{"deliveryNotes": notes}}); err != nil {
				return nil, err
			}
			a.Audit(ctx, "delivery.notes.set", "revision", r.ID, p.ID, map[string]any{"writtenBy": by, "chapters": len(notes.Chapters), "title": notes.Title})
			out := notesOut(p, r, notes)
			out.Summary = fmt.Sprintf("Saved the delivery notes of version %d: %s.", r.Number, notesParts(notes))
			if by == models.WrittenByClaude {
				out.Summary += " Claude wrote them; read them before they go out."
			}
			return out, nil
		})
}

func notesOut(p *models.Project, r *models.Revision, n *models.DeliveryNotes) *DeliveryNotesOut {
	out := &DeliveryNotesOut{ProjectID: p.ID, ProjectName: p.Name, RevisionID: r.ID, Number: r.Number, Duration: r.Duration, Warnings: []string{}}
	if n.Empty() {
		return out
	}
	out.Notes = n
	out.Chapters = chapterLines(n.Chapters)
	out.Markdown = NotesMarkdown(cutTitle(r, p), r.Number, n)
	out.Warnings = notesWarnings(n, r.Duration)
	return out
}

// notesParts says what notes hold: "a title, 5 chapters, a description and show notes".
func notesParts(n *models.DeliveryNotes) string {
	var parts []string
	if n.Title != "" {
		parts = append(parts, "a title")
	}
	if k := len(n.Titles); k > 0 {
		parts = append(parts, count(k, "other title", "other titles"))
	}
	if n.Description != "" {
		parts = append(parts, "a description")
	}
	if k := len(n.Chapters); k > 0 {
		parts = append(parts, count(k, "chapter", "chapters"))
	}
	if n.ShowNotes != "" {
		parts = append(parts, "show notes")
	}
	if len(parts) == 0 {
		return "nothing yet"
	}
	return andList(parts)
}

// cleanNotes checks what someone wrote and puts the chapters in order. A
// version's length (0: unknown) bounds the chapters.
func cleanNotes(d DeliveryNotesDraft, duration float64) (*models.DeliveryNotes, error) {
	n := &models.DeliveryNotes{Title: oneLine(d.Title), Description: strings.TrimSpace(d.Description), ShowNotes: strings.TrimSpace(d.ShowNotes)}
	if k := utf8.RuneCountInString(n.Title); k > notesTitleMax {
		return nil, invalid("That title is %d characters long; places like YouTube take up to %d.", k, notesTitleMax)
	}
	seen := map[string]bool{strings.ToLower(n.Title): true}
	for _, t := range d.Titles {
		t = oneLine(t)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		if k := utf8.RuneCountInString(t); k > notesTitleMax {
			return nil, invalid("The title %s is %d characters long; places like YouTube take up to %d.", q(clipText(t, 60)), k, notesTitleMax)
		}
		seen[strings.ToLower(t)] = true
		n.Titles = append(n.Titles, t)
	}
	if len(n.Titles) > 10 {
		return nil, invalid("Give at most 10 other titles.")
	}
	if k := utf8.RuneCountInString(n.Description); k > notesDescriptionMax {
		return nil, invalid("That description is %d characters long; places like YouTube take up to %d.", k, notesDescriptionMax)
	}
	if k := utf8.RuneCountInString(n.ShowNotes); k > notesShowNotesMax {
		return nil, invalid("Those show notes are %d characters long; keep them under %d.", k, notesShowNotesMax)
	}
	if len(d.Chapters) > 100 {
		return nil, invalid("Give at most 100 chapters.")
	}
	chs := make([]models.Chapter, 0, len(d.Chapters))
	for _, c := range d.Chapters {
		c.Title = oneLine(c.Title)
		if c.Title == "" {
			return nil, invalid("Every chapter needs a name (the one at %s has none).", ytClock(c.At))
		}
		if k := utf8.RuneCountInString(c.Title); k > notesTitleMax {
			return nil, invalid("The chapter %s is %d characters long; keep chapter names under %d.", q(clipText(c.Title, 60)), k, notesTitleMax)
		}
		if c.At < 0 || math.IsNaN(c.At) {
			return nil, invalid("The chapter %s starts before the video does.", q(c.Title))
		}
		if duration > 0 && c.At >= duration {
			return nil, invalid("The chapter %s starts at %s, after the video ends (it is %s long).", q(c.Title), ytClock(c.At), ytClock(duration))
		}
		c.At = math.Round(c.At*1000) / 1000
		chs = append(chs, c)
	}
	sort.SliceStable(chs, func(i, j int) bool { return chs[i].At < chs[j].At })
	for i := 1; i < len(chs); i++ {
		if ytClock(chs[i].At) == ytClock(chs[i-1].At) {
			return nil, invalid("Two chapters start at %s: %s and %s.", ytClock(chs[i].At), q(chs[i-1].Title), q(chs[i].Title))
		}
	}
	if len(chs) > 0 && chs[0].At >= 1 {
		return nil, invalid("The first chapter starts at 0:00 (now %s starts at %s); add one for the opening.", q(chs[0].Title), ytClock(chs[0].At))
	}
	if len(chs) > 0 {
		chs[0].At = 0
	}
	if len(chs) > 0 {
		n.Chapters = chs
	}
	return n, nil
}

// notesWarnings says what the places videos go may not take.
func notesWarnings(n *models.DeliveryNotes, duration float64) []string {
	out := []string{}
	if k := len(n.Chapters); k > 0 && k < 3 {
		out = append(out, "YouTube shows chapters only when there are at least three.")
	}
	for i, c := range n.Chapters {
		end := duration
		if i+1 < len(n.Chapters) {
			end = n.Chapters[i+1].At
		}
		if end > 0 && end-c.At < notesChapterMin {
			out = append(out, fmt.Sprintf("The chapter %s is shorter than %d seconds, and YouTube shows chapters only when each lasts at least that long.", q(c.Title), notesChapterMin))
			break
		}
	}
	if len(n.Chapters) > 0 {
		body := n.Description
		if body != "" {
			body += "\n\n"
		}
		body += strings.Join(chapterLines(n.Chapters), "\n")
		if k := utf8.RuneCountInString(body); k > notesDescriptionMax {
			out = append(out, fmt.Sprintf("With its chapters the description is %d characters long; YouTube takes up to %d.", k, notesDescriptionMax))
		}
	}
	for _, s := range append([]string{n.Title, n.Description}, n.Titles...) {
		if strings.ContainsAny(s, "<>") {
			out = append(out, "YouTube doesn't take < or > in titles and descriptions.")
			break
		}
	}
	return out
}

// chapterLines are chapters as a description lists them ("0:00 Intro").
func chapterLines(chs []models.Chapter) []string {
	out := make([]string, 0, len(chs))
	for _, c := range chs {
		out = append(out, ytClock(c.At)+" "+c.Title)
	}
	return out
}

// ytClock says a time as video descriptions do: 0:00, 1:05, 1:02:03.
func ytClock(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	s := int(math.Floor(sec + 1e-6))
	h, m := s/3600, (s%3600)/60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s%60)
	}
	return fmt.Sprintf("%d:%02d", m, s%60)
}

func clipText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + "…"
}

// NotesMarkdown is the file delivery writes next to a video: the title, other
// titles, the description followed by its chapters, and the show notes.
func NotesMarkdown(cut string, number int, n *models.DeliveryNotes) string {
	var b strings.Builder
	title := n.Title
	if title == "" {
		title = cut
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	who := "a person"
	switch n.WrittenBy {
	case models.WrittenByClaude:
		who = "Claude Code"
	case models.WrittenByAgent:
		who = "an agent"
	}
	fmt.Fprintf(&b, "Delivery notes of version %d of %s, written by %s.\n\n", number, cut, who)
	if len(n.Titles) > 0 {
		b.WriteString("## Other titles\n\n")
		for _, t := range n.Titles {
			fmt.Fprintf(&b, "- %s\n", t)
		}
		b.WriteString("\n")
	}
	if n.Description != "" || len(n.Chapters) > 0 {
		b.WriteString("## Description\n\n")
		if n.Description != "" {
			b.WriteString(n.Description + "\n\n")
		}
		if len(n.Chapters) > 0 {
			b.WriteString(strings.Join(chapterLines(n.Chapters), "\n") + "\n\n")
		}
	}
	if n.ShowNotes != "" {
		b.WriteString("## Show notes\n\n" + n.ShowNotes + "\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// ParseDeliveryNotes reads delivery notes someone (or Claude) wrote in a
// file: JSON ({"title", "titles", "description", "chapters": [{"at": "1:05" or
// 65, "title"}], "showNotes"}) or Markdown with the sections "# <title>",
// "## Other titles" (a list), "## Description", "## Chapters" (lines like
// "1:05 The garden") and "## Show notes".
func ParseDeliveryNotes(b []byte) (*DeliveryNotesDraft, error) {
	b = bytes.TrimSpace(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")))
	if len(b) == 0 {
		return nil, invalid("That file is empty.")
	}
	if b[0] == '{' {
		var raw struct {
			Title       string            `json:"title"`
			Titles      []string          `json:"titles"`
			Description string            `json:"description"`
			ShowNotes   string            `json:"showNotes"`
			Chapters    []json.RawMessage `json:"chapters"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, apperr.Wrap(apperr.InvalidInput, err, "MetaRush can't read those notes: the file is cut short or mistyped.")
		}
		d := &DeliveryNotesDraft{Title: raw.Title, Titles: raw.Titles, Description: raw.Description, ShowNotes: raw.ShowNotes}
		for _, c := range raw.Chapters {
			var ch struct {
				At    json.RawMessage `json:"at"`
				Title string          `json:"title"`
			}
			if json.Unmarshal(c, &ch) != nil {
				var line string
				if json.Unmarshal(c, &line) == nil {
					at, title, ok := chapterLine(line)
					if !ok {
						return nil, invalid("MetaRush can't read the chapter %s: write it as a time and a name, like “1:05 The garden”.", q(clipText(line, 60)))
					}
					d.Chapters = append(d.Chapters, models.Chapter{At: at, Title: title})
					continue
				}
				return nil, invalid("MetaRush can't read one of the chapters: give each a time and a name.")
			}
			at, ok := chapterAt(ch.At)
			if !ok {
				return nil, invalid("The chapter %s has no time MetaRush can read: write it like 1:05 or 65.", q(clipText(ch.Title, 60)))
			}
			d.Chapters = append(d.Chapters, models.Chapter{At: at, Title: ch.Title})
		}
		return d, nil
	}
	return parseNotesMarkdown(string(b))
}

func chapterAt(raw json.RawMessage) (float64, bool) {
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return f, f >= 0
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return editspec.ParseClock(s)
	}
	return 0, false
}

var chapterLineRe = regexp.MustCompile(`^\s*(?:[-*]\s+)?\(?(\d{1,2}(?::\d{1,2}){1,2}(?:\.\d+)?)\)?\s*(?:[-–—:|]\s*)?(.+?)\s*$`)

// chapterLine reads "1:05 The garden" (also "- 1:05 – The garden").
func chapterLine(s string) (float64, string, bool) {
	m := chapterLineRe.FindStringSubmatch(s)
	if m == nil {
		return 0, "", false
	}
	at, ok := editspec.ParseClock(m[1])
	return at, m[2], ok
}

func parseNotesMarkdown(s string) (*DeliveryNotesDraft, error) {
	d := &DeliveryNotesDraft{}
	section := ""
	body := map[string][]string{}
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "# ") && d.Title == "" && section == "":
			d.Title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			section = "intro"
			continue
		case strings.HasPrefix(line, "## "):
			section = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "## ")))
			continue
		}
		body[section] = append(body[section], line)
	}
	text := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(strings.Join(body[k], "\n")); v != "" {
				return v
			}
		}
		return ""
	}
	for _, l := range body["other titles"] {
		if t := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "-*")); t != "" {
			d.Titles = append(d.Titles, t)
		}
	}
	for _, l := range body["titles"] {
		if t := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "-*")); t != "" {
			d.Titles = append(d.Titles, t)
		}
	}
	// A description may end with its chapters; a Chapters section lists them on its own.
	var desc []string
	for _, l := range body["description"] {
		if at, title, ok := chapterLine(l); ok && strings.TrimSpace(l) != "" {
			d.Chapters = append(d.Chapters, models.Chapter{At: at, Title: title})
			continue
		}
		desc = append(desc, l)
	}
	d.Description = strings.TrimSpace(strings.Join(desc, "\n"))
	for _, l := range body["chapters"] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		at, title, ok := chapterLine(l)
		if !ok {
			return nil, invalid("MetaRush can't read the chapter %s: write it as a time and a name, like “1:05 The garden”.", q(clipText(strings.TrimSpace(l), 60)))
		}
		d.Chapters = append(d.Chapters, models.Chapter{At: at, Title: title})
	}
	d.ShowNotes = text("show notes", "shownotes", "notes")
	if d.Title == "" {
		if t := text("title"); t != "" {
			d.Title = t
		}
	}
	return d, nil
}

// NotesFlags puts what the command line said over notes read from a file:
// a title and a description (nil: not given), other titles and chapter lines
// ("1:05 The garden"; given, they replace the file's).
func NotesFlags(d *DeliveryNotesDraft, title, description *string, titles, chapters []string) error {
	if title != nil {
		d.Title = *title
	}
	if description != nil {
		d.Description = *description
	}
	if len(titles) > 0 {
		d.Titles = titles
	}
	if len(chapters) > 0 {
		d.Chapters = nil
		for _, line := range chapters {
			at, name, ok := chapterLine(line)
			if !ok {
				return invalid("MetaRush can't read the chapter %s: write it as a time and a name, like “1:05 The garden”.", q(clipText(strings.TrimSpace(line), 60)))
			}
			d.Chapters = append(d.Chapters, models.Chapter{At: at, Title: name})
		}
	}
	return nil
}

// CheckDeliveryNotes checks notes for a version of the given length (seconds;
// 0: unknown) the way saving them does: the cleaned notes, what the places
// videos go may not take, or why they can't be saved.
func CheckDeliveryNotes(d DeliveryNotesDraft, duration float64) (*models.DeliveryNotes, []string, error) {
	n, err := cleanNotes(d, duration)
	if err != nil {
		return nil, nil, err
	}
	if n.Empty() {
		return nil, nil, invalid("There's nothing in those notes: give a title, a description, chapters or show notes.")
	}
	return n, notesWarnings(n, duration), nil
}

// NotesParts says what notes hold, in plain words.
func NotesParts(n *models.DeliveryNotes) string { return notesParts(n) }

// ChapterLines are chapters as a description lists them ("0:00 Intro").
func ChapterLines(chs []models.Chapter) []string { return chapterLines(chs) }

// NotesAutoSafe reports whether saving notes can run by itself under Auto
// (D186: only what can be undone): taking notes off, or writing over notes a
// person wrote, asks.
func NotesAutoSafe(ctx context.Context, a *app.App, in DeliveryNotesSetIn) (bool, string) {
	if in.Clear {
		return false, "it takes the version's delivery notes off"
	}
	p, err := resolveProject(ctx, a, in.ProjectID)
	if err != nil {
		return false, "MetaRush can't find the project"
	}
	r, _, err := resolveRevision(ctx, a, p, in.Revision, in.Branch)
	if err != nil || r == nil {
		return false, "MetaRush can't find the version"
	}
	if n := r.DeliveryNotes; !n.Empty() && n.WrittenBy != models.WrittenByClaude {
		return false, "it would replace delivery notes a person wrote"
	}
	return true, ""
}

// writtenByChannel says who wrote notes by the channel they came through:
// Claude for the panel's own channels and for a command Claude Code ran in
// a terminal (CLAUDECODE=1), an agent through MCP, a person when a person
// runs it (the dashboard, typing in a terminal), otherwise an agent (a
// script).
func writtenByChannel(ctx context.Context, a *app.App) string {
	switch {
	case a.Actor.Via == models.ViaAssistant || a.Actor.Via == models.ViaAssistantAuto:
		return models.WrittenByClaude
	case a.Actor.Via == "mcp":
		return models.WrittenByAgent
	case callerOf(ctx).Human:
		return models.WrittenByPerson
	case a.Actor.Via == "cli" && os.Getenv("CLAUDECODE") == "1":
		return models.WrittenByClaude
	}
	return models.WrittenByAgent
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	Offer("delivery.notes.set", Offering{ID: "delivery-notes", Group: "deliver", Title: "Titles, chapters and show notes",
		Does:  "Keeps a version's title, description, chapters and show notes for delivery.",
		Words: []string{"youtube description", "chapter markers", "episode notes"}})
}
