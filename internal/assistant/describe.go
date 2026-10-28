package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Written scene descriptions (D74). `footage describe` has this machine's
// Claude Code — the dashboard assistant's runner, with its read-only
// allowlist — look at a clip's smart contact sheet, frames and on-screen
// readout and write a sentence or two per scene into its proposals folder;
// MetaRush checks the file and keeps the descriptions as the clip's
// "description" insight (searchable). It is off unless turned on for the
// project or workspace, and it uses the person's own Claude Code usage.

// DescribeSuffix names the descriptions file Claude writes.
const DescribeSuffix = ".describe.json"

// DescribeFile is the file a describe conversation writes.
const DescribeFile = "scenes" + DescribeSuffix

func describePrompt(as *models.Asset) string {
	return fmt.Sprintf(`Describe the scenes of the clip “%s” (asset %s, %.1f seconds) for search, in plain words.

1. Look at it: run `+"`metarush footage sheet %s --json`"+` and Read the image path it prints (a smart contact sheet: one frame per shot, visual changes and new speakers, each labeled with its time and what is being said). Use `+"`metarush footage visuals %s --json`"+` (what Apple Vision saw: shot types, faces, on-screen text) and, where you need more detail, `+"`metarush footage frames %s --at <seconds,...> --json`"+` (Read those images too).
2. Write the file %s in this message's proposals folder: a JSON array of {"from": <seconds>, "to": <seconds>, "text": "<one or two short sentences>"}, one entry per scene (a new shot or a clear change in what is seen), in time order, covering the clip, at most %d entries.
3. Describe only what you can see: who or what is on screen (no names unless the clip shows them or the visuals readout names that face), the place, what happens, any on-screen text. No guesses about feelings or intent, no editing advice.
4. Reply with one short line saying how many scenes you described.`,
		clipName(as), as.ID, as.Media.Duration, as.ID, as.ID, as.ID, DescribeFile, ops.MaxScenes)
}

// Describe runs one describe conversation for a clip and waits for it.
func (m *Manager) Describe(ctx context.Context, as *models.Asset, projectID string) (*ops.DescribeRun, error) {
	// What may leave (D290): describing a clip shows Claude its frames and
	// words, so a client that keeps them here gets no descriptions.
	whose := append([]string{projectID}, as.ProjectIDs...)
	if len(as.ProjectIDs) == 0 {
		whose = append(whose, "")
	}
	if err := leave.Check(ctx, m.App, leave.Send{Place: models.PlaceClaude, To: m.terms(), ToWords: "Claude", What: "frames and words of “" + clipName(as) + "” for scene descriptions",
		Kinds: []string{models.LeaveFaces, models.LeaveWords, models.LeavePictures}, Projects: whose}); err != nil {
		return nil, err
	}
	title := "Scene descriptions: “" + clipName(as) + "”"
	if r := []rune(title); len(r) > 120 {
		title = string(r[:119]) + "…"
	}
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: describePrompt(as), ProjectID: projectID, AssetID: as.ID}, Title: title})
	if err != nil {
		return nil, err
	}
	id, n := out.Conversation.ID, out.Turn
	var turn Turn
	for {
		d, err := m.Get(ctx, id, false, false)
		if err != nil {
			return nil, err
		}
		if n >= 1 && n <= len(d.Turns) {
			turn = d.Turns[n-1]
		}
		if TurnTerminal(turn.State) {
			break
		}
		select {
		case <-ctx.Done():
			_, _ = m.Stop(context.WithoutCancel(ctx), id)
			return nil, ctx.Err()
		case <-time.After(400 * time.Millisecond):
		}
	}
	if turn.State != TurnSucceeded {
		msg := strings.TrimSpace(turn.Error)
		if msg == "" {
			msg = "Claude Code stopped before it finished describing the scenes."
		}
		return nil, apperr.New(apperr.ToolFailed, "%s", msg).WithDetails(map[string]any{"state": turn.State})
	}
	b, err := readProposal(filepath.Join(m.dir(), filepath.FromSlash(turn.Folder)), DescribeFile)
	if err != nil {
		return nil, apperr.New(apperr.ToolFailed, "Claude Code didn't write the scene descriptions. Try describing the clip again.").
			WithDetails(map[string]any{"error": firstLine(err.Error())})
	}
	var scenes []ops.SceneDescription
	if err := json.Unmarshal(b, &scenes); err != nil {
		return nil, apperr.New(apperr.ToolFailed, "MetaRush couldn't read the scene descriptions Claude Code wrote. Try describing the clip again.").
			WithDetails(map[string]any{"error": firstLine(err.Error())})
	}
	d, _ := m.Get(ctx, id, false, false)
	model := ""
	if d != nil {
		model = d.ModelUsed
	}
	// Done: out of the way in the Claude Code panel (still there, archived).
	_, _ = m.Archive(ctx, ConversationArchiveIn{ID: id})
	return &ops.DescribeRun{ConversationID: id, Scenes: scenes, Model: model}, nil
}

// DescribeIn has Claude Code describe a clip's scenes, or saves descriptions
// Claude wrote in a session.
type DescribeIn struct {
	AssetID   string `json:"assetId,omitempty"`
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=With all: every clip of the project that has no descriptions yet."`
	All       bool   `json:"all,omitempty"`
	// Scenes saves these descriptions (written by Claude in a session) instead of asking Claude Code to write them.
	Scenes []ops.SceneDescription `json:"scenes,omitempty" jsonschema:"maxItems=80"`
	Force  bool                   `json:"force,omitempty" jsonschema:"description=Describe again even when the clip has descriptions."`
}

// DescribeOut says what was queued or saved.
type DescribeOut struct {
	Jobs    []string `json:"jobs"`
	Skipped []string `json:"skipped"`
	Saved   int      `json:"saved,omitempty" jsonschema:"description=Scenes saved (when scenes were given)."`
	Summary string   `json:"summary"`
	Note    string   `json:"note"`
}

// DescribeSettingsIn reads the setting (for a project, or the workspace).
type DescribeSettingsIn struct {
	ProjectID string `json:"projectId,omitempty"`
}

// DescribeConfigureIn turns written descriptions on or off.
type DescribeConfigureIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=Change this project only (default: the workspace)."`
	On        *bool  `json:"on,omitempty"`
	// Inherit makes the project follow the workspace's choice again.
	Inherit bool `json:"inherit,omitempty"`
}

// DescribeSettingsOut is the setting and whether Claude Code can do it here.
type DescribeSettingsOut struct {
	Workspace bool  `json:"workspace" jsonschema:"description=The workspace's choice (off by default)."`
	Project   *bool `json:"project,omitempty" jsonschema:"description=The project's own choice (absent: it follows the workspace)."`
	On        bool  `json:"on" jsonschema:"description=Whether new clips get descriptions here."`
	// ClaudeReady: Claude Code is installed and signed in on this machine.
	ClaudeReady bool   `json:"claudeReady"`
	ClaudeText  string `json:"claudeText"`
	Summary     string `json:"summary"`
	Note        string `json:"note"`
}

const usageNote = "Descriptions are written by your own Claude Code, on this machine, and use your Claude Code usage (like asking Claude in the dashboard). Off unless you turn it on."

func describeSettings(ctx context.Context, a *app.App, projectID string) *DescribeSettingsOut {
	on, proj, ws := ops.DescribeOn(ctx, a, projectID)
	st := For(a).Status(ctx, false)
	out := &DescribeSettingsOut{Workspace: ws, Project: proj, On: on, ClaudeReady: st.Available, ClaudeText: st.Reason, Note: usageNote}
	if st.Available {
		out.ClaudeText = "Claude Code is ready on this machine."
	}
	where := "this workspace"
	if projectID != "" && proj != nil {
		where = "this project"
	}
	if on {
		out.Summary = "Scene descriptions are on for " + where + "."
	} else {
		out.Summary = "Scene descriptions are off for " + where + "."
	}
	return out
}

func init() {
	// New clips of projects with descriptions on get them, where Claude Code is ready.
	ops.DescribeReady = func(ctx context.Context, a *app.App) bool { return For(a).Status(ctx, false).Available }
	ops.DescribeRunner = func(ctx context.Context, a *app.App, as *models.Asset, projectID string) (*ops.DescribeRun, error) {
		return For(a).Describe(ctx, as, projectID)
	}
	ops.Def("footage.describe.settings", "Show whether written scene descriptions are on (workspace and project) and whether Claude Code can write them here", ops.Flags{},
		func(ctx context.Context, a *app.App, in *DescribeSettingsIn) (*DescribeSettingsOut, error) {
			return describeSettings(ctx, a, in.ProjectID), nil
		})
	ops.Def("footage.describe.configure", "Turn written scene descriptions on or off for the workspace or one project (they use your Claude Code usage)", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *DescribeConfigureIn) (*DescribeSettingsOut, error) {
			if in.On == nil && !in.Inherit {
				return nil, apperr.New(apperr.InvalidInput, "Say whether scene descriptions should be on or off (or, for a project, follow the workspace).")
			}
			if in.ProjectID == "" {
				if in.Inherit {
					return nil, apperr.New(apperr.InvalidInput, "Only a project can follow the workspace's choice.")
				}
				// The workspace's choice: owners (D191); a project's own stays open.
				if err := ops.RequireOwner(ctx, a, "footage.describe.configure"); err != nil {
					return nil, err
				}
				if _, err := a.DB.C(store.CWorkspaces).UpdateOne(ctx, bson.M{"_id": a.WorkspaceID()}, bson.M{"$set": bson.M{"describeScenes": *in.On}}); err != nil {
					return nil, err
				}
				a.Audit(ctx, "footage.describe.configure", "workspace", a.WorkspaceID(), "", map[string]any{"on": *in.On})
				return describeSettings(ctx, a, ""), nil
			}
			var p models.Project
			if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), in.ProjectID, &p); err != nil {
				return nil, apperr.New(apperr.NotFound, "MetaRush can't find that project in this workspace.").WithDetails(map[string]any{"projectId": in.ProjectID})
			}
			upd := bson.M{"$set": bson.M{"describeScenes": in.On, "updatedAt": time.Now().UTC()}}
			if in.Inherit {
				upd = bson.M{"$unset": bson.M{"describeScenes": ""}, "$set": bson.M{"updatedAt": time.Now().UTC()}}
			}
			if err := a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), p.ID, upd); err != nil {
				return nil, err
			}
			a.Audit(ctx, "footage.describe.configure", "project", p.ID, p.ID, map[string]any{"on": in.On, "inherit": in.Inherit})
			return describeSettings(ctx, a, p.ID), nil
		})
	ops.Def("footage.describe", "Have this machine's Claude Code write a sentence or two per scene of a clip (searchable; uses your Claude Code usage; on per project or workspace), or save descriptions Claude wrote in a session", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *DescribeIn) (*DescribeOut, error) {
			out := &DescribeOut{Jobs: []string{}, Skipped: []string{}, Note: usageNote}
			if len(in.Scenes) > 0 {
				var as models.Asset
				if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
					return nil, apperr.New(apperr.NotFound, "MetaRush can't find that clip in this workspace.").WithDetails(map[string]any{"assetId": in.AssetID})
				}
				n, err := ops.SaveDescriptions(ctx, a, &as, in.Scenes, "claude-code", "")
				if err != nil {
					return nil, err
				}
				out.Saved, out.Summary = n, ops.DescribeSummary(as.DisplayTitle(), n)
				return out, nil
			}
			filter := bson.M{"state": models.AssetAvailable, "media.hasVideo": true}
			switch {
			case in.AssetID != "":
				filter["_id"] = in.AssetID
			case in.ProjectID != "" && in.All:
				filter["projectIds"] = in.ProjectID
			default:
				return nil, apperr.New(apperr.InvalidInput, "Say which clip to describe, or all the clips of a project.")
			}
			var assets []models.Asset
			if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), filter, &assets); err != nil {
				return nil, err
			}
			if len(assets) == 0 {
				return nil, apperr.New(apperr.InvalidInput, "There's no clip with a picture to describe.")
			}
			// Claude Code where this computer's background work runs: here, or
			// the computer that has its role now (D349; its Claude Code answers
			// for the role, as it does the panel's conversations).
			host, err := a.PinWork(ctx)
			if err != nil {
				return nil, err
			}
			if host.Here {
				if st := For(a).Status(ctx, false); !st.Available {
					return nil, apperr.New(apperr.NotConfigured, "%s", st.Reason).WithHint(st.Hint).WithDetails(map[string]any{"reason": "claude_unavailable"})
				}
			}
			for _, as := range assets {
				pid := in.ProjectID
				if pid == "" && len(as.ProjectIDs) > 0 {
					pid = as.ProjectIDs[0]
				}
				if on, _, _ := ops.DescribeOn(ctx, a, pid); !on {
					out.Skipped = append(out.Skipped, as.DisplayTitle()+": scene descriptions are off for its project")
					continue
				}
				if !in.Force && as.Analysis[jobs.TypeDescribe] == "available" {
					out.Skipped = append(out.Skipped, as.DisplayTitle()+": it already has scene descriptions")
					continue
				}
				key := jobs.TypeDescribe + ":" + as.ID
				if in.Force {
					key += ":" + time.Now().UTC().Format("20060102T150405.000")
				}
				j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeDescribe, IdempotencyKey: key, Priority: 4, AssetID: as.ID, ProjectID: pid,
					Payload: map[string]any{"assetId": as.ID, "projectId": pid, "requested": true}, Actor: a.Actor, Installation: host.ID, MaxAttempts: 1})
				if err != nil {
					return nil, err
				}
				out.Jobs = append(out.Jobs, j.ID)
			}
			switch {
			case len(out.Jobs) == 0 && len(out.Skipped) > 0:
				out.Summary = "Nothing to describe: " + strings.Join(out.Skipped, "; ") + "."
				if strings.Contains(out.Summary, "are off") {
					out.Summary += " Turn scene descriptions on for the project or the workspace first."
				}
			default:
				out.Summary = fmt.Sprintf("Claude Code will describe %s on this machine.", countClips(len(out.Jobs)))
			}
			return out, nil
		})
}

func orDefaultName(s, d string) string {
	if s == "" {
		return d
	}
	return "“" + s + "”"
}

func countClips(n int) string {
	if n == 1 {
		return "1 clip"
	}
	return fmt.Sprintf("%d clips", n)
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	ops.Offer("footage.describe", ops.Offering{ID: "scene-descriptions", Group: "understand", Title: "Scene descriptions",
		Does:  "Claude Code writes a sentence or two about each scene, so you can search for what is seen.",
		Words: []string{"describe the video", "what happens in each scene"}, Needs: ops.Needs{Claude: true, Leaves: "pictures of each scene leave this computer, for Claude"}})
}
