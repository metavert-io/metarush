package assistant

import (
	"context"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/intake"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
)

// Slides from the Claude Code panel (D104–D106): when Claude asks to bring in
// a GitHub repository or a web page, to make a code slide, a diagram or a
// repository card, or to look at a repository or page again, the person sees
// a card and confirms it; the dashboard runs the same op the CLI does.
// Inputs are built here from the command Claude ran; a diagram's description
// is read from this conversation's proposals folder and nowhere else, and is
// shown on the card before anything is made.

// resourceCommands are the commands describeResource turns into cards.
var resourceCommands = map[string]bool{"resources add": true, "add": true, "resources make": true, "resources refresh": true, "resources pictures": true}

// maxDiagramSource bounds a diagram description read from the proposals folder.
const maxDiagramSource = 50000

func (m *Manager) describeResource(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	switch call.Path {
	case "resources add", "add":
		m.describeResourceAdd(ctx, call, a)
	case "resources make":
		m.describeResourceMake(ctx, c, call, a)
	case "resources refresh":
		m.describeResourceRefresh(ctx, call, a)
	case "resources pictures":
		m.describeResourcePictures(ctx, call, a)
	}
}

// picturePaths reads a repeated flag of a repository's folders (--keep a
// --keep b, or --keep a,b), each cleaned; why says what is wrong.
func picturePaths(call *cliCall, name string) (paths []string, why string) {
	for _, v := range call.Multi[name] {
		for _, p := range strings.Split(v, ",") {
			if strings.TrimSpace(p) == "" {
				continue
			}
			c, err := resources.CleanPicturePath(p)
			if err != nil {
				return nil, "Claude named a folder of the repository MetaRush can't keep pictures from."
			}
			paths = append(paths, c)
		}
	}
	if len(paths) > resources.MaxPicturePaths {
		return nil, "Claude named too many folders at once."
	}
	return paths, ""
}

// folderWords names a repository's folders on a card.
func folderWords(paths []string) string {
	var out []string
	for _, p := range paths {
		if p == resources.WholeRepo {
			out = append(out, "the whole repository")
		} else {
			out = append(out, "“"+p+"”")
		}
	}
	return strings.Join(out, ", ")
}

// describeResourcePictures prepares keeping the pictures in a repository's
// folders, or no longer keeping a folder in step (`resources pictures <repo>
// --keep|--drop`, D216). Listing them needs no card: the repository's
// details say what it keeps.
func (m *Manager) describeResourcePictures(ctx context.Context, call *cliCall, a *AssistantAction) {
	ref := strings.TrimSpace(first(call.Args))
	if !resourceRef(ref) {
		incomplete(a, "Keep a repository's pictures", "Claude did not name the repository by its short name.")
		return
	}
	prj, ok := projectFlag(call)
	if !ok {
		incomplete(a, "Keep a repository's pictures", "Claude did not name the project by its ID.")
		return
	}
	keep, why := picturePaths(call, "keep")
	if why == "" {
		var drop []string
		drop, why = picturePaths(call, "drop")
		if why == "" && len(keep)+len(drop) == 0 {
			unavailable(a, "Look at the pictures “"+ref+"” keeps", "The repository's details on the Resources tab list the pictures it keeps; Claude reads them there too.")
			return
		}
		if why == "" {
			a.Title = "Change which folders of “" + ref + "” it keeps pictures from"
			switch {
			case len(drop) == 0:
				a.Title = "Keep the pictures in " + folderWords(keep) + " of “" + ref + "”"
			case len(keep) == 0:
				a.Title = "Stop keeping " + folderWords(drop) + " of “" + ref + "” in step"
			}
			if len(keep) > 0 {
				a.Details = append(a.Details, "Each PNG, JPEG, WebP, GIF or SVG file there becomes a picture of its own that says which commit it came from",
					"As the repository is now (getting its newest commit brings newer art); nothing from it ever runs")
			}
			if len(drop) > 0 {
				a.Details = append(a.Details, "The pictures kept from "+folderWords(drop)+" stay, and cuts that show them keep them")
			}
			setInput(a, "resources.pictures.set", ops.RepoPicturesSetIn{ResourceRefIn: ops.ResourceRefIn{Ref: ref, ProjectID: prj}, Keep: keep, Drop: drop})
			return
		}
	}
	incomplete(a, "Keep a repository's pictures", why)
}

// projectFlag reads --project: a project ID, or none (ok false: not an ID).
func projectFlag(call *cliCall) (string, bool) {
	prj := strings.TrimSpace(call.Flags["project"])
	return prj, prj == "" || ids.Valid(ids.Project, prj)
}

// resourceRef is a resource's short name (a PDF or page picture as name#2 is
// not a resource of its own) or ID.
func resourceRef(s string) bool {
	return resources.NamePattern.MatchString(s) || ids.Valid(ids.Resource, s)
}

// intoWhere says where a new resource goes.
func (m *Manager) intoWhere(ctx context.Context, projectID string, workspace bool) string {
	if projectID == "" || workspace {
		return "Into the workspace library, for every project"
	}
	return "Into " + m.nameOf(ctx, projectID) + "'s resources"
}

func intFlag(call *cliCall, name string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(call.Flags[name]))
	return n
}

// describeResourceAdd prepares bringing in one GitHub repository or web page
// (`resources add <link>`, or `add <link>` for a repository or page). Files
// are added by the person (dropped on the Resources tab), and a video link is
// imported from the Add box.
func (m *Manager) describeResourceAdd(ctx context.Context, call *cliCall, a *AssistantAction) {
	var links, files []string
	for _, x := range call.Args {
		if strings.HasPrefix(x, "http://") || strings.HasPrefix(x, "https://") || strings.HasPrefix(x, "github.com/") {
			links = append(links, x)
		} else {
			files = append(files, x)
		}
	}
	if len(files) > 0 || len(links) == 0 {
		unavailable(a, "Add files", "Add pictures, logos, PDFs and fonts from the dashboard: drop them on the project's Resources tab.")
		return
	}
	if len(links) > 1 {
		incomplete(a, "Bring in links", "Claude asked for several links at once; one repository or page per request.")
		return
	}
	raw := links[0]
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		incomplete(a, "Bring in a repository or page", "That isn't a web address MetaRush can use.")
		return
	}
	if !resources.AllowLocalPages() && resources.LocalAddress(u.Hostname()) {
		unavailable(a, "Bring in "+u.Host, "That address is on this computer or a local network, so MetaRush won't fetch it.")
		return
	}
	as := strings.ToLower(strings.TrimSpace(call.Flags["as"]))
	if as == "video" || (call.Path == "add" && as == "" && intake.RouteLink(raw).Route == resources.LinkVideo) {
		unavailable(a, "Import a video", "Paste the link in the Add box to import the video as footage.")
		return
	}
	if as != "" && as != resources.FetchRepo && as != resources.FetchPage && as != resources.FetchPicture {
		incomplete(a, "Bring in "+u.Host, "Claude asked for "+strconv.Quote(as)+": a repo, a page or a picture.")
		return
	}
	prj, ok := projectFlag(call)
	if !ok {
		incomplete(a, "Bring in a repository or page", "Claude did not name the project by its ID.")
		return
	}
	name := strings.TrimSpace(call.Flags["name"])
	if name != "" && !resources.NamePattern.MatchString(name) {
		incomplete(a, "Bring in a repository or page", "The short name "+strconv.Quote(name)+" isn't one MetaRush can use (lowercase letters, digits, - and _).")
		return
	}
	in := ops.ResourceFetchIn{URL: raw, ProjectID: prj, Workspace: hasBool(call, "workspace"), As: as, Ref: clip(strings.TrimSpace(call.Flags["ref"]), 200),
		Depth: intFlag(call, "depth"), MaxPages: intFlag(call, "max-pages"), Name: name, Title: clip(strings.TrimSpace(call.Flags["title"]), 200)}
	if in.Depth < 0 || in.Depth > 1 || in.MaxPages < 0 || in.MaxPages > 20 {
		incomplete(a, "Take a snapshot of "+u.Host, "A snapshot takes the page itself or also the pages it links to (depth 1), at most 20.")
		return
	}
	where := m.intoWhere(ctx, prj, in.Workspace)
	pics, why := picturePaths(call, "pictures")
	if why != "" {
		incomplete(a, "Bring in a repository", why)
		return
	}
	if r, isRepo := resources.ParseRepoURL(raw); isRepo && (as == "" || as == resources.FetchRepo) {
		a.Title = "Bring in the repository " + r.Owner + "/" + r.Name
		at := "Kept at its current commit"
		if ref := firstNonEmpty(in.Ref, r.Ref); ref != "" {
			at = "Kept at " + ref
		}
		files := "Only its text files; nothing from it ever runs"
		if len(pics) > 0 {
			// D216: the pictures in some of its folders, each a picture of its own.
			in.Pictures = pics
			files = "Its text files, and the pictures in " + folderWords(pics) + " as pictures of their own; nothing from it ever runs"
		}
		a.Details = []string{at, files, "Uses your own GitHub sign-in on this computer", where}
		setInput(a, "resources.fetch", in)
		return
	}
	if len(pics) > 0 {
		incomplete(a, "Bring in "+u.Host, "Only a repository keeps the pictures in its folders.")
		return
	}
	page := strings.TrimSuffix(u.Host+u.EscapedPath(), "/")
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg", ".pdf", ".heic", ".avif", ".ttf", ".otf", ".woff2":
		if as == "" || as == resources.FetchPicture {
			a.Title = "Download " + page
			a.Details = []string{"Kept with the address it came from", where}
			setInput(a, "resources.fetch", in)
			return
		}
	}
	a.Title = "Take a snapshot of " + page
	a.Details = []string{"Pictures on a computer and a phone, the whole page, and its words"}
	if in.Depth == 1 {
		n := in.MaxPages
		if n == 0 {
			n = 5
		}
		a.Details = append(a.Details, "And the pages it links to on the same site, "+strconv.Itoa(n)+" in all")
	}
	a.Details = append(a.Details, "Signed out: for a page as you see it signed in, use Send this page in the browser extension", where)
	setInput(a, "resources.fetch", in)
}

// describeResourceMake prepares making a code slide, a diagram or a
// repository card (`resources make code|diagram|stats`).
func (m *Manager) describeResourceMake(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	kind := strings.ToLower(first(call.Args))
	repo := ""
	if len(call.Args) > 1 {
		repo = strings.TrimSpace(call.Args[1])
	}
	if repo != "" && !resourceRef(repo) {
		incomplete(a, "Make a slide picture", "Claude did not name the repository by its short name.")
		return
	}
	prj, ok := projectFlag(call)
	if !ok {
		incomplete(a, "Make a slide picture", "Claude did not name the project by its ID.")
		return
	}
	name := strings.TrimSpace(call.Flags["name"])
	if name != "" && !resources.NamePattern.MatchString(name) {
		incomplete(a, "Make a slide picture", "The short name "+strconv.Quote(name)+" isn't one MetaRush can use (lowercase letters, digits, - and _).")
		return
	}
	theme := strings.ToLower(strings.TrimSpace(call.Flags["theme"]))
	if theme != "" && theme != "dark" && theme != "light" {
		incomplete(a, "Make a slide picture", "The theme is dark or light.")
		return
	}
	in := ops.ResourceMakeIn{Kind: kind, Repo: repo, Version: clip(strings.TrimSpace(call.Flags["version"]), 80), Theme: theme, Name: name,
		Title: clip(strings.TrimSpace(call.Flags["title"]), 200), ProjectID: prj, Workspace: hasBool(call, "workspace")}
	from := "From “" + repo + "”"
	if in.Version != "" {
		from += " at " + in.Version
	}
	switch kind {
	case "code":
		in.File, in.Lines, in.Highlight, in.Language = clip(strings.TrimSpace(call.Flags["file"]), 1024), strings.TrimSpace(call.Flags["lines"]), strings.TrimSpace(call.Flags["highlight"]), clip(call.Flags["language"], 40)
		if repo == "" || in.File == "" || in.Lines == "" {
			incomplete(a, "Make a code slide", "Claude did not name the repository, the file and the lines.")
			return
		}
		a.Title = "Make a code slide of " + in.File + ", lines " + strings.Replace(in.Lines, "-", "–", 1)
		a.Details = []string{from}
		if in.Highlight != "" {
			a.Details = append(a.Details, "Highlights lines "+strings.ReplaceAll(in.Highlight, "-", "–"))
		}
	case "diagram":
		src, format, why := m.diagramSource(c, call)
		if why != "" {
			incomplete(a, "Make a diagram", why)
			return
		}
		in.Source, in.Format = src, format
		a.Title = "Make a diagram"
		if in.Title != "" {
			a.Title = "Make the diagram “" + in.Title + "”"
		}
		lang := "Mermaid"
		if format == "d2" || (format == "" && strings.HasPrefix(strings.TrimSpace(src), "direction")) {
			lang = "D2"
		}
		lines := strings.Count(strings.TrimSpace(src), "\n") + 1
		a.Details = []string{"From Claude's description in " + lang + ", " + pluralize(lines, "line")}
		if boxes := mermaidLabels(src); lang == "Mermaid" && len(boxes) > 0 {
			a.Details = append(a.Details, "Shows: "+strings.Join(boxes, ", "))
		}
		if repo != "" {
			a.Details = append(a.Details, "About “"+repo+"”")
		}
	case "stats":
		if repo == "" {
			incomplete(a, "Make a repository card", "Claude did not name the repository.")
			return
		}
		a.Title = "Make a card about “" + repo + "”"
		a.Details = []string{"Its name, languages, files, contributors and licence"}
	default:
		incomplete(a, "Make a slide picture", "Claude asked for "+strconv.Quote(kind)+": a code slide (code), a diagram or a card (stats).")
		return
	}
	a.Details = append(a.Details, "Drawn in a browser that can't reach the network; it records exactly where it came from")
	if name != "" {
		a.Details = append(a.Details, "Graphics use it as "+name)
	}
	a.Details = append(a.Details, m.intoWhere(ctx, prj, in.Workspace))
	if prj == "" && !in.Workspace && repo != "" {
		a.Details[len(a.Details)-1] = "Next to the repository"
	}
	setInput(a, "resources.make", in)
}

// mermaidNode finds a Mermaid node with a label: id[Label], id(Label),
// id[(Label)], id((Label)), id{Label}, id>Label] and quoted labels.
var mermaidNode = regexp.MustCompile(`[A-Za-z0-9_]+\s*(?:\[\[|\[\(|\(\(|\{\{|\[|\(|\{|>)\s*"?([^\]\)\}"\n]{1,60})"?\s*(?:\]\]|\)\]|\)\)|\}\}|\]|\)|\})`)

// mermaidLabels are the labels of a Mermaid diagram's boxes, in order (at most
// eight), so the card says what the diagram will show.
func mermaidLabels(src string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range mermaidNode.FindAllStringSubmatch(src, -1) {
		l := strings.TrimSpace(m[1])
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		if out = append(out, l); len(out) == 8 {
			break
		}
	}
	return out
}

// diagramSource reads a diagram's description: --source, or --from a file in
// this conversation's proposals folder (never anywhere else). why is set when
// there is none to use.
func (m *Manager) diagramSource(c *conv, call *cliCall) (src, format, why string) {
	format = strings.ToLower(strings.TrimSpace(call.Flags["format"]))
	if format != "" && format != "mermaid" && format != "d2" {
		return "", "", "A diagram is written in Mermaid or D2."
	}
	from := strings.TrimSpace(call.Flags["from"])
	switch {
	case from == "" || from == "-":
		src = call.Flags["source"]
	case c == nil:
		return "", "", "Claude's diagram file is not in this conversation's proposals folder."
	default:
		p := from
		if !filepath.IsAbs(p) {
			p = filepath.Join(m.workspaceDir(), p)
		}
		real := func(s string) string {
			if rs, err := filepath.EvalSymlinks(s); err == nil {
				return rs
			}
			return s
		}
		root, file := real(filepath.Join(m.convDir(c.id), "proposals")), real(p)
		rel, err := filepath.Rel(root, file)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			return "", "", "Claude's diagram file is not in this conversation's proposals folder."
		}
		st, err := os.Stat(file)
		if err != nil || !st.Mode().IsRegular() {
			return "", "", "MetaRush can't read Claude's diagram file."
		}
		if st.Size() > maxDiagramSource {
			return "", "", "Claude's diagram is too long (at most 50,000 characters)."
		}
		b, err := os.ReadFile(file)
		if err != nil || !utf8.Valid(b) {
			return "", "", "MetaRush can't read Claude's diagram file."
		}
		src = string(b)
		if format == "" {
			switch strings.ToLower(filepath.Ext(file)) {
			case ".d2":
				format = "d2"
			case ".mmd", ".mermaid":
				format = "mermaid"
			}
		}
	}
	if strings.TrimSpace(src) == "" {
		return "", "", "Claude did not write the diagram's description."
	}
	if len(src) > maxDiagramSource {
		return "", "", "Claude's diagram is too long (at most 50,000 characters)."
	}
	return src, format, ""
}

// describeResourceRefresh prepares looking at a repository or page again (a
// new commit or snapshot; cuts keep theirs until they are updated).
func (m *Manager) describeResourceRefresh(ctx context.Context, call *cliCall, a *AssistantAction) {
	ref := strings.TrimSpace(first(call.Args))
	if !resourceRef(ref) {
		incomplete(a, "Look at a resource again", "Claude did not name the resource by its short name.")
		return
	}
	prj, ok := projectFlag(call)
	if !ok {
		incomplete(a, "Look at a resource again", "Claude did not name the project by its ID.")
		return
	}
	at := clip(strings.TrimSpace(call.Flags["ref"]), 200)
	a.Title = "Look at “" + ref + "” again"
	a.Details = []string{"A repository moves to its newest commit (or " + orDefaultName(at, "the branch it follows") + "), a web page gets a new snapshot",
		"Code slides and cards made from a repository get new versions too", "Cuts keep what they show until you update them"}
	if at != "" {
		a.Title = "Move “" + ref + "” to " + at
	}
	setInput(a, "resources.refresh", ops.ResourceRefreshIn{ResourceRefIn: ops.ResourceRefIn{Ref: ref, ProjectID: prj}, At: at})
}
