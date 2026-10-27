package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
)

// RelatedRef names an object a job belongs to, with where it lives in the dashboard.
type RelatedRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Link string `json:"link"`
}

// RelatedAsset is the clip a job worked on.
type RelatedAsset struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	Duration  float64 `json:"duration"`
	HasThumb  bool    `json:"hasThumb"`
	ProjectID string  `json:"projectId,omitempty"`
	Link      string  `json:"link"`
}

// RelatedRender is the render a render (or QA) job is about.
type RelatedRender struct {
	ID             string           `json:"id"`
	State          string           `json:"state"`
	Preset         string           `json:"preset"`
	Quality        string           `json:"quality"`
	RevisionNumber int              `json:"revisionNumber"`
	Size           int64            `json:"size,omitempty"`
	Duration       float64          `json:"duration,omitempty"`
	Width          int              `json:"width,omitempty"`
	Height         int              `json:"height,omitempty"`
	MediaURL       string           `json:"mediaUrl,omitempty" jsonschema:"description=Playable URL (dashboard) once the render is available."`
	QA             *models.RenderQA `json:"qa,omitempty"`
	Link           string           `json:"link"`
}

// QAFindingBrief is one finding, shortened.
type QAFindingBrief struct {
	Severity string `json:"severity"`
	Timecode string `json:"timecode"`
	Message  string `json:"message"`
}

// RelatedQA is a QA report.
type RelatedQA struct {
	ID       string           `json:"id"`
	Verdict  string           `json:"verdict"`
	Errors   int              `json:"errors"`
	Warnings int              `json:"warnings"`
	Info     int              `json:"info"`
	Findings []QAFindingBrief `json:"findings"`
	Link     string           `json:"link"`
}

// JobRelated are the objects a job touched, each with a display name and link.
type JobRelated struct {
	Project  *RelatedRef    `json:"project,omitempty"`
	Asset    *RelatedAsset  `json:"asset,omitempty"`
	Revision *RelatedRef    `json:"revision,omitempty"`
	Render   *RelatedRender `json:"render,omitempty"`
	QAReport *RelatedQA     `json:"qaReport,omitempty"`
}

// JobTarget is where a job's result lives ("Go to …").
type JobTarget struct {
	Label string `json:"label"`
	Link  string `json:"link"`
	Kind  string `json:"kind" jsonschema:"enum=render,enum=clip,enum=version,enum=report,enum=project"`
}

// JobProduced describes what a finished job made, in plain words.
type JobProduced struct {
	Summary  string `json:"summary"`
	Words    int    `json:"words,omitempty"`
	Segments int    `json:"segments,omitempty"`
	Speakers int    `json:"speakers,omitempty"`
	Language string `json:"language,omitempty"`
	Scenes   int    `json:"scenes,omitempty"`
	Pauses   int    `json:"pauses,omitempty"`
}

// JobShowOut is a job with plain-language context: what it is, who started it,
// where it ran, why it waits, what it produced and where that lives.
type JobShowOut struct {
	models.Job
	Title           string           `json:"title"`
	KindLabel       string           `json:"kindLabel"`
	StartedBy       string           `json:"startedBy"`
	Machine         string           `json:"machine,omitempty"`
	DurationSeconds *float64         `json:"durationSeconds,omitempty"`
	Waiting         *jobs.WaitReason `json:"waiting,omitempty"`
	Related         JobRelated       `json:"related"`
	Produced        *JobProduced     `json:"produced,omitempty"`
	Target          *JobTarget       `json:"target,omitempty"`
	Advice          string           `json:"advice,omitempty" jsonschema:"description=What to do about a failure, in plain words."`
	LogLink         string           `json:"logLink" jsonschema:"description=The activity log filtered to this job (See log)."`
	// Questions are open to-dos about the clip a finished ingest, transcription
	// or speaker-detection job worked on ("2 questions about this clip").
	Questions     *models.OpenQuestions `json:"questions,omitempty"`
	CanCancel     bool                  `json:"canCancel"`
	CanRetry      bool                  `json:"canRetry"`
	CanProcessNow bool                  `json:"canProcessNow"`
}

var jobKindLabel = map[string]string{
	jobs.TypeProxy: "Preview copy", jobs.TypeThumbnail: "Thumbnail", jobs.TypeContactSheet: "Frame grid", jobs.TypeWaveform: "Waveform",
	jobs.TypeMezzanine: "Steady copy", jobs.TypeEvidence: "Scene analysis", jobs.TypeTranscribe: "Transcription", jobs.TypeRender: "Render", jobs.TypeQA: "Quality check",
	jobs.TypeVoiceover:       "Narration",
	jobs.TypeDiarize:         "Speaker detection",
	jobs.TypeImport:          "Import from a link",
	jobs.TypeVisual:          "What's on screen",
	jobs.TypeDescribe:        "Scene descriptions",
	jobs.TypeResourceUpload:  "Copying a linked file",
	jobs.TypeFetch:           "Repository or web page",
	jobs.TypeStorageMove:     "Moving media",
	jobs.TypeGenerate:        "Generated video",
	jobs.TypeRemotionImport:  "Bringing in a Remotion project",
	jobs.TypeRemotionRender:  "Making a clip from a Remotion project",
	jobs.TypeInspect:         "Measuring for fixes",
	jobs.TypePrepare:         "Fixed copy",
	jobs.TypeRemotionTask:    "Work on a Remotion project",
	jobs.TypeRemotionRefresh: "Refreshing a Remotion project from its own steps",
	jobs.TypeRemotionPush:    "Sending changes back to a repository",
	jobs.TypeAbilityRun:      "Running an ability",
	jobs.TypeSwitches:        "Finding where the picture changes",
}

// importWords reads what a link import recorded: the video's title, its site,
// and (after a failure) the advice in plain words.
func importWords(j *models.Job) (title, site, advice string) {
	var p struct {
		Title    string `bson:"title"`
		Platform string `bson:"platform"`
		Advice   *struct {
			Message string `bson:"message"`
			Next    string `bson:"next"`
		} `bson:"advice"`
	}
	if b, err := bson.Marshal(j.Payload); err == nil {
		_ = bson.Unmarshal(b, &p)
	}
	site = models.PlatformName(p.Platform)
	if p.Advice != nil {
		advice = strings.TrimSpace(p.Advice.Message + " " + p.Advice.Next)
	}
	return p.Title, site, advice
}

func jobVerb(typ string) string {
	switch typ {
	case jobs.TypeProxy:
		return "Making a preview copy of"
	case jobs.TypeThumbnail:
		return "Making a thumbnail for"
	case jobs.TypeContactSheet:
		return "Making a frame grid for"
	case jobs.TypeWaveform:
		return "Drawing the waveform of"
	case jobs.TypeMezzanine:
		return "Making a steady copy of"
	case jobs.TypeEvidence:
		return "Analyzing scenes and pauses in"
	case jobs.TypeTranscribe:
		return "Transcribing"
	case jobs.TypeDiarize:
		return "Finding who's speaking in"
	case jobs.TypeVisual:
		return "Looking at what's on screen in"
	case jobs.TypeDescribe:
		return "Describing the scenes of"
	case jobs.TypeInspect:
		return "Measuring"
	case jobs.TypePrepare:
		return "Making the fixed copy of"
	case jobs.TypeSwitches:
		return "Finding where the picture changes in"
	}
	return "Working on"
}

func startedBy(a *app.App, actor models.Actor, machines map[string]string) string {
	who := ""
	switch actor.Via {
	case "dashboard":
		who = "You, in the dashboard"
		if actor.UserID != "" && actor.UserID != a.Actor.UserID { // a.Actor: the person looking
			who = orName(actor.UserName, "Someone else") + ", in the dashboard"
		}
	case models.ViaAssistant:
		who = "Claude Code (from the dashboard)"
	case models.ViaAssistantAuto:
		who = "Claude Code, by itself (your setting for safe steps)"
	case "cli":
		who = "Claude Code or the command line"
	case "mcp":
		who = "An app connected to MetaRush (Claude Desktop or Claude Code)"
	case "watch":
		who = "The watched folder"
	case "worker":
		who = "MetaRush, automatically"
	default:
		who = orName(actor.UserName, "Someone")
	}
	if actor.InstallationID != "" && actor.InstallationID != a.Cfg.Installation.ID {
		who += ", on " + orName(machines[actor.InstallationID], "another machine")
	}
	return who
}

func orName(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

// narrationAdvice says what to do when narration work failed, from its stable error code.
func narrationAdvice(j *models.Job) string {
	code, _ := j.Payload["errorCode"].(string)
	e := strings.ToLower(j.Error)
	switch {
	case j.State == models.JobCanceled:
		return "It was stopped. Start it again when you are ready: lines that were already made are reused at no cost."
	case strings.Contains(e, "stopped while doing this"):
		return "Start it again from the same computer: lines that were already made are reused at no cost."
	case code == "E_NOT_CONFIGURED" && strings.Contains(e, "voice is chosen"):
		return "Choose a narration voice in Settings, Integrations, then try again."
	case code == "E_NOT_CONFIGURED":
		return "Add your ElevenLabs key on this computer in Settings, Integrations, then try again. Each computer needs its own key."
	case code == "E_BUDGET":
		return "This project reached its narration spending limit. Raise the limit in Settings, Integrations, shorten the script, or use the free scratch voice."
	case code == "E_PROVIDER":
		return "ElevenLabs could not do it right now. Check the key and your plan in Settings, Integrations, then try again."
	case code == "E_TOOL_MISSING":
		return "This computer has no free system voice. On a Mac it is built in; on Linux install espeak-ng, then try again."
	case code == "E_CONSENT_REQUIRED":
		return "A copy of someone else's voice needs that person's recorded consent. Use your own voice, or add their recording first."
	}
	return jobTryAgain
}

// jobTryAgain is what to do about a failed job when nothing more specific is known.
const jobTryAgain = "Try again. If it keeps failing, open Details and share what it says with whoever helps you with MetaRush."

func adviceFor(j *models.Job) string {
	if j.Type == jobs.TypeVoiceover {
		return narrationAdvice(j)
	}
	if j.Type == jobs.TypeStorageMove {
		return "Start the move again on that computer: it continues where it stopped. The old storage is unchanged."
	}
	if j.Type == jobs.TypeGenerate {
		// The generation job wrote what happened and what to do, in plain words.
		if adv, _ := j.Payload["advice"].(string); adv != "" {
			return adv
		}
		if k, _ := j.Payload["kind"].(string); k != "" && k != models.GenKindVideo {
			if j.State == models.JobCanceled {
				return "It was stopped. Ask for it again when you want it; nothing more is spent."
			}
			return "Ask for it again in the Generate panel. If it keeps failing, change the words or the files."
		}
		if j.State == models.JobCanceled {
			return "It was stopped. Generate it again when you want it; nothing more is spent."
		}
		return "Generate it again from the clip's menu or Generate a shot. If it keeps failing, change the prompt."
	}
	if j.Type == jobs.TypeImport {
		if _, _, adv := importWords(j); adv != "" {
			return adv
		}
		if j.State == models.JobCanceled {
			return "It was stopped. Choose Retry to import it again."
		}
	}
	e := strings.ToLower(j.Error)
	switch {
	case strings.Contains(e, "no audio") && j.Type == jobs.TypeDiarize:
		return "This clip has no sound, so nobody speaks in it. Nothing needs fixing."
	case strings.Contains(e, "no audio"):
		return "This clip has no sound, so there is nothing to transcribe. Nothing needs fixing."
	case j.Type == jobs.TypeDiarize && strings.Contains(e, "speaker detection"):
		return "That machine couldn't tell the voices apart: the part of MetaRush that does it is missing or broken there. Run the setup check from Settings on that machine, then try again."
	case strings.Contains(e, "model") && (strings.Contains(e, "not found") || strings.Contains(e, "missing") || strings.Contains(e, "no such file")):
		return "The transcriber on that machine is missing a file it needs. Run the setup check from Settings on that machine, then try again."
	case strings.Contains(e, "not installed") || strings.Contains(e, "not on path") || strings.Contains(e, "executable file not found"):
		return "A program this needs isn't installed on that machine. Run the setup check from Settings there to see what to install, then try again."
	case j.Type == jobs.TypeRender && strings.HasPrefix(e, "render ") && strings.Contains(e, "not found"):
		// The worker waited for the render a few times first (renderRecord).
		return "MetaRush couldn't find this render, even after waiting for it, so trying again won't help. Render the version again."
	case strings.Contains(e, "not found in this workspace") || strings.Contains(e, "not found"):
		return "Something it needs was removed, so trying again probably won't help."
	case strings.Contains(e, "canonical store") || strings.Contains(e, "storage") || strings.Contains(e, "upload"):
		return "Saving the result to MetaRush's storage failed, often because of a brief network problem. Try again; if it keeps failing, run the setup check from Settings."
	case strings.Contains(e, "gave up") || strings.Contains(e, "stopped responding"):
		return "The machine running it stopped responding (closed, asleep or offline). Try again to run it once more."
	case j.State == models.JobCanceled:
		return "It was stopped. Try again to run it once more."
	}
	return jobTryAgain
}

// JobDetail builds the job details the dashboard dialog and `metarush job show` read.
func JobDetail(ctx context.Context, a *app.App, id string) (*JobShowOut, error) {
	j, err := a.Queue.Get(ctx, id)
	if err != nil {
		return nil, notFound("job", id)
	}
	out := &JobShowOut{Job: *j, KindLabel: orName(jobKindLabel[j.Type], j.Type), LogLink: links.Log("job", j.ID)}
	if k, _ := j.Payload["kind"].(string); j.Type == jobs.TypeGenerate && k != "" && k != models.GenKindVideo {
		out.KindLabel = "AI work" // pictures, speech, music… (D226)
	}
	var workers []models.WorkerDevice
	_ = a.DB.Find(ctx, store.CWorkers, a.WorkspaceID(), nil, &workers)
	machines := map[string]string{}
	for _, w := range workers {
		machines[w.ID] = w.Name
	}
	var insts []models.Installation
	_ = a.DB.Find(ctx, store.CInstallations, a.WorkspaceID(), nil, &insts, options.Find().SetProjection(bson.M{"name": 1}))
	for _, in := range insts {
		if machines[in.ID] == "" {
			machines[in.ID] = in.Name
		}
	}
	out.StartedBy = startedBy(a, j.CreatedBy, machines)
	if j.Lease != nil {
		out.Machine = j.Lease.OwnerName
	} else if n := len(j.Attempts); n > 0 {
		out.Machine = orName(j.Attempts[n-1].WorkerName, machines[j.Attempts[n-1].Worker])
	}
	if n := len(j.Attempts); n > 0 {
		start := j.Attempts[n-1].StartedAt
		end := time.Now()
		if j.FinishedAt != nil {
			end = *j.FinishedAt
		}
		if j.State != models.JobQueued {
			d := end.Sub(start).Seconds()
			out.DurationSeconds = &d
		}
	}
	switch j.State {
	case models.JobQueued:
		out.Waiting = jobs.Why(*j, workers, time.Now().UTC())
		out.CanProcessNow = out.Waiting != nil && out.Waiting.CanProcessNow
		out.CanCancel = true
	case models.JobRunning:
		out.CanCancel = true
	case models.JobFailed, models.JobCanceled:
		// Narration is started again where it was asked for (that computer's key and files).
		// Generated video is asked for again (a new take, priced again), never retried as is.
		out.CanRetry = j.Type != jobs.TypeVoiceover && j.Type != jobs.TypeStorageMove && j.Type != jobs.TypeGenerate
		out.Advice = adviceFor(j)
	}
	// Project.
	projectID := j.ProjectID
	var asset *models.Asset
	if j.AssetID != "" {
		var as models.Asset
		if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), j.AssetID, &as) == nil {
			asset = &as
			if projectID == "" && len(as.ProjectIDs) > 0 {
				projectID = as.ProjectIDs[0]
			}
			out.Related.Asset = &RelatedAsset{ID: as.ID, Title: as.DisplayTitle(), Name: as.Name, Kind: as.Kind, Duration: as.Media.Duration,
				HasThumb: as.Variants[models.VariantThumbnail].StorageKey != "", Link: links.Asset(firstOr(as.ProjectIDs), as.ID)}
			if len(as.ProjectIDs) > 0 {
				out.Related.Asset.ProjectID = as.ProjectIDs[0]
			}
		}
	}
	var projectName string
	if projectID != "" {
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), projectID, &p) == nil {
			projectName = p.Name
			out.Related.Project = &RelatedRef{ID: p.ID, Name: p.Name, Link: links.Project(p.ID)}
		}
	}
	// Render and revision (render jobs carry the render; QA jobs carry it in the payload).
	renderID := j.RenderID
	if renderID == "" {
		renderID, _ = j.Payload["renderId"].(string)
	}
	var rd *models.Render
	if renderID != "" {
		var r models.Render
		if a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), renderID, &r) == nil {
			rd = &r
			rr := &RelatedRender{ID: r.ID, State: r.State, Preset: r.Preset, Quality: r.Quality, RevisionNumber: r.RevisionNumber, QA: r.QA, Link: links.Render(r.ProjectID, r.ID)}
			if r.Output != nil {
				rr.Size, rr.Duration, rr.Width, rr.Height = r.Output.Size, r.Output.Duration, r.Output.Width, r.Output.Height
				if r.State == models.RenderAvailable {
					rr.MediaURL = "/media/renders/" + r.ID
				}
			}
			out.Related.Render = rr
			out.Related.Revision = &RelatedRef{ID: r.RevisionID, Name: fmt.Sprintf("Version %d", r.RevisionNumber), Link: links.Revision(r.ProjectID, r.RevisionID)}
		}
	}
	// Narration: the version it was made for.
	var narrationRev string
	if j.Type == jobs.TypeVoiceover {
		narrationRev, _ = j.Payload["revisionId"].(string)
		if narrationRev != "" {
			out.Related.Revision = &RelatedRef{ID: narrationRev, Name: fmt.Sprintf("Version %d", toInt(j.Payload["revisionNumber"])), Link: links.Revision(projectID, narrationRev)}
		}
	}
	// QA report.
	if j.Type == jobs.TypeQA {
		repID, _ := j.Result["reportId"].(string)
		var rep models.QAReport
		found := false
		if repID != "" {
			found = a.DB.GetByID(ctx, store.CQAReports, a.WorkspaceID(), repID, &rep) == nil
		}
		if found {
			q := &RelatedQA{ID: rep.ID, Verdict: rep.Verdict, Errors: rep.Counts.Errors, Warnings: rep.Counts.Warnings, Info: rep.Counts.Info, Findings: []QAFindingBrief{},
				Link: links.QA(rep.ProjectID, rep.RenderID, rep.ID)}
			for _, f := range rep.Findings {
				if len(q.Findings) >= 6 {
					break
				}
				q.Findings = append(q.Findings, QAFindingBrief{Severity: f.Severity, Timecode: f.Timecode, Message: f.Message})
			}
			out.Related.QAReport = q
		}
	}
	// Title.
	switch {
	case j.Type == jobs.TypeRender && rd != nil:
		quality := map[string]string{"review": "Review", "final": "Final"}[rd.Quality]
		out.Title = fmt.Sprintf("%s render of %s, version %d", orName(quality, "Render"), orName(projectName, "a project"), rd.RevisionNumber)
	case j.Type == jobs.TypeQA && rd != nil:
		out.Title = fmt.Sprintf("Quality check of %s, version %d (%s render)", orName(projectName, "a project"), rd.RevisionNumber, rd.Quality)
	case j.Type == jobs.TypeVoiceover:
		t, _ := j.Payload["title"].(string)
		out.Title = orName(t, "Narration")
		if n := toInt(j.Payload["revisionNumber"]); n > 0 {
			out.Title = fmt.Sprintf("%s for %s, version %d", out.Title, orName(projectName, "a project"), n)
		} else if projectName != "" {
			out.Title += " for " + projectName
		}
	case j.Type == jobs.TypeImport:
		t, site, _ := importWords(j)
		if t != "" {
			out.Title = fmt.Sprintf("Importing “%s” from %s", t, site)
		} else {
			out.Title = "Importing a video from " + site
		}
		if j.State == models.JobSucceeded {
			if asset != nil {
				out.Produced = &JobProduced{Summary: fmt.Sprintf("Added “%s” to MetaRush.", asset.DisplayTitle())}
			} else if n := toInt(j.Result["queued"]); n > 0 {
				out.Produced = &JobProduced{Summary: fmt.Sprintf("Found %d videos; each one is imported on its own.", n)}
			}
		}
	case j.Type == jobs.TypeGenerate:
		what, _ := j.Payload["what"].(string)
		out.Title = "Making " + orName(what, "a generated video")
		if projectName != "" {
			out.Title += " for " + projectName
		}
		if id, _ := j.Result["assetId"].(string); id != "" && j.State == models.JobSucceeded {
			var as models.Asset
			if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &as) == nil {
				out.Produced = &JobProduced{Summary: fmt.Sprintf("Added “%s” to Sources, marked as AI-generated.", as.DisplayTitle())}
				out.Target = &JobTarget{Label: "Open the clip", Link: links.Asset(firstOr(as.ProjectIDs), as.ID), Kind: "clip"}
			}
		}
		// Other kinds of AI work (D226): a picture in the resources library.
		if id, _ := j.Result["resourceId"].(string); id != "" && j.State == models.JobSucceeded {
			var r models.Resource
			if a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), id, &r) == nil {
				out.Produced = &JobProduced{Summary: fmt.Sprintf("Added the picture “%s” to the resources library, marked as AI-generated.", firstNonEmpty(r.Title, r.Name))}
				out.Target = &JobTarget{Label: "Open the picture", Link: links.Resource(r.ProjectID, r.ID), Kind: "resource"}
			}
		}
	case j.Type == jobs.TypeStorageMove:
		to, _ := j.Payload["storage"].(string)
		out.Title = "Moving media to " + orName(to, "new storage")
		if s, _ := j.Result["summary"].(string); s != "" && j.State == models.JobSucceeded {
			out.Produced = &JobProduced{Summary: s}
		}
	case asset != nil:
		out.Title = fmt.Sprintf("%s “%s”", jobVerb(j.Type), asset.DisplayTitle())
	default:
		out.Title = out.KindLabel
	}
	// What it produced.
	if j.State == models.JobSucceeded {
		switch j.Type {
		case jobs.TypeTranscribe:
			if asset != nil && asset.Transcript != nil {
				t := asset.Transcript
				p := &JobProduced{Words: t.Words, Segments: t.Segments, Speakers: t.Speakers, Language: t.Language}
				switch {
				case t.Words == 0 && t.Segments == 0:
					p.Summary = "No speech was found in this clip."
				case t.Speakers > 1:
					p.Summary = fmt.Sprintf("A transcript of %d words with %d speakers.", t.Words, t.Speakers)
				default:
					p.Summary = fmt.Sprintf("A transcript of %d words.", t.Words)
				}
				out.Produced = p
			}
		case jobs.TypeDiarize:
			// Who speaks now (names given since included), in the words the Speakers tab uses.
			p := &JobProduced{Speakers: toInt(j.Result["voices"])}
			if asset != nil {
				if vs, err := speakers.Voices(ctx, a, asset.ID, false); err == nil {
					p.Summary = speakers.Summary(asset.DisplayTitle(), vs)
				}
			}
			if p.Summary == "" {
				p.Summary = fmt.Sprintf("Found %d voices.", p.Speakers)
				if p.Speakers == 1 {
					p.Summary = "Found 1 voice."
				}
			}
			out.Produced = p
		case jobs.TypeEvidence:
			p := &JobProduced{Scenes: toInt(j.Result["scenes"]), Pauses: toInt(j.Result["silences"])}
			p.Summary = fmt.Sprintf("Found %d scene changes and %d pauses.", p.Scenes, p.Pauses)
			if c := toInt(j.Result["visualChanges"]); c > 0 {
				p.Summary = fmt.Sprintf("Found %s, %d pauses.", count(c, "visual change (cuts, slow changes, movement)", "visual changes (cuts, slow changes, movement)"), p.Pauses)
			}
			out.Produced = p
		case jobs.TypeVisual:
			s, _ := j.Result["summary"].(string)
			if s == "" {
				s = "Looked at what's on screen"
			}
			out.Produced = &JobProduced{Summary: capFirst(s) + "."}
		case jobs.TypeDescribe:
			out.Produced = &JobProduced{Summary: fmt.Sprintf("Claude Code described %s.", sceneWords(toInt(j.Result["scenes"])))}
		case jobs.TypeRender:
			if rd != nil && rd.Output != nil {
				out.Produced = &JobProduced{Summary: fmt.Sprintf("A %dx%d video, %.1f seconds long.", rd.Output.Width, rd.Output.Height, rd.Output.Duration)}
			}
		case jobs.TypeQA:
			if q := out.Related.QAReport; q != nil {
				// A problem must be fixed; a thing worth checking may be fine.
				s := "No problems found."
				switch {
				case q.Errors > 0 && q.Warnings > 0:
					s = fmt.Sprintf("%s to fix and %s worth checking.", count(q.Errors, "problem", "problems"), count(q.Warnings, "thing", "things"))
				case q.Errors > 0:
					s = fmt.Sprintf("%s to fix.", count(q.Errors, "problem", "problems"))
				case q.Warnings > 0:
					s = fmt.Sprintf("%s worth checking.", count(q.Warnings, "thing", "things"))
				}
				if heard := speechHeard(ctx, a, q.ID); heard != "" {
					s += " " + heard
				}
				out.Produced = &JobProduced{Summary: s}
			}
		case jobs.TypeVoiceover:
			if s, _ := j.Result["summary"].(string); s != "" {
				out.Produced = &JobProduced{Summary: s}
			}
		case jobs.TypeImport, jobs.TypeStorageMove: // set with the title above
		default:
			if k := jobKindLabel[j.Type]; k != "" {
				out.Produced = &JobProduced{Summary: k + " ready."}
			}
		}
	}
	if j.State == models.JobSucceeded && asset != nil && asksAboutClip[j.Type] {
		out.Questions = todos.ForClip(ctx, a, asset.ID)
	}
	out.Target = jobTarget(out)
	return out, nil
}

// speechHeard says, in plain words, how the render's speech compared with the
// words the edit expects (the speech continuity check), or "" when it did not run.
func speechHeard(ctx context.Context, a *app.App, reportID string) string {
	var rep models.QAReport
	if a.DB.GetByID(ctx, store.CQAReports, a.WorkspaceID(), reportID, &rep) != nil {
		return ""
	}
	m := rep.Measurements
	if m.SpeechWordsExpected == 0 {
		return ""
	}
	if m.SpeechWordsMatched >= m.SpeechWordsExpected {
		return fmt.Sprintf("Every one of the %d expected words was heard in the finished video.", m.SpeechWordsExpected)
	}
	return fmt.Sprintf("%d of the %d expected words were heard in the finished video.", m.SpeechWordsMatched, m.SpeechWordsExpected)
}

// asksAboutClip are the job types whose results can raise questions about a
// clip: the ingest steps (transcription finds voices to name) and speaker detection.
var asksAboutClip = map[string]bool{jobs.TypeThumbnail: true, jobs.TypeProxy: true, jobs.TypeContactSheet: true, jobs.TypeWaveform: true,
	jobs.TypeMezzanine: true, jobs.TypeEvidence: true, jobs.TypeTranscribe: true, jobs.TypeDiarize: true, jobs.TypeVisual: true}

// jobTarget is the "Go to …" for a job: the render in its project's Renders tab,
// the report on its render, the clip in Sources (on its Speakers tab for speaker
// detection), the version in History.
func jobTarget(o *JobShowOut) *JobTarget {
	r := o.Related
	switch {
	case o.Type == jobs.TypeQA && r.QAReport != nil:
		return &JobTarget{Label: "Go to the quality check", Link: r.QAReport.Link, Kind: "report"}
	case r.Render != nil:
		return &JobTarget{Label: "Go to the render", Link: r.Render.Link, Kind: "render"}
	case o.Type == jobs.TypeDiarize && r.Asset != nil:
		return &JobTarget{Label: "Go to the speakers", Link: links.AssetSpeakers(r.Asset.ProjectID, r.Asset.ID), Kind: "clip"}
	case (o.Type == jobs.TypeVisual || o.Type == jobs.TypeDescribe) && r.Asset != nil:
		return &JobTarget{Label: "Go to what's on screen", Link: links.AssetOnScreen(r.Asset.ProjectID, r.Asset.ID), Kind: "clip"}
	case r.Asset != nil:
		return &JobTarget{Label: "Go to the clip", Link: r.Asset.Link, Kind: "clip"}
	case r.Revision != nil:
		return &JobTarget{Label: "Go to the version", Link: r.Revision.Link, Kind: "version"}
	case r.Project != nil:
		return &JobTarget{Label: "Go to the project", Link: r.Project.Link, Kind: "project"}
	}
	return nil
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}
