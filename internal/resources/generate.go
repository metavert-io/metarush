package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/oldnames"
	rnd "github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// Generated resources (D106): pictures MetaRush makes from a repository —
// code slides (the code highlighted with Shiki), architecture diagrams
// (Mermaid or D2 that Claude wrote, drawn as SVG) and repository cards
// (stats). Code slides, Mermaid diagrams and cards are drawn in the same
// network-jailed headless browser as renders (video/render/generate.mjs);
// D2 runs offline. Each records exactly where it came from
// (repo@commit:path:lines) and how it was made, so a newer commit can make
// it again as a new version (cuts move to it only with cut update).

// Make kinds.
const (
	MakeCode    = "code"
	MakeDiagram = "diagram"
	MakeStats   = "stats"
	// MakeAI is a picture an AI service made (internal/aiwork, D226); it is
	// never refreshed from a repository.
	MakeAI = "ai"
)

// MaxCodeLines is the most lines a code slide shows (it stays readable on a phone).
const MaxCodeLines = 80

// maxCodeLineChars is where a long line is cut (with …): longer lines would
// make the whole slide's type too small to read.
const maxCodeLineChars = 110

// MakeOptions describe a generated resource.
type MakeOptions struct {
	Kind string
	// Repo is the repository it is made from (code and stats; optional for
	// diagrams); RepoSHA a version of it (default the current one).
	Repo    *models.Resource
	RepoSHA string
	// Code slides: the file, its lines (1-based, inclusive; 0,0 = the whole
	// file when it is short), lines to highlight ("12-14,20"), the language
	// (default: from the file name).
	File      string
	From, To  int
	Highlight string
	Language  string
	// Diagrams: mermaid or d2 (default: told by the source) and the source.
	Format string
	Source string
	// Theme: dark or light (default: from the brand kit's background).
	Theme string
	// Name and Title of the new resource (default from the file or repository).
	Name, Title string
	// Where it goes: a project, or the workspace library.
	ProjectID string
	Workspace bool
	// ResourceID makes it a new version of that resource (a refresh).
	ResourceID string
}

// MakeResult is what was made.
type MakeResult struct {
	Add        *AddResult
	Provenance string
	Notes      []string
}

var highlightRe = regexp.MustCompile(`^\d+(-\d+)?(,\d+(-\d+)?)*$`)

// ParseLines reads "80-120" or "80" (one line).
func ParseLines(s string) (from, to int, err error) {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "–", "-"), " ", ""))
	if s == "" {
		return 0, 0, nil
	}
	a, b, found := strings.Cut(s, "-")
	from, err1 := strconv.Atoi(strings.TrimPrefix(a, "L"))
	to = from
	var err2 error
	if found {
		to, err2 = strconv.Atoi(strings.TrimPrefix(b, "L"))
	}
	if err1 != nil || err2 != nil || from < 1 || to < from {
		return 0, 0, apperr.New(apperr.InvalidInput, "lines are a range like 40-72 (first and last line, counting from 1)")
	}
	return from, to, nil
}

// parseHighlight turns "12-14,20" into ranges inside [from, to].
func parseHighlight(s string, from, to int) ([][2]int, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	if s == "" {
		return nil, nil
	}
	if !highlightRe.MatchString(s) {
		return nil, apperr.New(apperr.InvalidInput, "highlight lines like 12-14,20 (lines of the file)")
	}
	var out [][2]int
	for _, part := range strings.Split(s, ",") {
		a, b, err := ParseLines(part)
		if err != nil {
			return nil, err
		}
		if a < from || b > to {
			return nil, apperr.New(apperr.InvalidInput, "highlighted lines %s are outside the lines shown (%d-%d)", part, from, to)
		}
		out = append(out, [2]int{a, b})
	}
	return out, nil
}

// brandColors are the brand's colours (or MetaRush's defaults): the brand of
// the Content Settings the project uses (D193; "" the default's).
func brandColors(ctx context.Context, a *app.App, projectID string) editspec.Brand {
	b := editspec.DefaultBrand
	var k *models.BrandKit
	if rec, _, err := content.For(ctx, a, projectID); err == nil && rec != nil {
		k = rec.BrandKit
	}
	if k != nil {
		for _, kv := range []struct{ dst, src *string }{{&b.Primary, &k.Primary}, {&b.Accent, &k.Accent}, {&b.Text, &k.Text}, {&b.Background, &k.Background}} {
			if *kv.src != "" {
				*kv.dst = *kv.src
			}
		}
		if k.Font != "" {
			b.Font = k.Font
		}
	}
	return b
}

// themeFor picks dark or light from a background colour.
func themeFor(bg string) string {
	r, g, b, ok := hexRGB(bg)
	if !ok {
		return "dark"
	}
	lum := func(c float64) float64 {
		c /= 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	if 0.2126*lum(r)+0.7152*lum(g)+0.0722*lum(b) > 0.45 {
		return "light"
	}
	return "dark"
}

func hexRGB(s string) (float64, float64, float64, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return float64(v >> 16 & 0xff), float64(v >> 8 & 0xff), float64(v & 0xff), true
}

// mermaidStart tells Mermaid from D2 by the first word.
var mermaidStart = regexp.MustCompile(`(?m)^\s*(%%.*\n\s*)*(graph|flowchart|sequenceDiagram|classDiagram(-v2)?|stateDiagram(-v2)?|erDiagram|gantt|journey|pie|mindmap|timeline|C4Context|C4Container|C4Component|C4Dynamic|C4Deployment|architecture-beta|block-beta|quadrantChart|gitGraph|sankey-beta|sankey|xychart-beta|xychart|requirementDiagram|packet-beta|packet|kanban|radar-beta|treemap-beta)\b`)

// DiagramFormat tells which language a diagram is written in.
func DiagramFormat(src string) string {
	if mermaidStart.MatchString(src) {
		return "mermaid"
	}
	return "d2"
}

// genResult is what the generator script says when it is done.
type genResult struct {
	Width, Height int
	Versions      map[string]string
}

// runGenerator runs video/render/generate.mjs in the jailed browser.
func runGenerator(ctx context.Context, a *app.App, kind string, input any, out string) (*genResult, error) {
	if !a.Tools.Has(tools.Node) {
		return nil, apperr.New(apperr.ToolMissing, "Making slides needs Node.js and the Remotion renderer, which aren't on this computer yet.").
			WithHint("Add the Remotion renderer in Settings → Components (it brings Node.js).").WithCommand("metarush components add renderer")
	}
	videoDir, rs, err := rnd.New(a.Cfg, a.Tools).Ready(false)
	if err != nil {
		return nil, err
	}
	release, err := rnd.Use(rs)
	if err != nil {
		return nil, err
	}
	defer release()
	in := out + ".in.json"
	b, _ := json.Marshal(input)
	if err := os.WriteFile(in, b, 0o600); err != nil {
		return nil, err
	}
	var mu sync.Mutex
	var done *genResult
	var failMsg string
	cctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	_, runErr := a.Tools.Run(cctx, tools.Spec{Tool: tools.Node, Dir: videoDir, Background: true,
		Args: []string{"render/generate.mjs", kind, "--in", in, "--out", out, "--bundle-cache", filepath.Join(a.Cfg.Cache.Dir, "remotion-bundle")},
		OnLine: func(line string) {
			var ev struct {
				Type     string            `json:"type"`
				Message  string            `json:"message"`
				Width    int               `json:"width"`
				Height   int               `json:"height"`
				Versions map[string]string `json:"versions"`
			}
			if json.Unmarshal([]byte(line), &ev) != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			switch ev.Type {
			case "done":
				done = &genResult{Width: ev.Width, Height: ev.Height, Versions: ev.Versions}
			case "error":
				failMsg = ev.Message
			}
		}})
	if done == nil {
		msg := failMsg
		if msg == "" && runErr != nil {
			msg = runErr.Error()
		}
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			msg = "it took too long"
		}
		return nil, apperr.New(apperr.InvalidInput, "MetaRush couldn't draw it: %s", firstLine(msg))
	}
	if _, err := os.Stat(out); err != nil {
		return nil, fmt.Errorf("the generator wrote nothing")
	}
	return done, nil
}

// repoSource is the provenance of something made from a repository version.
func repoSource(repo *models.Resource, v *models.ResourceVersion, tool, step string) models.ResourceSource {
	src := models.ResourceSource{Kind: models.FromGenerated, Tool: tool}
	if repo != nil && v != nil && v.Repo != nil {
		src.URL, src.Site, src.Commit, src.License, src.Author = v.Source.URL, v.Source.Site, v.Repo.Commit, v.Repo.License, v.Repo.Owner
		src.Lineage = []models.ResourceLineage{{ResourceID: repo.ID, SHA256: v.SHA256, Step: step}}
	}
	return src
}

// Make makes a generated resource and adds it (or a new version of it).
func Make(ctx context.Context, a *app.App, o MakeOptions) (*MakeResult, error) {
	var repoV *models.ResourceVersion
	if o.Repo != nil {
		if o.Repo.Kind != models.ResourceRepo {
			return nil, apperr.New(apperr.InvalidInput, "%s is a %s, not a repository.", o.Repo.Title, KindWord(o.Repo.Kind))
		}
		repoV = o.Repo.Latest()
		if o.RepoSHA != "" {
			repoV = nil
			for i := range o.Repo.Versions {
				x := &o.Repo.Versions[i]
				if strings.HasPrefix(x.SHA256, o.RepoSHA) || (x.Repo != nil && strings.HasPrefix(x.Repo.Commit, o.RepoSHA)) {
					repoV = x
				}
			}
		}
		if repoV == nil || repoV.Repo == nil {
			return nil, missingVersion(o.Repo, o.RepoSHA)
		}
	}
	brand := brandColors(ctx, a, o.ProjectID)
	theme := o.Theme
	if theme == "" {
		theme = themeFor(brand.Background)
	}
	if theme != "dark" && theme != "light" {
		return nil, apperr.New(apperr.InvalidInput, "Choose a dark or a light theme.")
	}
	work, err := makeTemp(a)
	if err != nil {
		return nil, err
	}
	defer removeAll(work)
	res := &MakeResult{}
	add := AddOptions{Name: o.Name, Title: o.Title, ProjectID: o.ProjectID, ResourceID: o.ResourceID, Via: a.Actor.Via}
	if o.Workspace {
		add.ProjectID, add.Scope = "", models.ScopeWorkspace
	}
	if add.ProjectID == "" && add.Scope == "" && o.ResourceID == "" {
		add.Scope = models.ScopeWorkspace
		if o.Repo != nil && o.Repo.Scope == models.ScopeProject {
			add.ProjectID, add.Scope = o.Repo.ProjectID, models.ScopeProject // next to the repository it came from
		}
	}
	var file string
	switch o.Kind {
	case MakeCode:
		if o.Repo == nil {
			return nil, apperr.New(apperr.InvalidInput, "A code slide is made from a repository: say which one.").WithCommand("metarush resources list --kind repo")
		}
		raw, _, err := RepoFile(ctx, a, o.Repo, repoV.SHA256, o.File)
		if err != nil {
			return nil, err
		}
		if isBinaryData(raw) {
			return nil, apperr.New(apperr.InvalidInput, "%s isn't text, so it can't be shown as code.", o.File)
		}
		lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		from, to := o.From, o.To
		if from == 0 && to == 0 {
			if len(lines) > MaxCodeLines {
				return nil, apperr.New(apperr.InvalidInput, "%s has %d lines: say which ones to show (at most %d), like lines 40-72.", o.File, len(lines), MaxCodeLines)
			}
			from, to = 1, max(1, len(lines))
		}
		if to > len(lines) {
			return nil, apperr.New(apperr.InvalidInput, "%s has %d lines, so there's no line %d.", o.File, len(lines), to)
		}
		if to-from+1 > MaxCodeLines {
			return nil, apperr.New(apperr.InvalidInput, "A code slide shows at most %d lines (asked for %d): pick the part that matters.", MaxCodeLines, to-from+1)
		}
		hl, err := parseHighlight(o.Highlight, from, to)
		if err != nil {
			return nil, err
		}
		shown := lines[from-1 : to]
		code, cut := codeForSlide(shown)
		if cut > 0 {
			res.Notes = append(res.Notes, fmt.Sprintf("%d long lines were shortened to %d characters (…).", cut, maxCodeLineChars))
		}
		lang := strings.TrimSpace(o.Language)
		if lang == "" {
			_, lang = LanguageOf(o.File)
		}
		if lang == "" {
			lang = "text"
		}
		clean := strings.TrimPrefix(path.Clean("/"+o.File), "/")
		span := fmt.Sprintf("lines %d–%d", from, to)
		if from == to {
			span = fmt.Sprintf("line %d", from)
		}
		input := map[string]any{"code": code, "language": lang, "theme": theme, "firstLine": from, "highlight": hl, "brand": brand,
			"header": map[string]any{"path": clean, "lines": span, "repo": repoV.Repo.Owner + "/" + repoV.Repo.Name + " @ " + models.ShortSHA(repoV.Repo.Commit)}}
		file = filepath.Join(work, "code-"+Slug(path.Base(clean))+".png")
		gr, err := runGenerator(ctx, a, MakeCode, input, file)
		if err != nil {
			return nil, err
		}
		if add.Title == "" && o.ResourceID == "" {
			add.Title = path.Base(clean) + ", " + span
		}
		lineSpan := fmt.Sprintf("%d-%d", from, to)
		add.Source = repoSource(o.Repo, repoV, "code slide (Shiki "+gr.Versions["shiki"]+")", fmt.Sprintf("%s of %s", span, clean))
		add.Source.File, add.Source.Lines = clean, lineSpan
		add.Source.MadeWith = &models.ResourceMadeWith{Kind: MakeCode, RepoID: o.Repo.ID, RepoSHA: repoV.SHA256, File: clean, From: from, To: to, Language: lang,
			Highlight: strings.ReplaceAll(o.Highlight, " ", ""), Snippet: clipBytes(strings.Join(shown, "\n"), 20000), Theme: theme,
			Caption: clean + " · " + span}
	case MakeDiagram:
		src := strings.TrimSpace(o.Source)
		if src == "" {
			return nil, apperr.New(apperr.InvalidInput, "A diagram needs its description, written in Mermaid or D2.").WithCommand("metarush resources make diagram <repo> --from <file>")
		}
		if len(src) > 50000 {
			return nil, apperr.New(apperr.InvalidInput, "That diagram is too long to draw (at most 50,000 characters).")
		}
		format := strings.ToLower(strings.TrimSpace(o.Format))
		if format == "" {
			format = DiagramFormat(src)
		}
		file = filepath.Join(work, "diagram.svg")
		tool := ""
		switch format {
		case "mermaid":
			gr, err := runGenerator(ctx, a, MakeDiagram, map[string]any{"format": "mermaid", "source": src, "theme": theme, "brand": brand}, file)
			if err != nil {
				return nil, err
			}
			tool = "diagram (Mermaid " + gr.Versions["mermaid"] + ")"
		case "d2":
			if err := runD2(ctx, a, src, theme, file); err != nil {
				return nil, err
			}
			tool = "diagram (D2)"
		default:
			return nil, apperr.New(apperr.InvalidInput, "A diagram is written in Mermaid or D2.")
		}
		if add.Title == "" && o.ResourceID == "" {
			add.Title = "Diagram"
			if o.Repo != nil {
				add.Title = o.Repo.Title + " diagram"
			}
		}
		add.Source = repoSource(o.Repo, repoV, tool, "a diagram of the repository")
		add.Source.MadeWith = &models.ResourceMadeWith{Kind: MakeDiagram, Format: format, Source: clipBytes(src, 50000), Theme: theme}
		if o.Repo != nil {
			add.Source.MadeWith.RepoID, add.Source.MadeWith.RepoSHA = o.Repo.ID, repoV.SHA256
		}
	case MakeStats:
		if o.Repo == nil {
			return nil, apperr.New(apperr.InvalidInput, "A repository card is made from a repository: say which one.")
		}
		rs := repoV.Repo
		input := map[string]any{"theme": theme, "brand": brand, "repo": rs.Owner + "/" + rs.Name, "description": rs.Description, "stars": rs.Stars, "forks": rs.Forks,
			"license": rs.License, "languages": rs.Languages, "files": rs.Files, "contributors": len(rs.Contributors), "commit": models.ShortSHA(rs.Commit),
			"committedAt": rs.CommittedAt.Format("2 Jan 2006"), "topics": rs.Topics, "recent": len(rs.Recent)}
		file = filepath.Join(work, "repo-card.png")
		if _, err := runGenerator(ctx, a, MakeStats, input, file); err != nil {
			return nil, err
		}
		if add.Title == "" && o.ResourceID == "" {
			add.Title = rs.Owner + "/" + rs.Name + " card"
		}
		add.Source = repoSource(o.Repo, repoV, "repository card", "what the repository holds at this commit")
		add.Source.MadeWith = &models.ResourceMadeWith{Kind: MakeStats, RepoID: o.Repo.ID, RepoSHA: repoV.SHA256, Theme: theme}
	default:
		return nil, apperr.New(apperr.InvalidInput, "MetaRush makes code slides (code), diagrams (diagram) and repository cards (stats).")
	}
	add.FileName = filepath.Base(file)
	ar, err := AddFile(ctx, a, file, add)
	if err != nil {
		return nil, err
	}
	res.Add = ar
	res.Provenance = add.Source.Provenance()
	return res, nil
}

// codeForSlide expands tabs and shortens very long lines; it returns the
// code and how many lines were shortened.
func codeForSlide(lines []string) (string, int) {
	cut := 0
	out := make([]string, len(lines))
	for i, l := range lines {
		l = expandTabs(strings.TrimRight(l, " \t"), 4)
		if r := []rune(l); len(r) > maxCodeLineChars {
			l = string(r[:maxCodeLineChars-1]) + "…"
			cut++
		}
		out[i] = l
	}
	return strings.Join(out, "\n"), cut
}

func expandTabs(s string, n int) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			k := n - col%n
			b.WriteString(strings.Repeat(" ", k))
			col += k
			continue
		}
		b.WriteRune(r)
		col++
	}
	return b.String()
}

func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// runD2 draws a D2 diagram as SVG offline: no bundled downloads, and a proxy
// that refuses everything for anything it might try to fetch.
func runD2(ctx context.Context, a *app.App, src, theme, out string) error {
	if !a.Tools.Has(tools.D2) {
		return apperr.New(apperr.ToolMissing, "Drawing D2 diagrams needs D2 (about 30 MB), which isn't on this computer.").
			WithHint("Write the diagram in Mermaid instead (it needs nothing extra), or add D2 in Settings → Components.").WithCommand("metarush components add diagrams")
	}
	in := out + ".d2"
	if err := os.WriteFile(in, []byte(src), 0o600); err != nil {
		return err
	}
	themeID := "0"
	if theme == "dark" {
		themeID = "200"
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	refuse := "http://127.0.0.1:9"
	_, err := a.Tools.Run(cctx, tools.Spec{Tool: tools.D2, Dir: filepath.Dir(in),
		Args: []string{"--bundle=false", "--layout=dagre", "--theme=" + themeID, "--pad=48", "--", in, out},
		Env:  []string{"HTTP_PROXY=" + refuse, "HTTPS_PROXY=" + refuse, "http_proxy=" + refuse, "https_proxy=" + refuse, "NO_PROXY=", "D2_LAYOUT=dagre"}})
	if err != nil {
		var fe *tools.ErrFailed
		if errors.As(err, &fe) {
			return apperr.New(apperr.InvalidInput, "D2 couldn't draw it: %s", firstLine(fe.Stderr))
		}
		return err
	}
	return nil
}

// RegenerateFromRepo makes new versions of the code slides and repository
// cards made from an older commit of a repository, from its current one. A
// code slide follows its code when the file changed around it; when the code
// itself changed or the file is gone it stays as it was, with a note.
// Diagrams are Claude's description of the code: they stay (make one again
// when the architecture changed).
func RegenerateFromRepo(ctx context.Context, a *app.App, repo *models.Resource) ([]string, error) {
	cur := repo.Latest()
	if cur == nil || cur.Repo == nil {
		return nil, nil
	}
	var made []models.Resource
	filter := oldnames.MadeFrom(repo.ID)
	filter["removed"] = bson.M{"$ne": true}
	if err := a.DB.Find(ctx, store.CResources, a.WorkspaceID(), filter, &made); err != nil {
		return nil, err
	}
	var notes []string
	for i := range made {
		r := &made[i]
		v := r.Latest()
		if v == nil || v.Source.MadeWith == nil || v.Source.MadeWith.RepoID != repo.ID || v.Source.MadeWith.RepoSHA == cur.SHA256 {
			continue
		}
		rc := v.Source.MadeWith
		o := MakeOptions{Kind: rc.Kind, Repo: repo, Theme: rc.Theme, ResourceID: r.ID}
		switch rc.Kind {
		case MakeCode:
			raw, _, err := RepoFile(ctx, a, repo, "", rc.File)
			if err != nil {
				notes = append(notes, fmt.Sprintf("%s stays as it was: %s isn't in the newer commit.", r.Title, rc.File))
				continue
			}
			from, to, ok := locateSnippet(string(raw), rc.Snippet, rc.From)
			if !ok {
				notes = append(notes, fmt.Sprintf("%s stays as it was: its code in %s changed. Make it again if you want the new code.", r.Title, rc.File))
				continue
			}
			o.File, o.From, o.To, o.Language = rc.File, from, to, rc.Language
			if rc.Highlight != "" {
				o.Highlight = shiftRanges(rc.Highlight, from-rc.From)
			}
		case MakeStats:
		default:
			continue
		}
		out, err := Make(ctx, a, o)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s couldn't be made again: %s", r.Title, apperr.From(err).Message))
			continue
		}
		if out.Add.Status == StatusVersion {
			notes = append(notes, fmt.Sprintf("Made a new version of %s from %s.", r.Title, models.ShortSHA(cur.Repo.Commit)))
		}
	}
	return notes, nil
}

// locateSnippet finds a code slide's lines in a newer version of its file:
// at the same place, else the first place they appear. ok is false when the
// code is no longer there exactly.
func locateSnippet(file, snippet string, oldFrom int) (from, to int, ok bool) {
	lines := strings.Split(strings.ReplaceAll(file, "\r\n", "\n"), "\n")
	want := strings.Split(strings.ReplaceAll(snippet, "\r\n", "\n"), "\n")
	if len(want) == 0 || snippet == "" {
		return 0, 0, false
	}
	match := func(at int) bool {
		if at < 0 || at+len(want) > len(lines) {
			return false
		}
		for i, w := range want {
			if strings.TrimRight(lines[at+i], " \t") != strings.TrimRight(w, " \t") {
				return false
			}
		}
		return true
	}
	if match(oldFrom - 1) {
		return oldFrom, oldFrom + len(want) - 1, true
	}
	for at := range lines {
		if match(at) {
			return at + 1, at + len(want), true
		}
	}
	return 0, 0, false
}

// shiftRanges moves "12-14,20" by delta lines.
func shiftRanges(s string, delta int) string {
	if delta == 0 {
		return s
	}
	var parts []string
	for _, p := range strings.Split(s, ",") {
		a, b, err := ParseLines(p)
		if err != nil {
			return ""
		}
		if a == b {
			parts = append(parts, strconv.Itoa(a+delta))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", a+delta, b+delta))
		}
	}
	return strings.Join(parts, ",")
}

// KindWord names a resource kind in plain words.
func KindWord(k string) string {
	switch k {
	case models.ResourceSVG:
		return "vector picture"
	case models.ResourcePDF:
		return "PDF"
	case models.ResourceFont:
		return "font"
	case models.ResourceRepo:
		return "repository"
	case models.ResourceWebsite:
		return "web page"
	}
	return "picture"
}
