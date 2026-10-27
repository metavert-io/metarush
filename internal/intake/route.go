// Package intake is the Add box (D80): one classifier and one set of routes
// for everything people add — dropped or chosen files, folders and pasted
// links, in the dashboard (Home, a project's Sources and Resources tabs, a
// drop anywhere) and in the terminal (`metarush add`, `metarush resources
// add`). Video and audio go to footage (internal/ingest), pictures, SVGs,
// PDFs and fonts to the resources library (internal/resources), folders
// become linked folders kept in step (D81), and links go to the link import
// (YouTube, TikTok, X and video sites) or, for GitHub repositories and web
// pages, to the URL sources part 2 fills in (resources.RegisterURLSource).
// It asks only when a link could be either a video or a page.
package intake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
)

// RouteIn asks where things would go, before they are sent.
type RouteIn struct {
	// Names are file names (the dashboard asks this to say "Drop to add 2
	// pictures to Resources and a video to Sources"); the server decides by
	// content once a file arrives.
	Names []string `json:"names,omitempty" jsonschema:"maxItems=2000"`
	// Text is pasted or dropped text; the links in it are routed.
	Text string `json:"text,omitempty" jsonschema:"maxLength=20000"`
}

// FileRoute is where a file goes.
type FileRoute struct {
	Name  string `json:"name"`
	Route string `json:"route" jsonschema:"enum=footage,enum=resource,enum=unknown"`
	Kind  string `json:"kind,omitempty" jsonschema:"description=For resources: image\\, svg\\, pdf or font."`
	// Convert: a HEIC or AVIF photo becomes a JPEG on the way in.
	Convert bool `json:"convert,omitempty"`
}

// LinkRoute is where a link goes.
type LinkRoute struct {
	Input string `json:"input"`
	URL   string `json:"url,omitempty"`
	// Route: video (the link import), repo (a GitHub repository), page (a web
	// page or a picture on the web), ambiguous (ask: video or page?), invalid.
	Route    string `json:"route" jsonschema:"enum=video,enum=repo,enum=page,enum=ambiguous,enum=invalid"`
	Platform string `json:"platform,omitempty"`
	// Ready says MetaRush can bring it in now (repositories and pages come with part 2).
	Ready   bool   `json:"ready"`
	Message string `json:"message"`
}

// RouteOut is the classifier's answer.
type RouteOut struct {
	Files []FileRoute `json:"files"`
	Links []LinkRoute `json:"links"`
	// Summary says it in a few words ("2 pictures and a video").
	Summary string `json:"summary"`
}

// AddLinksIn brings in links through the Add box.
type AddLinksIn struct {
	URLs      []string `json:"urls" jsonschema:"minItems=1,maxItems=50"`
	ProjectID string   `json:"projectId,omitempty" jsonschema:"description=The project they go into (default: the Inbox / the workspace library)."`
	// As answers the question for links that could be either: video (import
	// the video) or page (use the page in graphics, part 2).
	As            string `json:"as,omitempty" jsonschema:"enum=video,enum=page"`
	BrowserSignIn string `json:"browserSignIn,omitempty" jsonschema:"enum=chrome,enum=safari,enum=firefox,enum=brave,enum=edge"`
	Playlist      int    `json:"playlist,omitempty" jsonschema:"minimum=0,maximum=200"`
	// Depth 1 also takes the same-site pages a web page links to (MaxPages in all).
	Depth    int `json:"depth,omitempty" jsonschema:"minimum=0,maximum=1"`
	MaxPages int `json:"maxPages,omitempty" jsonschema:"minimum=0,maximum=20"`
}

// LinkOutcome is what happened to one link.
type LinkOutcome struct {
	Input  string `json:"input"`
	Route  string `json:"route" jsonschema:"enum=video,enum=repo,enum=page,enum=ambiguous,enum=invalid"`
	Status string `json:"status" jsonschema:"enum=importing,enum=fetching,enum=later,enum=ask,enum=refused,enum=added,description=importing: handed to the link import (see imports); fetching: a repository or page is being brought in as resources (jobId); later: not supported yet; ask: could be a video or a page (answer with as); refused: not a link MetaRush can use; added: brought in as resources."`
	// JobID is the fetch that brings a repository or a page in.
	JobID string `json:"jobId,omitempty"`
	// Choices for ask: video, page.
	Choices []string `json:"choices,omitempty"`
	Message string   `json:"message"`
}

// AddLinksOut is the outcome; Imports is the link import's answer for the
// video links (the dashboard's import toasts read it).
type AddLinksOut struct {
	Links   []LinkOutcome         `json:"links"`
	Imports *linkimport.ImportOut `json:"imports,omitempty"`
	Summary string                `json:"summary"`
}

// RouteLink classifies one address.
func RouteLink(raw string) LinkRoute {
	lr := LinkRoute{Input: raw}
	l, err := linkimport.Classify(raw)
	if err != nil && errors.Is(err, linkimport.ErrLocalLink) && resources.AllowLocalPages() {
		// Tests take snapshots of a page served on this computer (never in normal use).
		lr.URL, lr.Platform, lr.Route, lr.Ready = strings.TrimSpace(raw), models.PlatformWeb, resources.LinkPage, true
		if rr, ok := resources.ParseRepoURL(raw); ok {
			lr.URL, lr.Route = rr.URL(), resources.LinkRepo
		}
		lr.Message = readyMessage(lr.Route, lr.URL)
		return lr
	}
	if err != nil {
		lr.Route = "invalid"
		lr.Message = upper(err.Error()) + "."
		if errors.Is(err, linkimport.ErrNotALink) {
			lr.Message = "That isn't a web address MetaRush can use."
		}
		return lr
	}
	lr.URL, lr.Platform = l.URL, l.Platform
	if l.Platform != models.PlatformWeb {
		lr.Route, lr.Ready = resources.LinkVideo, true
		lr.Message = "A video on " + models.PlatformName(l.Platform) + ": MetaRush downloads it and adds it as footage."
		return lr
	}
	route, u := resources.RouteWebAddress(l.URL)
	lr.Route = route
	switch route {
	case resources.LinkVideo:
		lr.Ready = true
		lr.Message = "A video on the web: MetaRush downloads it and adds it as footage."
	case resources.LinkRepo, resources.LinkPage:
		src := resources.URLSourceFor(route, u)
		lr.Ready = src != nil && !isPlaceholder(src)
		lr.Message = resources.ComingSoonMessage(route)
		if lr.Ready {
			lr.Message = readyMessage(route, l.URL)
		}
	default:
		lr.Message = "This could be a video to import or a web page to use in graphics."
	}
	return lr
}

// readyMessage says what MetaRush does with a repository or a page.
func readyMessage(route, raw string) string {
	if route == resources.LinkRepo {
		return "A GitHub repository: MetaRush keeps a read-only copy of its files as they are now, for code slides, diagrams and a stats card."
	}
	if u, err := url.Parse(raw); err == nil && pictureLink(u.Path) {
		return "A picture on the web: MetaRush downloads it into Resources, noting where it came from."
	}
	return "A web page: MetaRush takes a dated snapshot (screenshots for a computer and a phone, the whole page, and its text)."
}

func pictureLink(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg", ".pdf", ".heic", ".avif":
		return true
	}
	return false
}

// isPlaceholder reports a URL source part 1 registered without an implementation.
func isPlaceholder(s resources.URLSource) bool {
	_, err := s.Add(context.Background(), nil, resources.URLAddIn{})
	return errors.Is(err, resources.ErrComingSoon)
}

func upper(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// findLinks pulls web addresses out of pasted text.
func findLinks(text string) []string {
	var out []string
	for _, w := range strings.Fields(text) {
		w = strings.Trim(w, "<>()[]{}\"',;")
		if strings.HasPrefix(w, "http://") || strings.HasPrefix(w, "https://") {
			if u, err := url.Parse(w); err == nil && u.Host != "" {
				dup := false
				for _, x := range out {
					dup = dup || x == w
				}
				if !dup {
					out = append(out, w)
				}
			}
		}
	}
	return out
}

func summarizeRoutes(files []FileRoute) string {
	var pics, videos, other int
	for _, f := range files {
		switch f.Route {
		case resources.RouteResource:
			pics++
		case resources.RouteFootage:
			videos++
		default:
			other++
		}
	}
	var parts []string
	if videos > 0 {
		parts = append(parts, count(videos, "video or sound file", "videos or sound files"))
	}
	if pics > 0 {
		parts = append(parts, count(pics, "picture, PDF or font", "pictures, PDFs or fonts"))
	}
	if other > 0 {
		parts = append(parts, count(other, "other file", "other files"))
	}
	return strings.Join(parts, " and ")
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func init() {
	ops.Def("add.route", "Say where files and links would go if added: footage, resources, a linked folder, a link import, a GitHub repository or a web page snapshot", ops.Flags{}, func(ctx context.Context, a *app.App, in *RouteIn) (*RouteOut, error) {
		out := &RouteOut{Files: []FileRoute{}, Links: []LinkRoute{}}
		for _, n := range in.Names {
			s := resources.RouteByName(n)
			out.Files = append(out.Files, FileRoute{Name: n, Route: s.Route, Kind: s.Kind, Convert: s.Convert})
		}
		for _, l := range findLinks(in.Text) {
			out.Links = append(out.Links, RouteLink(l))
		}
		out.Summary = summarizeRoutes(out.Files)
		if len(out.Links) > 0 {
			if out.Summary != "" {
				out.Summary += " and "
			}
			out.Summary += count(len(out.Links), "link", "links")
		}
		return out, nil
	})
	ops.Def("add.links", "Bring in pasted or dropped links: videos (YouTube, TikTok, X, video sites) are imported as footage; GitHub repositories (pinned at a commit) and web pages (dated snapshots) become resources; asks when a link could be either", ops.Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *AddLinksIn) (*AddLinksOut, error) {
		out := &AddLinksOut{Links: []LinkOutcome{}}
		var videos []string
		for _, raw := range in.URLs {
			lr := RouteLink(raw)
			o := LinkOutcome{Input: raw, Route: lr.Route, Message: lr.Message}
			route := lr.Route
			if route == resources.LinkAmbiguous {
				switch in.As {
				case "video":
					route = resources.LinkVideo
				case "page":
					route = resources.LinkPage
				}
			}
			switch route {
			case "invalid":
				o.Status = "refused"
			case resources.LinkVideo:
				o.Status = "importing"
				videos = append(videos, raw)
				continue
			case resources.LinkAmbiguous:
				o.Status, o.Choices = "ask", []string{"video", "page"}
				o.Message = "Is this a video to import, or a web page to use in graphics?"
			case resources.LinkRepo, resources.LinkPage:
				u, _ := url.Parse(lr.URL)
				if u == nil {
					u, _ = url.Parse(strings.TrimSpace(raw))
				}
				src := resources.URLSourceFor(route, u)
				if src == nil {
					o.Status, o.Message = "later", resources.ComingSoonMessage(route)
					break
				}
				pid := ""
				if strings.TrimSpace(in.ProjectID) != "" {
					p, err := ops.ResolveProject(ctx, a, in.ProjectID)
					if err != nil {
						return nil, err
					}
					pid = p.ID
				}
				res, err := src.Add(ctx, a, resources.URLAddIn{URL: u.String(), ProjectID: pid, Depth: in.Depth, MaxPages: in.MaxPages})
				switch {
				case errors.Is(err, resources.ErrComingSoon):
					o.Status, o.Message = "later", resources.ComingSoonMessage(route)
				case err != nil:
					o.Status, o.Message = "refused", apperr.From(err).Message
				case res.JobID != "":
					o.Status, o.Message, o.JobID = "fetching", res.Message, res.JobID
				default:
					o.Status, o.Message = "added", res.Message
				}
			}
			out.Links = append(out.Links, o)
		}
		if len(videos) > 0 {
			raw, _ := json.Marshal(linkimport.ImportIn{URLs: videos, ProjectID: in.ProjectID, BrowserSignIn: in.BrowserSignIn, Playlist: in.Playlist})
			res, err := ops.Call(ctx, a, "ingest.url", raw, ops.CallerFrom(ctx))
			if err != nil {
				return nil, err
			}
			imp := res.(*linkimport.ImportOut)
			out.Imports = imp
			for _, r := range imp.Imports {
				st := "importing"
				if r.Status == "refused" {
					st = "refused"
				}
				out.Links = append(out.Links, LinkOutcome{Input: r.Input, Route: resources.LinkVideo, Status: st, Message: r.Message})
			}
		}
		var parts []string
		n := map[string]int{}
		for _, l := range out.Links {
			n[l.Status]++
		}
		if n["importing"] > 0 {
			parts = append(parts, count(n["importing"], "video is being imported", "videos are being imported"))
		}
		if n["fetching"] > 0 {
			parts = append(parts, count(n["fetching"], "repository or page is being brought in", "repositories or pages are being brought in"))
		}
		if n["added"] > 0 {
			parts = append(parts, count(n["added"], "repository or page was added", "repositories or pages were added"))
		}
		if n["later"] > 0 {
			parts = append(parts, count(n["later"], "link can't be added yet", "links can't be added yet"))
		}
		if n["ask"] > 0 {
			parts = append(parts, count(n["ask"], "link needs an answer", "links need an answer"))
		}
		if n["refused"] > 0 {
			parts = append(parts, count(n["refused"], "link can't be used", "links can't be used"))
		}
		out.Summary = "Nothing to add." // every link has one of the outcomes above; never "."
		if len(parts) > 0 {
			out.Summary = upper(strings.Join(parts, "; ")) + "."
		}
		return out, nil
	})
}
