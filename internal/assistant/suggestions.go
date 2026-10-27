package assistant

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// silentLines counts narration lines that have no audio yet.
func silentLines(beats []editspec.Beat) int {
	n := 0
	for _, b := range beats {
		if b.Take == nil {
			n++
		}
	}
	return n
}

// Suggestions are 4–6 natural prompts for the current state, shown when a
// conversation has no messages yet ("Things to try"). Picking one only fills
// the input. They teach what the assistant can do; none mention the product.
func (m *Manager) Suggestions(ctx context.Context, in AssistantSuggestionsIn) (*AssistantSuggestionsOut, error) {
	a := m.App
	out := &AssistantSuggestionsOut{Suggestions: []AssistantSuggestion{}}
	add := func(label, prompt, reason string) {
		if len(out.Suggestions) < 6 {
			out.Suggestions = append(out.Suggestions, AssistantSuggestion{Label: label, Prompt: prompt, Reason: reason})
		}
	}
	inboxFilter := ops.InboxFilter()
	inboxFilter["workspaceId"], inboxFilter["state"] = a.WorkspaceID(), models.AssetAvailable
	inbox, _ := a.DB.C(store.CAssets).CountDocuments(ctx, inboxFilter)
	// Questions MetaRush is waiting on (to-dos): unnamed voices first, since
	// naming them is something Claude can help with right in the conversation.
	questions := func(projectID string) (voices, other int64) {
		f := bson.M{"workspaceId": a.WorkspaceID(), "state": models.TodoOpen}
		if projectID != "" {
			f["projectIds"] = projectID
		}
		total, _ := a.DB.C(store.CTodos).CountDocuments(ctx, f)
		f["kind"] = models.TodoSpeakerUnnamed
		voices, _ = a.DB.C(store.CTodos).CountDocuments(ctx, f)
		return voices, total - voices
	}
	if in.ProjectID == "" {
		if voices, other := questions(""); voices > 0 {
			add("Help me name the voices", "Who speaks in my footage? Help me name the voices that don't have a name yet.", fmt.Sprintf("%d unnamed voices", voices))
		} else if other > 0 {
			add("Go through the open questions", "What questions are waiting for me? Let's go through them one at a time.", fmt.Sprintf("%d open questions", other))
		}
		var projects []models.Project
		_ = a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{"archived": bson.M{"$ne": true}}, &projects,
			options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}).SetLimit(3))
		if inbox > 0 {
			add("Sort my inbox into projects", "Look at the footage in my inbox and suggest which project each file belongs in.", fmt.Sprintf("%d files in the inbox", inbox))
		}
		if len(projects) > 0 {
			p := projects[0]
			add("Review the footage in "+p.Name, "Review the footage in "+p.Name+". What are the strongest moments?", "most recent project")
			if p.Branches["main"].Head != "" {
				add("What's wrong with the latest render?", "What's wrong with the latest render of "+p.Name+"?", "it has a cut")
			} else {
				add("Make a first cut of "+p.Name, "Make a 30-second vertical cut of "+p.Name+" with captions.", "no cut yet")
			}
		}
		add("What can you do here?", "What can you help me with in this workspace? Give me a few examples based on my projects.", "")
		add("Start a new project", "Help me plan a new video project: ask me about the goal, the audience and the length.", "")
		return out, nil
	}
	if !ids.Valid(ids.Project, in.ProjectID) {
		return nil, apperr.New(apperr.InvalidInput, "MetaRush can't find that project.").WithDetails(map[string]any{"projectId": in.ProjectID})
	}
	var p models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), in.ProjectID, &p); err != nil {
		return nil, apperr.New(apperr.NotFound, "MetaRush can't find that project.").WithDetails(map[string]any{"projectId": in.ProjectID})
	}
	footage, _ := a.DB.C(store.CAssets).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectIds": p.ID, "state": models.AssetAvailable})
	head := p.Branches["main"].Head
	if voices, other := questions(p.ID); voices > 0 {
		add("Help me name the voices", "Who speaks in this footage? Help me name the voices that don't have a name yet.", fmt.Sprintf("%d unnamed voices", voices))
	} else if other > 0 {
		add("Go through the open questions", "What questions about this project are waiting for me? Let's go through them one at a time.", fmt.Sprintf("%d open questions", other))
	}
	if footage == 0 {
		add("What do I need to get started?", "This project has no footage yet. What should I shoot or bring in for it?", "no footage yet")
		if inbox > 0 {
			add("Find footage for this project in my inbox", "Which files in my inbox belong in this project?", fmt.Sprintf("%d files in the inbox", inbox))
		}
		add("Plan this video with me", "Help me plan this video: goal, audience, length and structure.", "")
		return out, nil
	}
	if head == "" {
		add("Find the 3 strongest opening moments", "Find the 3 strongest opening moments in this footage, with timestamps.", "footage but no cut yet")
		add("Make a 30-second vertical cut with captions", "Make a 30-second vertical cut with captions from the strongest moments.", "no cut yet")
		add("What's in this footage?", "What's in this footage? Summarize each clip in a sentence.", "")
		add("Find the best quotes", "Find the most quotable lines in the transcripts.", "")
	} else {
		var rev models.Revision
		_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), head, &rev)
		add("Tighten the middle, keep the ending", "Tighten the middle of the current cut and keep the ending as it is.", "there is a cut")
		var renders []models.Render
		_ = a.DB.Find(ctx, store.CRenders, a.WorkspaceID(), bson.M{"revisionId": head, "state": bson.M{"$nin": bson.A{models.RenderFailed, models.RenderCanceled}}}, &renders,
			options.Find().SetLimit(1))
		if len(renders) == 0 {
			add("Render a review copy", fmt.Sprintf("Render a review copy of version %d so I can watch it.", rev.Number), "the latest cut has no render")
		} else {
			add("Check the latest render for problems", "Check the latest render for problems before I post it.", "there is a render")
		}
		if rev.Spec.Output.Preset == "vertical-1080p" {
			add("Make a 16:9 version", "Make a 16:9 landscape version of the current cut.", "the cut is vertical")
		} else {
			add("Make a vertical version", "Make a 9:16 vertical version of the current cut for social.", "the cut is not vertical")
		}
		if vo := rev.Spec.Voiceover; vo == nil || len(vo.Beats) == 0 {
			add("Write a voiceover script for this cut", "Write a short voiceover script for the current cut, with each line placed where it fits the pictures.", "the cut has no narration")
		} else if silent := silentLines(vo.Beats); silent > 0 {
			add("Hear the narration", "Make a free scratch voice for the narration so I can check the timing.", pluralize(silent, "narration line")+" without audio")
		}
		n, _ := a.DB.C(store.CRevisions).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectId": p.ID, "approval.state": models.ApprovalApproved})
		if n > 0 {
			add("Export the approved cut", "Export the approved cut to a folder on this machine.", "a version is approved")
		}
		add("Explain this cut", "Explain why the current cut is put together the way it is, clip by clip.", "")
	}
	if inbox > 0 {
		add("Sort my inbox into projects", "Look at the footage in my inbox and suggest which project each file belongs in.", fmt.Sprintf("%d files in the inbox", inbox))
	}
	return out, nil
}
