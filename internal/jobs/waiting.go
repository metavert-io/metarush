package jobs

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/models"
)

// OnlineWindow is how recently a worker must have heartbeated to count as online.
const OnlineWindow = 45 * time.Second

// Wait reason codes (stable; the dashboard branches on them).
const (
	WaitBattery       = "battery"        // capable workers are deferring heavy jobs for battery
	WaitHeavyDisabled = "heavy_disabled" // capable workers do not take heavy jobs at all
	WaitNoWorker      = "no_worker"      // no online worker can run this job type
	WaitBusy          = "busy"           // capable workers are busy with other jobs
	WaitRetry         = "retry_backoff"  // waiting to retry after an error
	WaitStarting      = "starting"       // a worker will pick it up momentarily
	// WaitComponent: the computers that could run it lack a component a
	// person adds in Settings → Components (D326).
	WaitComponent = "needs_component"
)

// WaitReason explains why a queued job is not running yet.
type WaitReason struct {
	Code           string `json:"code" jsonschema:"enum=battery,enum=heavy_disabled,enum=no_worker,enum=busy,enum=retry_backoff,enum=starting,enum=needs_component"`
	Text           string `json:"text"`
	Machine        string `json:"machine,omitempty"`
	BatteryPercent *int   `json:"batteryPercent,omitempty"`
	Threshold      int    `json:"threshold,omitempty"`
	RetryAt        string `json:"retryAt,omitempty"`
	// Component is what to add for it to run (needs_component).
	Component string `json:"component,omitempty"`
	// CanProcessNow: "Process now" (job.expedite) would let it start.
	CanProcessNow bool `json:"canProcessNow,omitempty"`
}

// Online reports whether a worker heartbeated recently.
func Online(w models.WorkerDevice, now time.Time) bool {
	return now.Sub(w.LastHeartbeat) < OnlineWindow
}

func handles(w models.WorkerDevice, typ string) bool {
	for _, t := range w.JobTypes {
		if t == typ {
			return true
		}
	}
	return false
}

// Why explains why a queued job is waiting, from the online workers' published
// state. It returns nil for jobs that are not queued. Text is a plain sentence
// for people ("No machine running MetaRush can make transcripts right now. It
// starts as soon as one is."); Code is what programs branch on.
func Why(j models.Job, workers []models.WorkerDevice, now time.Time) *WaitReason {
	if j.State != models.JobQueued || j.CancelRequested {
		return nil
	}
	if j.NextRunAt.After(now) {
		return &WaitReason{Code: WaitRetry, Text: "It ran into a problem and tries again " + PlainIn(j.NextRunAt.Sub(now)) + ".",
			RetryAt: j.NextRunAt.UTC().Format(time.RFC3339), CanProcessNow: true}
	}
	var capable []models.WorkerDevice
	for _, w := range workers {
		if Online(w, now) && handles(w, j.Type) && (j.Installation == "" || w.ID == j.Installation) {
			capable = append(capable, w)
		}
	}
	if len(capable) == 0 {
		// A computer that would run it once a component is added says so (D326).
		for _, w := range workers {
			if !Online(w, now) || (j.Installation != "" && w.ID != j.Installation) {
				continue
			}
			for _, l := range w.Lacks {
				if l.JobType == j.Type {
					return &WaitReason{Code: WaitComponent, Machine: w.Name, Component: l.Component, Text: l.Says}
				}
			}
		}
	}
	if len(capable) == 0 && j.Installation != "" {
		// A pinned job (a link import) waits for the machine that asked for it.
		machine := ""
		for _, w := range workers {
			if w.ID == j.Installation {
				machine = w.Name
			}
		}
		if machine == "" {
			return &WaitReason{Code: WaitNoWorker, Text: PinnedWaitText + " the computer that asked for it."}
		}
		return &WaitReason{Code: WaitNoWorker, Machine: machine, Text: PinnedWaitText + " " + machine + "."}
	}
	if len(capable) == 0 {
		return &WaitReason{Code: WaitNoWorker, Text: "No machine running MetaRush can " + doing(j.Type) + " right now. It starts as soon as one is."}
	}
	if j.Heavy && !j.AllowOnBattery {
		var takers, deferring, disabled []models.WorkerDevice
		for _, w := range capable {
			switch {
			case !w.HeavyEnabled:
				disabled = append(disabled, w)
			case w.DeferringHeavy:
				deferring = append(deferring, w)
			default:
				takers = append(takers, w)
			}
		}
		if len(takers) == 0 && len(deferring) > 0 {
			w := deferring[0]
			r := &WaitReason{Code: WaitBattery, Machine: w.Name, BatteryPercent: w.BatteryPercent, Threshold: w.HeavyBatteryThreshold, CanProcessNow: true}
			if w.BatteryPercent != nil {
				r.Text = fmt.Sprintf("Paused: battery at %d%% on %s.", *w.BatteryPercent, w.Name)
			} else {
				r.Text = "Paused: " + w.Name + " is on battery."
			}
			return r
		}
		if len(takers) == 0 {
			return &WaitReason{Code: WaitHeavyDisabled, Machine: disabled[0].Name, Text: "Big jobs like transcripts and final renders are turned off on " + names(disabled) + "."}
		}
		capable = takers
	}
	free := false
	for _, w := range capable {
		if w.Running < max(1, w.Concurrency) {
			free = true
		}
	}
	if !free {
		doingNow := "it's"
		if len(capable) > 1 {
			doingNow = "they're"
		}
		return &WaitReason{Code: WaitBusy, Machine: capable[0].Name, Text: "Waiting for " + names(capable) + " to finish what " + doingNow + " doing."}
	}
	return &WaitReason{Code: WaitStarting, Machine: capable[0].Name, Text: "Starting on " + capable[0].Name + "."}
}

// PinnedWaitText starts the text of a job that waits for the one machine that
// may run it ("Waiting for MetaRush to run on studio-mac.").
const PinnedWaitText = "Waiting for MetaRush to run on"

// doing is what a job type does, for "No machine running MetaRush can … right now".
func doing(typ string) string {
	switch typ {
	case TypeProxy:
		return "make preview copies"
	case TypeThumbnail:
		return "make thumbnails"
	case TypeContactSheet:
		return "make frame grids"
	case TypeWaveform:
		return "draw waveforms"
	case TypeMezzanine:
		return "make steady copies"
	case TypeEvidence:
		return "find the scenes and pauses"
	case TypeTranscribe:
		return "make transcripts"
	case TypeRender:
		return "render videos"
	case TypeQA:
		return "run quality checks"
	case TypeDiarize:
		return "tell voices apart"
	case TypeVoiceover:
		return "make narration"
	case TypeImport:
		return "import videos from links"
	case TypeResourceUpload:
		return "copy files from linked folders"
	case TypeVisual:
		return "look at what's on screen"
	case TypeDescribe:
		return "describe scenes"
	case TypeFetch:
		return "get repositories and web pages"
	case TypeStorageMove:
		return "move the media"
	case TypeGenerate:
		return "make things with AI services"
	case TypeRemotionImport:
		return "bring in Remotion projects"
	case TypeRemotionRender:
		return "make clips from Remotion projects"
	case TypeInspect:
		return "measure clips for their fixes"
	case TypePrepare:
		return "make fixed copies"
	case TypeRemotionTask:
		return "work on Remotion projects"
	case TypeRemotionRefresh:
		return "refresh Remotion projects from their own steps"
	case TypeRemotionPush:
		return "send changes back to a Remotion project's repository"
	case TypeAbilityRun:
		return "run abilities"
	case TypeSwitches:
		return "find where the picture changes"
	}
	return "do this"
}

func names(ws []models.WorkerDevice) string {
	var out []string
	for _, w := range ws {
		out = append(out, w.Name)
	}
	sort.Strings(out)
	if len(out) > 2 {
		return out[0] + ", " + out[1] + " and others"
	}
	return strings.Join(out, " and ")
}

// Label is a person-facing name for a job type.
func Label(typ string) string { return label(typ) }

func label(typ string) string {
	switch typ {
	case TypeThumbnail:
		return "thumbnail"
	case TypeContactSheet:
		return "frame grid"
	case TypeWaveform:
		return "waveform"
	case TypeTranscribe:
		return "transcription"
	case TypeRender:
		return "render"
	case TypeQA:
		return "quality check"
	case TypeProxy:
		return "preview copy"
	case TypeMezzanine:
		return "steady copy"
	case TypeEvidence:
		return "scene analysis"
	case TypeDiarize:
		return "speaker detection"
	case TypeVoiceover:
		return "narration"
	case TypeImport:
		return "link import"
	case TypeResourceUpload:
		return "copy of a linked file"
	case TypeVisual:
		return "look at what's on screen"
	case TypeDescribe:
		return "scene descriptions"
	case TypeFetch:
		return "repository or web page"
	case TypeStorageMove:
		return "media move"
	case TypeGenerate:
		return "piece of AI work"
	case TypeRemotionImport:
		return "Remotion project"
	case TypeRemotionRender:
		return "clip from a Remotion project"
	case TypeInspect:
		return "measurements for fixes"
	case TypePrepare:
		return "fixed copy"
	case TypeRemotionTask:
		return "work on a Remotion project"
	case TypeRemotionRefresh:
		return "refresh of a Remotion project"
	case TypeRemotionPush:
		return "changes sent back to a repository"
	case TypeAbilityRun:
		return "run of an ability"
	case TypeSwitches:
		return "picture changes"
	}
	return typ
}

// PlainIn says when something happens, for people: "in a few seconds",
// "in 40 seconds", "in about a minute", "in about 3 minutes", "in about 6 hours".
func PlainIn(d time.Duration) string {
	switch {
	case d < 10*time.Second:
		return "in a few seconds"
	case d < 55*time.Second:
		return fmt.Sprintf("in %d seconds", int(d.Seconds()+0.5))
	case d < 90*time.Second:
		return "in about a minute"
	case d < 55*time.Minute:
		return fmt.Sprintf("in about %d minutes", int(d.Minutes()+0.5))
	case d < 90*time.Minute:
		return "in about an hour"
	}
	return fmt.Sprintf("in about %d hours", int(d.Hours()+0.5))
}
