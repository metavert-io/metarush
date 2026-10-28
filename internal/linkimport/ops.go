package linkimport

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// ImportIn imports videos from links.
type ImportIn struct {
	URLs      []string `json:"urls" jsonschema:"minItems=1,maxItems=50,description=Links to videos on YouTube\\, TikTok or X (other sites the downloader supports work too)."`
	ProjectID string   `json:"projectId,omitempty" jsonschema:"description=Add the videos to this project (default: the inbox)."`
	// BrowserSignIn is opt-in for these imports only; the choice is remembered on
	// this machine as the next suggestion, never applied by itself.
	BrowserSignIn string `json:"browserSignIn,omitempty" jsonschema:"enum=chrome,enum=safari,enum=firefox,enum=brave,enum=edge,description=Download with this browser's sign-in (for a bot check or sign-in wall). The downloader reads the browser's cookies itself for this run; MetaRush never copies or keeps them."`
	Playlist      int    `json:"playlist,omitempty" jsonschema:"minimum=0,maximum=200,description=For a playlist or channel link: import its first N videos. Without it such links import nothing."`
	DryRun        bool   `json:"dryRun,omitempty" jsonschema:"description=Look the links up (title\\, account\\, length\\, license) without importing anything."`
}

// ImportRow is the outcome for one link.
type ImportRow struct {
	Input    string `json:"input"`
	URL      string `json:"url,omitempty"`
	Platform string `json:"platform,omitempty" jsonschema:"enum=youtube,enum=tiktok,enum=x,enum=web"`
	Kind     string `json:"kind,omitempty" jsonschema:"enum=video,enum=post,enum=playlist,enum=channel"`
	Status   string `json:"status" jsonschema:"enum=queued,enum=importing,enum=already,enum=refused,enum=would-import,description=queued = an import job will download it; importing = it is already being imported; already = it is in MetaRush (added to the project when one was given); refused = nothing was imported (see message); would-import = a dry run."`
	JobID    string `json:"jobId,omitempty"`
	AssetID  string `json:"assetId,omitempty"`
	Title    string `json:"title,omitempty"`
	Message  string `json:"message" jsonschema:"description=What happens\\, in plain words."`
	Next     string `json:"next,omitempty" jsonschema:"description=What to do next\\, when something needs the person."`
	Code     string `json:"code,omitempty" jsonschema:"description=Why it was refused (stable: playlist\\, not_a_link\\, local\\, no_video\\, unsupported\\, or an advice code from a dry-run lookup)."`
	// Source is what a dry run found out about the video.
	Source *models.AssetSource `json:"source,omitempty"`
	// Count is how many videos a playlist dry run found.
	Count int `json:"count,omitempty"`
}

// ImportOut lists what happened to each link.
type ImportOut struct {
	Imports []ImportRow `json:"imports"`
	Queued  int         `json:"queued"`
	Already int         `json:"already"`
	Refused int         `json:"refused"`
	JobIDs  []string    `json:"jobIds"`
	// Machine runs the imports (the one that asked); Running says whether
	// MetaRush is running there now (imports start at once).
	Machine string `json:"machine"`
	Running bool   `json:"running"`
	DryRun  bool   `json:"dryRun"`
	Note    string `json:"note,omitempty"`
}

// ImportToolsIn has no options.
type ImportToolsIn struct{}

// ImportConfigureIn changes link-import settings on this machine.
type ImportConfigureIn struct {
	AutoUpdate    *bool `json:"autoUpdate,omitempty" jsonschema:"description=Let the daemon update the downloader about once a week while no import runs."`
	ForgetBrowser bool  `json:"forgetBrowser,omitempty" jsonschema:"description=Forget which browser's sign-in this machine used last."`
}

// Running reports whether this installation's worker runs imports right now.
func Running(ctx context.Context, a *app.App) bool {
	return runningOn(ctx, a, a.Cfg.Installation.ID)
}

// runningOn reports whether a computer's worker runs imports right now.
func runningOn(ctx context.Context, a *app.App, installation string) bool {
	var w models.WorkerDevice
	if err := a.DB.GetByID(ctx, store.CWorkers, a.WorkspaceID(), installation, &w); err != nil {
		return false
	}
	if !jobs.Online(w, time.Now()) {
		return false
	}
	for _, t := range w.JobTypes {
		if t == jobs.TypeImport {
			return true
		}
	}
	return false
}

func refusal(code string, err error) (string, string) {
	switch {
	case errors.Is(err, ErrNotALink):
		return "That isn't a web link.", "Paste the address of a video, like https://www.youtube.com/watch?v=…"
	case errors.Is(err, ErrLocalLink):
		return "That link points at this computer or a local network, so MetaRush won't download from it.", ""
	}
	msg := err.Error()
	return strings.ToUpper(msg[:1]) + msg[1:] + ".", "Use a link to a single video on YouTube, TikTok or X."
}

func init() {
	ops.Def("ingest.url", "Import videos from links (YouTube, TikTok, X): looked up, downloaded on this machine, then added like dropped files (dedupe, preview, transcript, speakers), with the original post recorded on the clip", ops.Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *ImportIn) (*ImportOut, error) {
		if len(in.URLs) == 0 {
			return nil, apperr.New(apperr.InvalidInput, "Paste at least one link.")
		}
		if len(in.URLs) > 50 {
			return nil, apperr.New(apperr.InvalidInput, "Paste at most 50 links at a time.")
		}
		in.BrowserSignIn = strings.ToLower(strings.TrimSpace(in.BrowserSignIn))
		// The browser sign-in is this computer's person's own sign-ins: only an
		// owner may lend them to an import (D191).
		if in.BrowserSignIn != "" {
			if err := ops.RequireOwner(ctx, a, "ingest.url"); err != nil {
				return nil, err
			}
		}
		if in.BrowserSignIn != "" && !ValidBrowser(in.BrowserSignIn) {
			return nil, apperr.New(apperr.InvalidInput, "The browser sign-in can come from Chrome, Safari, Firefox, Brave or Edge.").WithDetails(map[string]any{"browsers": Browsers})
		}
		// Imported where this computer's background work runs: here, or the
		// computer that has its role now — except with this browser's
		// sign-in, which is only here (D349).
		host, err := a.PinWork(ctx)
		if err != nil {
			return nil, err
		}
		if in.BrowserSignIn != "" && !in.DryRun {
			if err := a.NeedsThisComputer(ctx, "a link import with your browser's sign-in"); err != nil {
				return nil, err
			}
		}
		if in.Playlist < 0 || in.Playlist > 200 {
			return nil, apperr.New(apperr.InvalidInput, "From a playlist, import between 1 and 200 videos.")
		}
		if in.DryRun && len(in.URLs) > 10 {
			return nil, apperr.New(apperr.InvalidInput, "Look up at most 10 links at a time.")
		}
		projectName := ""
		if in.ProjectID != "" {
			var p models.Project
			if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), in.ProjectID, &p); err != nil {
				// A project's name works too ("fernbrook").
				found, rerr := ops.ResolveProject(ctx, a, in.ProjectID)
				if rerr != nil {
					ae := apperr.From(rerr)
					return nil, apperr.New(apperr.NotFound, "%s", ae.Message).WithHint(ae.Hint).WithDetails(map[string]any{"project": in.ProjectID})
				}
				p = *found
				in.ProjectID = p.ID
			}
			projectName = p.Name
		}
		out := &ImportOut{Imports: []ImportRow{}, JobIDs: []string{}, Machine: host.Name, DryRun: in.DryRun}
		via := a.Actor.Via
		if via == "" {
			via = "cli"
		}
		seen := map[string]bool{}
		for _, raw := range in.URLs {
			row := ImportRow{Input: strings.TrimSpace(raw)}
			l, err := Classify(raw)
			if err != nil {
				row.Status, row.Code = "refused", "not_a_link"
				switch {
				case errors.Is(err, ErrLocalLink):
					row.Code = "local"
				case errors.Is(err, ErrNoVideo):
					row.Code = "no_video"
				case !errors.Is(err, ErrNotALink):
					row.Code = "unsupported"
				}
				row.Message, row.Next = refusal(row.Code, err)
				out.Imports = append(out.Imports, row)
				out.Refused++
				continue
			}
			row.URL, row.Platform, row.Kind = l.URL, l.Platform, l.Kind
			if seen[l.URL] {
				continue
			}
			seen[l.URL] = true
			if l.Many() && in.Playlist == 0 {
				what := "a playlist"
				if l.Kind == KindChannel {
					what = "a channel page"
					if l.Platform != models.PlatformYouTube {
						what = "a profile page"
					}
				}
				row.Status, row.Code = "refused", "playlist"
				row.Message = "This link is " + what + ", so nothing was imported. MetaRush imports one video at a time unless you ask for more."
				row.Next = "To bring in its first videos, say how many (the dashboard offers “Import the first 5”)."
				out.Imports = append(out.Imports, row)
				out.Refused++
				continue
			}
			if in.DryRun {
				out.Imports = append(out.Imports, dryRun(ctx, a, l, in))
				continue
			}
			if !l.Many() {
				if as := findImported(ctx, a, l.Platform, l.VideoID); as != nil {
					if in.ProjectID != "" {
						linkProject(ctx, a, as.ID, in.ProjectID)
					}
					row.Status, row.AssetID, row.Title = "already", as.ID, as.DisplayTitle()
					row.Message = "Already in MetaRush as “" + as.DisplayTitle() + "”."
					if projectName != "" {
						row.Message = "Already in MetaRush as “" + as.DisplayTitle() + "”; it's in " + projectName + " now."
					}
					out.Imports = append(out.Imports, row)
					out.Already++
					continue
				}
			}
			p := Payload{URL: l.URL, Input: row.Input, Platform: l.Platform, Kind: l.Kind, VideoID: l.VideoID, ListID: l.ListID, Browser: in.BrowserSignIn, Via: via, Limit: in.Playlist}
			j, created, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeImport, Payload: p.asMap(), IdempotencyKey: IdempotencyKey(host.ID, l, in.ProjectID),
				Priority: 9, MaxAttempts: 3, ProjectID: in.ProjectID, Actor: a.Actor, Installation: host.ID})
			if err != nil {
				return nil, err
			}
			row.JobID = j.ID
			switch {
			case created:
				row.Status, row.Message = "queued", "Importing "+describeLink(l, in.Playlist)+"."
			case j.State == models.JobQueued || j.State == models.JobRunning:
				row.Status, row.Message = "importing", "Already being imported."
				if in.BrowserSignIn != "" && j.State == models.JobQueued {
					_ = a.DB.Update(ctx, store.CJobs, a.WorkspaceID(), j.ID, bson.M{"$set": bson.M{"payload.browser": in.BrowserSignIn, "updatedAt": time.Now().UTC()}})
				}
			case j.State == models.JobFailed || j.State == models.JobCanceled:
				// The same import again (perhaps with the browser sign-in now): it
				// continues where it stopped.
				set := bson.M{"payload.browser": in.BrowserSignIn, "payload.via": via, "payload.limit": in.Playlist, "updatedAt": time.Now().UTC()}
				_ = a.DB.Update(ctx, store.CJobs, a.WorkspaceID(), j.ID, bson.M{"$set": set, "$unset": bson.M{"payload.advice": "", "payload.detail": ""}})
				if _, err := a.Queue.Retry(ctx, j.ID); err != nil {
					return nil, err
				}
				row.Status, row.Message = "queued", "Importing "+describeLink(l, in.Playlist)+" again."
				if in.BrowserSignIn != "" {
					row.Message = "Importing " + describeLink(l, in.Playlist) + " again, with your " + BrowserName(in.BrowserSignIn) + " sign-in."
				}
			default: // succeeded earlier, but the clip isn't found by its video ID (a post with several videos)
				row.Status, row.Message = "already", "Already imported."
				if id, _ := j.Result["assetId"].(string); id != "" {
					row.AssetID = id
					if in.ProjectID != "" {
						linkProject(ctx, a, id, in.ProjectID)
					}
				}
			}
			if row.Status == "queued" || row.Status == "importing" {
				out.JobIDs = append(out.JobIDs, j.ID)
				out.Queued++
			} else {
				out.Already++
			}
			if l.Note != "" && row.Status == "queued" {
				row.Message += " " + l.Note
			}
			out.Imports = append(out.Imports, row)
		}
		if in.DryRun {
			return out, nil
		}
		if in.BrowserSignIn != "" && out.Queued > 0 && a.Cfg.Imports.Browser != in.BrowserSignIn {
			// Remembered on this machine as the next suggestion (never used without asking).
			if err := config.SetField(a.Cfg.Home(), []string{"imports", "browser"}, in.BrowserSignIn); err == nil {
				a.Cfg.Imports.Browser = in.BrowserSignIn
			}
		}
		out.Running = runningOn(ctx, a, host.ID)
		switch {
		case out.Queued == 0:
		case out.Running:
			out.Note = "The import runs on " + host.Name + "; follow it in the activity bar."
		case !host.Here:
			out.Note = "It starts as soon as MetaRush is running on " + host.Name + ", which has this computer's role now."
		default:
			out.Note = "It starts as soon as MetaRush is running on " + host.Name + " (open MetaRush to start it)."
		}
		if out.Queued+out.Already > 0 {
			a.Audit(ctx, "import.url", "workspace", a.WorkspaceID(), in.ProjectID, map[string]any{"queued": out.Queued, "already": out.Already, "refused": out.Refused,
				"jobIds": out.JobIDs, "browserSignIn": in.BrowserSignIn, "via": via})
		}
		return out, nil
	})
	ops.Def("ingest.url.tools", "Check the downloader link imports use on this machine: yt-dlp version and age, how it updates, its JavaScript runtime for YouTube, PO-token plugins", ops.Flags{}, func(ctx context.Context, a *app.App, _ *ImportToolsIn) (*ImportTools, error) {
		st := Probe(ctx, a)
		return &st, nil
	})
	ops.Def("ingest.url.update", "Update the downloader (yt-dlp) the way it was installed: brew upgrade, or yt-dlp -U for the self-contained build; owners", ops.Flags{Mutating: true, OwnerOnly: true}, func(ctx context.Context, a *app.App, _ *ImportToolsIn) (*ImportToolsUpdate, error) {
		res, err := Update(ctx, a)
		if errors.Is(err, ErrSelfUpdateUnsupported) {
			// The message says what to do; res.How (in Details) is the command that updates it.
			return nil, apperr.New(apperr.ToolFailed, "%s", res.Message).WithDetails(res)
		}
		if err != nil {
			if res == nil {
				return nil, apperr.New(apperr.ToolMissing, "%s", err.Error()).
					WithHint("Add the link downloader in Settings → Components, or save videos with the MetaRush browser extension.").WithCommand("metarush components add link-import")
			}
			return nil, apperr.New(apperr.ToolFailed, "%s", res.Message).WithDetails(res)
		}
		return res, nil
	})
	ops.Def("ingest.url.configure", "Change link-import settings on this machine: automatic weekly downloader updates, forget the remembered browser; owners", ops.Flags{Mutating: true, OwnerOnly: true}, func(ctx context.Context, a *app.App, in *ImportConfigureIn) (*ImportTools, error) {
		if in.AutoUpdate != nil {
			if err := config.SetField(a.Cfg.Home(), []string{"imports", "autoUpdate"}, *in.AutoUpdate); err != nil {
				return nil, err
			}
			v := *in.AutoUpdate
			a.Cfg.Imports.AutoUpdate = &v
		}
		if in.ForgetBrowser {
			if err := config.SetField(a.Cfg.Home(), []string{"imports", "browser"}, ""); err != nil {
				return nil, err
			}
			a.Cfg.Imports.Browser = ""
		}
		a.Audit(ctx, "import.configure", "installation", a.Cfg.Installation.ID, "", map[string]any{"autoUpdate": in.AutoUpdate, "forgetBrowser": in.ForgetBrowser})
		st := Probe(ctx, a)
		return &st, nil
	})
}

// describeLink says what an import brings in: "the video from YouTube", "the
// first 5 videos of the playlist" (n is how many a list import asked for).
func describeLink(l Link, n int) string {
	switch l.Kind {
	case KindPlaylist:
		return fmt.Sprintf("the first %d videos of the playlist", n)
	case KindChannel:
		page := "channel"
		if l.Platform != models.PlatformYouTube {
			page = "profile"
		}
		return fmt.Sprintf("the latest %d videos of the %s", n, page)
	case KindPost:
		return "the video from the post on X"
	}
	return "the video from " + models.PlatformName(l.Platform)
}

// dryRun looks a link up without importing it.
func dryRun(ctx context.Context, a *app.App, l Link, in *ImportIn) ImportRow {
	row := ImportRow{Input: l.Input, URL: l.URL, Platform: l.Platform, Kind: l.Kind, Status: "would-import"}
	if !a.Tools.Has(tools.YtDlp) {
		adv := AdviceFor(AdviceMissing, l.Platform, "")
		row.Status, row.Code, row.Message, row.Next = "refused", adv.Code, adv.Message, adv.Next
		return row
	}
	r := &runner{a: a, browser: in.BrowserSignIn}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if l.Many() {
		info, err := r.lookup(ctx, r.listArgs(l.URL, in.Playlist), l.Platform)
		if err != nil {
			f := asFailure(err)
			row.Status, row.Code, row.Message, row.Next = "refused", f.Advice.Code, f.Advice.Message, f.Advice.Next
			return row
		}
		row.Title, row.Count = info.Title, min(len(info.Entries), in.Playlist)
		row.Message = fmt.Sprintf("Would import the first %d videos of “%s”", row.Count, firstNonEmpty(info.Title, "the list"))
		row.Message += listedTitles(info.Entries[:row.Count], 5) + "."
		return row
	}
	info, err := r.lookup(ctx, r.lookupArgs(l.URL), l.Platform)
	if err != nil {
		f := asFailure(err)
		row.Status, row.Code, row.Message, row.Next = "refused", f.Advice.Code, f.Advice.Message, f.Advice.Next
		return row
	}
	if info.Type == "playlist" && len(info.Entries) > 0 {
		row.Count = min(len(info.Entries), maxPostVideos)
		first := info.Entries[0]
		info = &first
	}
	src := Provenance(l.Platform, info, "yt-dlp", a.Tools.Version(ctx, tools.YtDlp), in.BrowserSignIn != "", time.Now())
	row.Source, row.Title = &src, src.Title
	switch {
	case info.Live():
		adv := AdviceFor(AdviceLive, l.Platform, "")
		row.Status, row.Code, row.Message, row.Next = "refused", adv.Code, adv.Message, adv.Next
	case info.DRM():
		adv := AdviceFor(AdviceDRM, l.Platform, "")
		row.Status, row.Code, row.Message, row.Next = "refused", adv.Code, adv.Message, adv.Next
	default:
		row.Message = "Would import " + Summary(l.Platform, src) + "."
		if src.License != "" {
			row.Message += " License: " + src.License + "."
		}
		if row.Count > 1 {
			row.Message += fmt.Sprintf(" The post has %d videos; each becomes a clip.", row.Count)
		}
	}
	return row
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	ops.Offer("ingest.url", ops.Offering{ID: "video-links", Group: "bring", Title: "Videos from links",
		Does:  "Imports a video from a link, downloaded on this computer, with where it came from.",
		Words: []string{"download a video", "paste a link"}, Needs: ops.Needs{Programs: []string{"yt-dlp"}, Works: []string{"editing"}}})
}
