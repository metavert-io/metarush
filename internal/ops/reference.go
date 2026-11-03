package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
)

// Reference clips (D398): a video someone else made, kept to learn from — what
// it does, how it is paced — and never played in a cut. "Start from a video you
// like" brings its link in as one; anyone may mark a clip so (it only takes
// away a use), while a cut plays it nowhere; only a person says a video is
// theirs (footage.reference.mine, people only), which takes the mark off. The
// edit check refuses a reference clip in a cut's clips or sound
// (reference_footage, with how to mark it as yours), and Recipes never take
// one as footage.

// FootageReferenceIn names a clip.
type FootageReferenceIn struct {
	AssetID string `json:"assetId" jsonschema:"minLength=1,description=The clip: its ID or its name."`
}

// FootageReferenceOut is where a clip stands after the change.
type FootageReferenceOut struct {
	Clip AssetSummary `json:"clip"`
	// Reference: it is a reference now (someone else's video, kept to learn from).
	Reference bool `json:"reference"`
	// Changed: this call changed it (false: it was so already).
	Changed bool   `json:"changed"`
	Message string `json:"message"`
}

func init() {
	Def("footage.reference", "Mark a clip as a reference: someone else's video, kept to learn from (what it does, how it is paced), that no cut plays; refused while a cut's latest version plays it", Flags{Mutating: true}, markReference)
	Def("footage.reference.mine", "Say a reference clip is your own video, so cuts can play it again — people only", Flags{Mutating: true, HumanOnly: true}, referenceMine)
}

func markReference(ctx context.Context, a *app.App, in *FootageReferenceIn) (*FootageReferenceOut, error) {
	as, err := ResolveFootage(ctx, a, in.AssetID, "")
	if err != nil {
		return nil, err
	}
	title := as.DisplayTitle()
	if as.IsReference() {
		return &FootageReferenceOut{Clip: summarize(*as), Reference: true, Message: fmt.Sprintf("“%s” is a reference already: someone else's video, kept to learn from.", title)}, nil
	}
	if err := MarkReference(ctx, a, as, models.ReferenceMarked); err != nil {
		return nil, err
	}
	return &FootageReferenceOut{Clip: summarize(*as), Reference: true, Changed: true,
		Message: fmt.Sprintf("“%s” is a reference now: someone else's video, kept to learn from. No cut plays it; if it's your own video, say so on its page.", title)}, nil
}

// MarkReference marks a clip as a reference (D398), refused while the latest
// version of a cut plays it (taking it out is the person's edit first). The
// clip in memory is updated too.
func MarkReference(ctx context.Context, a *app.App, as *models.Asset, why string) error {
	if as.IsReference() {
		return nil
	}
	uses, err := HeadsPlaying(ctx, a, as.ID) // anywhere a latest version plays it, nested cuts too (D440)
	if err != nil {
		return apperr.Wrap(apperr.Database, err, "MetaRush couldn't look at which cuts play “%s”.", as.DisplayTitle())
	}
	if len(uses) > 0 {
		var where []string
		for i, u := range uses {
			if i == 3 {
				where = append(where, fmt.Sprintf("%d more", len(uses)-3))
				break
			}
			where = append(where, "“"+branchWords(u.Project.Name, u.Branch)+"”")
		}
		return apperr.New(apperr.Conflict, "“%s” plays in the latest version of %s, so it can't be a reference while a cut uses it.", as.DisplayTitle(), joinAnd(where)).
			WithHint("Take it out of those cuts first, or keep it as your own footage.").
			WithDetails(map[string]any{"reason": "used_in_cut", "cuts": len(uses)})
	}
	ref := &models.AssetReference{By: a.Actor, At: time.Now().UTC(), Why: why}
	res, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": as.ID, "workspaceId": a.WorkspaceID(), "reference": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"reference": ref, "updatedAt": time.Now().UTC()}})
	if err != nil {
		return apperr.Wrap(apperr.Database, err, "MetaRush couldn't mark “%s” as a reference.", as.DisplayTitle())
	}
	as.Reference = ref
	if res.ModifiedCount > 0 {
		a.Audit(ctx, "footage.reference", "asset", as.ID, firstOr(as.ProjectIDs), map[string]any{"why": why})
		_ = speakers.Reconcile(ctx, a, as.ID) // nobody in someone else's video is asked about: its voices' and faces' questions close
	}
	return nil
}

func referenceMine(ctx context.Context, a *app.App, in *FootageReferenceIn) (*FootageReferenceOut, error) {
	if a.Actor.Agent != "" || a.ProposeOnly || a.Actor.Via == models.ViaAssistant || a.Actor.Via == models.ViaAssistantAuto {
		return nil, apperr.New(apperr.HumanOnly, "Only a person says a video is theirs: Claude, Auto and AI apps never do.").
			WithDetails(map[string]any{"reason": "people_only"})
	}
	as, err := ResolveFootage(ctx, a, in.AssetID, "")
	if err != nil {
		return nil, err
	}
	title := as.DisplayTitle()
	if !as.IsReference() {
		return &FootageReferenceOut{Clip: summarize(*as), Message: fmt.Sprintf("“%s” is your footage already: cuts can play it.", title)}, nil
	}
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": as.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$unset": bson.M{"reference": ""}, "$set": bson.M{"updatedAt": time.Now().UTC()}}); err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't change “%s”.", title)
	}
	was := as.Reference
	as.Reference = nil
	details := map[string]any{}
	if was != nil {
		details["markedBy"], details["markedAt"] = was.By.UserID, was.At
	}
	a.Audit(ctx, "footage.reference.mine", "asset", as.ID, firstOr(as.ProjectIDs), details)
	_ = speakers.Reconcile(ctx, a, as.ID) // the person's own video: who speaks and who is on screen are asked about again
	return &FootageReferenceOut{Clip: summarize(*as), Changed: true, Message: fmt.Sprintf("“%s” is yours now: cuts can play it.", title)}, nil
}

// RefuseReference refuses to make anything from a reference (D441): someone
// else's video is never sent to an AI service nor made into new footage,
// pictures or sound (what came back would be ordinary footage). doing says
// what was asked ("make a shot from it").
func RefuseReference(as *models.Asset, doing string) error {
	return apperr.New(apperr.Conflict, "%s, so MetaRush doesn't %s. If it's your own video, say so on its page (This is mine) and try again.", ReferenceWords(as), doing).
		WithCommand("metarush footage reference " + as.ID + " --mine").
		WithDetails(map[string]any{"reason": "reference", "assetId": as.ID})
}

// ReferencesPlayed lists the references a spec plays, in the cuts it nests too
// (D441): what holds a version back from going out.
func ReferencesPlayed(ctx context.Context, a *app.App, s *editspec.Spec) ([]models.Asset, error) {
	ids := map[string]bool{}
	var collect func(s *editspec.Spec, depth int)
	collect = func(s *editspec.Spec, depth int) {
		for _, id := range editspec.AssetIDs(s) {
			ids[id] = true
		}
		if depth > editspec.MaxNestDepth {
			return
		}
		for _, c := range s.Clips {
			if _, revID, ok := editspec.NestedRef(c.AssetID); ok {
				var r models.Revision
				if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), revID, &r) == nil {
					collect(&r.Spec, depth+1)
				}
			}
		}
	}
	collect(s, 1)
	if len(ids) == 0 {
		return nil, nil
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	var out []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": list}, "reference": bson.M{"$exists": true}}, &out,
		options.Find().SetSort(bson.D{{Key: "_id", Value: 1}})); err != nil {
		return nil, err
	}
	return out, nil
}

// ReferenceWords says a reference clip plainly for refusals elsewhere ("“X” is
// a reference: someone else's video, kept to learn from").
func ReferenceWords(as *models.Asset) string {
	return "“" + strings.TrimSpace(as.DisplayTitle()) + "” is a reference: someone else's video, kept to learn from"
}

// AskStartFrom records on a clip a draft asked for from it (D401), unless one
// is asked for or made already (a failed one is asked again), then tells the
// source's hooks so the draft is made at once when MetaRush has looked at the
// clip. It returns the clip's request as it stands.
func AskStartFrom(ctx context.Context, a *app.App, assetID string, sf models.StartFromVideo) (*models.StartFromVideo, error) {
	sf = models.StartFromVideo{For: sf.For, ProjectID: sf.ProjectID, Name: sf.Name, Title: sf.Title, State: models.StartFromWaiting, At: time.Now().UTC()}
	filter := bson.M{"_id": assetID, "workspaceId": a.WorkspaceID(), "$or": bson.A{bson.M{"startFrom": bson.M{"$exists": false}}, bson.M{"startFrom.state": models.StartFromFailed}}}
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, filter, bson.M{"$set": bson.M{"startFrom": sf, "updatedAt": time.Now().UTC()}}); err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't keep what you asked for on the clip.")
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
		return nil, notFound("asset", assetID)
	}
	SourceFactsChanged(ctx, a, assetID, FactsAsked)
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
		return nil, notFound("asset", assetID)
	}
	return as.StartFrom, nil
}

// VisualsPossible reports whether some computer of the workspace can look at
// what's on screen (Apple Vision on a Mac, D73): this one, or one seen in the
// last month that offers it.
func VisualsPossible(ctx context.Context, a *app.App) bool { return visualsPossible(ctx, a) }

// CutsOf are a clip's hard cuts from its stored technical evidence.
func CutsOf(ev *evidence.Stored) []float64 { return cutsOf(ev) }
