package abilities

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// What may leave (D290): a run sends what its manifest says it sends of the
// inputs it is given, to the hosts it declared. The plan refuses inputs the
// Content Settings of the projects they belong to keep here; the proxy
// refuses, request by request, hosts those settings don't let receive them.

func init() {
	// The hosts approved abilities reach, for a client's list of who may
	// receive their things (D288).
	leave.Receivers[models.PlaceAbilities] = func(ctx context.Context, a *app.App) []leave.LeaveReceiver {
		var vs []models.AbilityVersion
		if err := a.DB.Find(ctx, store.CAbilities, a.WorkspaceID(), bson.M{"state": models.AbilityApproved}, &vs,
			options.Find().SetProjection(bson.M{"facts.hosts": 1, "title": 1, "name": 1})); err != nil {
			return nil
		}
		seen := map[string]bool{}
		out := []leave.LeaveReceiver{}
		for _, v := range vs {
			for _, h := range v.Facts.Hosts {
				if !seen[h] {
					seen[h] = true
					out = append(out, leave.LeaveReceiver{Name: h, Title: h + " (" + v.Title + ")"})
				}
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out
	}
}

// runSend is what a run sends of its inputs, and whose they are: the project
// what it brings back goes to, and each clip's and picture's projects.
func runSend(ctx context.Context, a *app.App, f models.AbilityFacts, inputs []models.AbilityRunInput, projectID, title string) leave.Send {
	s := leave.Send{Place: models.PlaceAbilities, Projects: []string{projectID}, What: "a run of the ability “" + title + "”"}
	if f.Network == NetworkOff || (reachesHere(f) && len(f.Hosts) == 0) {
		return s
	}
	var files [][]string
	words := false
	for _, in := range inputs {
		switch {
		case in.Ref != "":
			s.Projects = append(s.Projects, inputProjects(ctx, a, in.Ref)...)
			if !contains(f.Sends, fileInputSends[in.Kind]) {
				continue
			}
			switch in.Kind {
			case InFootage:
				files = append(files, []string{models.LeaveFootage, models.LeaveVoices, models.LeaveFaces})
			case InSound:
				files = append(files, []string{models.LeaveVoices})
			case InPicture:
				files = append(files, leave.RoleKinds("picture", strings.HasPrefix(in.Ref, ids.Asset+"_")))
			}
		case in.Kind == InWords && contains(f.Sends, SendsWords):
			if v, _ := in.Value.(string); strings.TrimSpace(v) != "" {
				words = true
			}
		}
	}
	s.Kinds = leave.Kinds(words, files...)
	return s
}

// inputProjects are the projects a clip or a picture belongs to ("" none).
func inputProjects(ctx context.Context, a *app.App, ref string) []string {
	if strings.HasPrefix(ref, ids.Asset+"_") {
		var as models.Asset
		if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), ref, &as) == nil {
			if len(as.ProjectIDs) == 0 {
				return []string{""}
			}
			return as.ProjectIDs
		}
		return nil
	}
	var r models.Resource
	if a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), ref, &r) == nil {
		return []string{r.ProjectID}
	}
	return nil
}

// reachesHere says a run may reach its one service on this computer (D284).
// What goes there doesn't leave this computer, so the rules about what may
// leave refuse only the ability's other hosts, request by request.
func reachesHere(f models.AbilityFacts) bool {
	return f.Network == NetworkMetaRush && f.Local != nil
}

// keptKinds is the first rule keeping what a run sends here (nil: none).
// Only who may receive it is left to the proxy, host by host — and to the
// proxy alone when the run may also reach its service on this computer.
func keptKinds(ctx context.Context, a *app.App, f models.AbilityFacts, s leave.Send) (*leave.Refusal, error) {
	if len(s.Kinds) == 0 || reachesHere(f) {
		return nil, nil
	}
	s.AnyReceiver = true
	return leave.Problem(ctx, a, s)
}

// hostsKept says which declared hosts the rules don't let receive what the
// run sends (each with its refusal's words).
func hostsKept(ctx context.Context, a *app.App, s leave.Send, hosts []string) (kept []string, why string) {
	for _, h := range hosts {
		hs := s
		hs.To, hs.ToWords, hs.Kinds = h, h, nil
		if r, err := leave.Problem(ctx, a, hs); err == nil && r != nil {
			kept = append(kept, h)
			if why == "" {
				why = r.Message
			}
		}
	}
	return kept, why
}

// hostRefuser is the proxy's Refuse for one run: the rules asked once per
// host, each refusal recorded once.
func hostRefuser(ctx context.Context, a *app.App, s leave.Send) func(host string) string {
	var mu sync.Mutex
	seen := map[string]string{}
	return func(host string) string {
		mu.Lock()
		defer mu.Unlock()
		if why, ok := seen[host]; ok {
			return why
		}
		hs := s
		hs.To, hs.ToWords, hs.Kinds = host, host, nil
		why := ""
		if r, err := leave.Problem(ctx, a, hs); err != nil {
			why = "MetaRush couldn't read the client's Content Settings, so it didn't send the request."
		} else if r != nil {
			leave.Record(ctx, a, r, hs)
			why = fmt.Sprintf("%s MetaRush didn't send the request.", r.Message)
		}
		seen[host] = why
		return why
	}
}
