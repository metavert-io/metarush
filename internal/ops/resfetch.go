package ops

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/resources"
)

// Repositories, web pages and what MetaRush makes from them (D104–D106):
// resources.fetch brings a GitHub repository (pinned at a commit) or a web
// page (a dated snapshot) in through a fetch job on this machine;
// resources.make draws a code slide, a diagram or a repository card.

// ResourceFetchIn brings in a repository or a web page.
type ResourceFetchIn struct {
	URL       string `json:"url" jsonschema:"minLength=1,maxLength=2000,description=A GitHub repository (https://github.com/owner/repo\\, a /tree/<branch> address works too) or a web page."`
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=The project (ID or name); empty: the workspace library."`
	Workspace bool   `json:"workspace,omitempty" jsonschema:"description=The workspace library (every project can use it)."`
	// As says what the address is when it could be either (default: told by the address).
	As  string `json:"as,omitempty" jsonschema:"enum=repo,enum=page,enum=picture"`
	Ref string `json:"ref,omitempty" jsonschema:"maxLength=200,description=A repository's branch\\, tag or commit (default: its default branch)."`
	// Depth 1 also takes the same-site pages a page links to, MaxPages in all (default 5).
	Depth    int    `json:"depth,omitempty" jsonschema:"minimum=0,maximum=1"`
	MaxPages int    `json:"maxPages,omitempty" jsonschema:"minimum=0,maximum=20"`
	Name     string `json:"name,omitempty" jsonschema:"pattern=^[a-z][a-z0-9_-]{0\\,47}$,description=The short name (one repository or page)."`
	Title    string `json:"title,omitempty" jsonschema:"maxLength=200"`
	// Pictures are a repository's folders or files whose pictures MetaRush
	// keeps too (D216), each a picture of its own pinned to the commit.
	Pictures []string `json:"pictures,omitempty" jsonschema:"maxItems=20,description=A repository: folders or files whose pictures (PNG\\, JPEG\\, WebP\\, GIF\\, SVG) are kept as pictures of their own (. is the whole repository)."`
}

// ResourceFetchOut says what happens.
type ResourceFetchOut struct {
	Kind   string `json:"kind" jsonschema:"enum=repo,enum=page,enum=picture"`
	Status string `json:"status" jsonschema:"enum=fetching,enum=already,description=fetching: a job brings it in (see jobId); already: the same fetch is waiting or running."`
	JobID  string `json:"jobId"`
	// Machine runs the fetch (this one); Running says whether its worker runs now.
	Machine string `json:"machine"`
	Running bool   `json:"running"`
	Message string `json:"message"`
	// Note says when it starts if MetaRush isn't running on this machine now.
	Note string `json:"note,omitempty"`
	// ProjectID is the project it goes into (empty: the workspace library).
	ProjectID string `json:"projectId,omitempty"`
}

// ResourceMakeIn makes a code slide, a diagram or a repository card.
type ResourceMakeIn struct {
	Kind string `json:"kind" jsonschema:"enum=code,enum=diagram,enum=stats,description=code: a code slide (a file's lines\\, highlighted); diagram: a Mermaid or D2 diagram drawn as SVG; stats: a card about a repository."`
	// Repo is the repository it is made from (its name or ID); Version a commit or version of it.
	Repo    string `json:"repo,omitempty" jsonschema:"description=The repository (short name\\, title or ID). Needed for code and stats; for a diagram it records where the diagram came from."`
	Version string `json:"version,omitempty" jsonschema:"description=A version of the repository (a commit or version hash\\, first characters are enough); default the current one."`
	// Code slides.
	File      string `json:"file,omitempty" jsonschema:"maxLength=1024,description=The file inside the repository (code)."`
	Lines     string `json:"lines,omitempty" jsonschema:"pattern=^L?[0-9]{1\\,6}(-L?[0-9]{1\\,6})?$,description=The lines to show\\, like 40-72 (code; at most 80)."`
	Highlight string `json:"highlight,omitempty" jsonschema:"pattern=^[0-9]{1\\,6}(-[0-9]{1\\,6})?(\\,[0-9]{1\\,6}(-[0-9]{1\\,6})?)*$,description=Lines to highlight\\, like 44-46\\,60 (lines of the file)."`
	Language  string `json:"language,omitempty" jsonschema:"maxLength=40,description=The code's language (default: from the file name)."`
	// Diagrams.
	Format string `json:"format,omitempty" jsonschema:"enum=mermaid,enum=d2,description=The diagram's language (default: told by its first word)."`
	Source string `json:"source,omitempty" jsonschema:"maxLength=50000,description=The diagram's description in Mermaid or D2."`
	// Everything.
	Theme     string `json:"theme,omitempty" jsonschema:"enum=dark,enum=light,description=Default: from the brand kit's background."`
	Name      string `json:"name,omitempty" jsonschema:"pattern=^[a-z][a-z0-9_-]{0\\,47}$,description=The short name (a name that is already there gets a new version)."`
	Title     string `json:"title,omitempty" jsonschema:"maxLength=200"`
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=The project it goes into (ID or name); default: next to the repository."`
	Workspace bool   `json:"workspace,omitempty"`
}

// ResourceMakeOut is what was made.
type ResourceMakeOut struct {
	Resource ResourceRow `json:"resource"`
	Status   string      `json:"status" jsonschema:"enum=added,enum=version,enum=same"`
	// Provenance is exactly where it came from: repo@commit:path:lines.
	Provenance string   `json:"provenance,omitempty"`
	Message    string   `json:"message"`
	Notes      []string `json:"notes,omitempty"`
}

// ResourceRefreshIn looks at a resource again; a repository moves to its
// newest commit and a web page gets a new snapshot (new versions).
type ResourceRefreshIn struct {
	ResourceRefIn
	At string `json:"at,omitempty" jsonschema:"maxLength=200,description=A repository: the branch\\, tag or commit to pin (default: the one it follows)."`
}

// fetchKind tells what an address is.
func fetchKind(raw, as string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "", apperr.New(apperr.InvalidInput, "That isn't a web address MetaRush can use.").WithHint("Give a GitHub repository (https://github.com/owner/repo) or a web page (https://…).")
	}
	switch as {
	case resources.FetchRepo, resources.FetchPage, resources.FetchPicture:
		if as == resources.FetchRepo {
			if _, ok := resources.ParseRepoURL(raw); !ok {
				return "", apperr.New(apperr.InvalidInput, "That isn't a GitHub repository's address.").WithHint("Like https://github.com/owner/repo.")
			}
		}
		return as, nil
	case "":
	default:
		return "", apperr.New(apperr.InvalidInput, "Say whether it's a repository, a web page or a picture (repo, page or picture).")
	}
	if _, ok := resources.ParseRepoURL(raw); ok {
		return resources.FetchRepo, nil
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg", ".pdf", ".heic", ".avif", ".ttf", ".otf", ".woff2":
		return resources.FetchPicture, nil
	}
	return resources.FetchPage, nil
}

// FetchURL queues a fetch (resources.fetch, the Add box and `metarush add`).
func FetchURL(ctx context.Context, a *app.App, in ResourceFetchIn) (*ResourceFetchOut, error) {
	kind, err := fetchKind(in.URL, in.As)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(strings.TrimSpace(in.URL))
	if !resources.AllowLocalPages() && resources.LocalAddress(u.Hostname()) {
		return nil, apperr.New(apperr.InvalidInput, "That address is on this computer or a local network, so MetaRush won't fetch it.")
	}
	projectID := ""
	if strings.TrimSpace(in.ProjectID) != "" && !in.Workspace {
		p, err := resolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		projectID = p.ID
	}
	req := resources.FetchRequest{Kind: kind, URL: u.String(), ProjectID: projectID, Workspace: projectID == "", Ref: strings.TrimSpace(in.Ref), Depth: in.Depth,
		MaxPages: in.MaxPages, Name: in.Name, Title: in.Title}
	if len(in.Pictures) > 0 {
		if kind != resources.FetchRepo {
			return nil, apperr.New(apperr.InvalidInput, "Only a repository keeps the pictures in its folders.").WithHint("Pictures from a web page are kept with its snapshot.")
		}
		ch, err := resources.ChangePicturePaths(nil, in.Pictures, nil)
		if err != nil {
			return nil, err
		}
		req.Pictures = ch.Paths
	}
	if kind == resources.FetchRepo {
		r, _ := resources.ParseRepoURL(in.URL)
		req.URL = r.URL()
		if req.Ref == "" {
			req.Ref = r.Ref
		}
	}
	q, err := resources.QueueFetch(ctx, a, req)
	if err != nil {
		return nil, err
	}
	out := &ResourceFetchOut{Kind: kind, Status: q.Status, JobID: q.JobID, Machine: a.Cfg.Installation.Name, Message: q.Message, ProjectID: projectID}
	ws, _ := OnlineWorkers(ctx, a, jobs.TypeFetch)
	for _, w := range ws {
		if w.ID == a.Cfg.Installation.ID {
			out.Running = true
		}
	}
	if !out.Running {
		out.Note = "It starts as soon as MetaRush is running on " + a.Cfg.Installation.Name + "."
	}
	return out, nil
}

// RepoSummary is a repository resource's current version in a row.
type RepoSummary struct {
	Commit      string                `json:"commit"`
	CommittedAt time.Time             `json:"committedAt"`
	Subject     string                `json:"subject,omitempty"`
	Ref         string                `json:"ref,omitempty"`
	URL         string                `json:"url"`
	Files       int                   `json:"files"`
	Skipped     int                   `json:"skipped,omitempty"`
	Languages   []models.RepoLanguage `json:"languages,omitempty"`
	License     string                `json:"license,omitempty"`
	Stars       int                   `json:"stars,omitempty"`
	Private     bool                  `json:"private,omitempty"`
}

// RepoDetails is what `resources show` tells about a repository version.
type RepoDetails struct {
	RepoSummary
	Description  string                   `json:"description,omitempty"`
	Readme       string                   `json:"readme,omitempty" jsonschema:"description=The start of its README (the whole file is in the read-only copy)."`
	Tree         []RepoTreeRow            `json:"tree" jsonschema:"description=Its top-level folders and files\\, with how many files each holds."`
	Recent       []models.RepoCommit      `json:"recent,omitempty"`
	Contributors []models.RepoContributor `json:"contributors,omitempty"`
	SkippedWhy   []string                 `json:"skippedWhy,omitempty"`
	History      string                   `json:"history,omitempty" jsonschema:"description=gh: GitHub said (with your gh sign-in); git: read from the commits fetched."`
	// Pictures are what it keeps from its picture paths at this commit (D216;
	// the first pictures listed, all counted).
	Pictures *RepoPictures `json:"pictures,omitempty"`
}

// repoDetailsPictures is how many kept pictures a repository's overview lists.
const repoDetailsPictures = 48

// RepoTreeRow is one top-level folder or file of a repository.
type RepoTreeRow struct {
	Path  string `json:"path"`
	Dir   bool   `json:"dir"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

func repoSummary(v *models.ResourceVersion) *RepoSummary {
	if v == nil || v.Repo == nil {
		return nil
	}
	rs := v.Repo
	return &RepoSummary{Commit: rs.Commit, CommittedAt: rs.CommittedAt, Subject: rs.Subject, Ref: rs.Ref, URL: v.Source.URL, Files: rs.Files, Skipped: rs.Skipped,
		Languages: rs.Languages, License: rs.License, Stars: rs.Stars, Private: rs.Private}
}

// repoDetails reads a repository version's overview.
func repoDetails(ctx context.Context, a *app.App, r *models.Resource, v *models.ResourceVersion) *RepoDetails {
	s := repoSummary(v)
	if s == nil {
		return nil
	}
	d := &RepoDetails{RepoSummary: *s, Description: v.Repo.Description, Readme: v.Repo.Readme, Recent: v.Repo.Recent, Contributors: v.Repo.Contributors,
		SkippedWhy: v.Repo.SkippedWhy, History: v.Repo.History, Tree: []RepoTreeRow{}}
	if len(r.PicturePaths) > 0 || len(v.Repo.Pictures) > 0 {
		if pics, err := RepoPicturesOf(ctx, a, r, v, "", repoDetailsPictures); err == nil {
			d.Pictures = pics
		}
	}
	ov, err := resources.ReadOverview(ctx, a, v)
	if err != nil {
		return d
	}
	if ov.Readme != "" {
		d.Readme = ov.Readme
		if len(d.Readme) > 4000 {
			d.Readme = d.Readme[:4000] + "…"
		}
	}
	top := map[string]*RepoTreeRow{}
	for _, f := range ov.Files {
		head, rest, isDir := strings.Cut(f.Path, "/")
		_ = rest
		t := top[head]
		if t == nil {
			t = &RepoTreeRow{Path: head, Dir: isDir}
			top[head] = t
		}
		t.Files++
		t.Bytes += f.Bytes
	}
	for _, t := range top {
		d.Tree = append(d.Tree, *t)
	}
	sort.Slice(d.Tree, func(i, j int) bool {
		if d.Tree[i].Dir != d.Tree[j].Dir {
			return d.Tree[i].Dir
		}
		return d.Tree[i].Path < d.Tree[j].Path
	})
	if len(d.Tree) > 80 {
		d.Tree = d.Tree[:80]
	}
	return d
}

func init() {
	Def("resources.fetch", "Bring in a GitHub repository (pinned at a commit: its text files, README, languages and history; binaries and large files left out) or a web page (a dated snapshot: screenshots for a computer and a phone, the whole page, its text, title, colours and icon). It runs as a job on this machine", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *ResourceFetchIn) (*ResourceFetchOut, error) {
		return FetchURL(ctx, a, *in)
	})
	Def("resources.make", "Make a code slide (a repository file's lines, highlighted), a diagram (Mermaid or D2, drawn as SVG) or a repository card, as a resource that says exactly where it came from", Flags{Mutating: true, LocalOnly: true}, func(ctx context.Context, a *app.App, in *ResourceMakeIn) (*ResourceMakeOut, error) {
		o := resources.MakeOptions{Kind: in.Kind, File: in.File, Highlight: in.Highlight, Language: in.Language, Format: in.Format, Source: in.Source, Theme: in.Theme,
			Name: in.Name, Title: in.Title, Workspace: in.Workspace, RepoSHA: strings.ToLower(strings.TrimSpace(in.Version))}
		pid := ""
		if strings.TrimSpace(in.ProjectID) != "" && !in.Workspace {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			pid = p.ID
			o.ProjectID = p.ID
		}
		if strings.TrimSpace(in.Repo) != "" {
			e, _, err := findResource(ctx, a, ResourceRefIn{Ref: in.Repo, ProjectID: pid})
			if err != nil {
				return nil, err
			}
			o.Repo = &e.Resource
		}
		if in.Lines != "" {
			from, to, err := resources.ParseLines(in.Lines)
			if err != nil {
				return nil, err
			}
			o.From, o.To = from, to
		}
		if in.Kind == resources.MakeCode && strings.TrimSpace(in.File) == "" {
			return nil, apperr.New(apperr.InvalidInput, "Say which file of the repository the code slide shows.").WithCommand("metarush resources path <repo>")
		}
		res, err := resources.Make(ctx, a, o)
		if err != nil {
			return nil, err
		}
		ar := res.Add
		viewPID := ar.Resource.ProjectID
		_, v, _ := resourceScope(ctx, a, viewPID)
		e := resources.Entry{Resource: *ar.Resource, Origin: resources.FromWorkspace}
		if ar.Resource.Scope == models.ScopeProject {
			e.Origin = resources.FromHere
		}
		out := &ResourceMakeOut{Resource: ResourceRowOf(e, v, folderNames(ctx, a)), Status: ar.Status, Provenance: res.Provenance, Notes: append(ar.Notes, res.Notes...)}
		switch ar.Status {
		case resources.StatusAdded:
			out.Message = fmt.Sprintf("Made %s (%s).", ar.Resource.Title, ar.Resource.Name)
		case resources.StatusVersion:
			out.Message = fmt.Sprintf("Made a new version of %s (%s). Cuts that show it keep the older one until you update them.", ar.Resource.Title, ar.Resource.Name)
		default:
			out.Message = ar.Message
		}
		return out, nil
	})
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	Offer("resources.fetch",
		Offering{ID: "github-repositories", Group: "bring", Title: "GitHub repositories", Input: map[string]any{"as": "repo"},
			Does:  "Brings in a GitHub repository at a commit — its text files, README and history — with your own git sign-in.",
			Words: []string{"git repo", "source code", "codebase"}, Needs: Needs{Programs: []string{"git"}}},
		Offering{ID: "web-pages", Group: "bring", Title: "Web pages", Input: map[string]any{"as": "page"},
			Does:  "Takes a dated snapshot of a web page: pictures of it on a computer and a phone, its words, colours and logo.",
			Words: []string{"website", "screenshot of a site", "landing page"}, Needs: Needs{Works: []string{"renders"}}})
	Offer("resources.make",
		Offering{ID: "code-slides", Group: "make", Title: "Code slides", Input: map[string]any{"kind": "code"},
			Does:  "A slide of a repository file's lines, highlighted, that says exactly where they came from.",
			Words: []string{"show code", "code snippet"}, Needs: Needs{Works: []string{"renders"}}},
		Offering{ID: "diagrams", Group: "make", Title: "Diagrams", Input: map[string]any{"kind": "diagram"},
			Does:  "Draws a diagram described in Mermaid or D2 as a picture for the cut.",
			Words: []string{"flowchart", "chart of boxes and arrows"}, Needs: Needs{Works: []string{"renders"}}},
		Offering{ID: "repository-cards", Group: "make", Title: "Repository cards", Input: map[string]any{"kind": "stats"},
			Does:  "A card about a repository: its languages, how active it is and its history.",
			Words: []string{"project stats", "repo card"}, Needs: Needs{Works: []string{"renders"}}})
}
