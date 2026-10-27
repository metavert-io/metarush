package content

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/store"
)

// Changed says what a choice did, plainly.
type Changed struct {
	// Message says what changed for the project, and what it uses now.
	Message string
	// Kept: a project that stops using its own settings keeps them aside;
	// choosing "its own" again brings them back.
	Kept bool
	// Back: "its own" brought back the settings it had before.
	Back bool
}

// Follow makes project use target's settings, live (target "": the default).
// Loops are refused; following itself is "its own".
func Follow(ctx context.Context, a *app.App, projectID, target string) (*Changed, error) {
	g, err := Load(ctx, a)
	if err != nil {
		return nil, err
	}
	if !g.Has(projectID) {
		return nil, apperr.New(apperr.NotFound, "That project isn't in this workspace.")
	}
	name := g.Name(projectID)
	if target == projectID {
		return nil, apperr.New(apperr.InvalidInput, "A project can't use its own settings by following itself: give it its own instead.").
			WithCommand("metarush content own --project " + projectID)
	}
	if target != "" && !g.Has(target) {
		return nil, apperr.New(apperr.NotFound, "That project isn't in this workspace.")
	}
	if target == "" && g.Default == projectID {
		return nil, apperr.New(apperr.InvalidInput, "%s's settings are the default: it can't use the default as well.", quote(name)).
			WithHint("Tick “Use for new projects” on another project first, or have it follow another project.")
	}
	if g.wouldLoop(projectID, target, false) {
		via := g.Name(target)
		if target == "" {
			via = "the default"
		}
		return nil, apperr.New(apperr.Conflict, "%s can't use %s's settings: they come from %s already, so they would go round in a circle.", quote(name), via, quote(name)).
			WithHint("Choose a project with settings of its own, or give this one its own.").
			WithDetails(map[string]any{"projectId": projectID, "target": target})
	}
	uses, follows := g.Uses(projectID)
	wasOwn := uses == models.ContentOwn
	if (target == "" && uses == models.ContentDefault) || (target != "" && uses == models.ContentProject && follows == target) {
		return &Changed{Message: fmt.Sprintf("%s already uses %s.", quote(name), whoseWords(g, target))}, nil
	}
	set := bson.M{"updatedAt": time.Now().UTC()}
	actor := a.Actor
	use := models.ContentUse{Uses: models.ContentDefault, Since: time.Now().UTC(), By: &actor}
	if target != "" {
		use.Uses, use.Project = models.ContentProject, target
	}
	set["content"] = use
	if err := a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), projectID, bson.M{"$set": set}); err != nil {
		return nil, err
	}
	g2, _ := Load(ctx, a)
	out := &Changed{}
	owner := ""
	if g2 != nil {
		owner = g2.Resolve(projectID).Owner
	}
	switch {
	case target == "":
		out.Message = fmt.Sprintf("%s uses the default settings now (%s's).", quote(name), g.Name(owner))
		if owner == "" {
			out.Message = fmt.Sprintf("%s uses the default settings now; no project's settings are the default yet.", quote(name))
		}
	case owner != "" && owner != target:
		out.Message = fmt.Sprintf("%s uses %s's settings now, which come from %s.", quote(name), quote(g.Name(target)), quote(g.Name(owner)))
	default:
		out.Message = fmt.Sprintf("%s uses %s's settings now: changes there show up here.", quote(name), quote(g.Name(target)))
	}
	if wasOwn {
		if rec, _ := Record(ctx, a, projectID); rec != nil && !RecordEmpty(ctx, a, rec) {
			out.Kept = true
			out.Message += " Its own settings are kept aside: choose “its own” again to get them back."
		}
	}
	a.Audit(ctx, "content.follow", "project", projectID, projectID, map[string]any{"target": target, "owner": owner})
	return out, nil
}

// Own gives a project its own settings: the ones it had before when it kept
// some aside, else empty — or, with copy, a copy of what it uses now.
func Own(ctx context.Context, a *app.App, projectID string, copy bool) (*Changed, error) {
	g, err := Load(ctx, a)
	if err != nil {
		return nil, err
	}
	if !g.Has(projectID) {
		return nil, apperr.New(apperr.NotFound, "That project isn't in this workspace.")
	}
	name := g.Name(projectID)
	r := g.Resolve(projectID)
	if r.Own() {
		return &Changed{Message: fmt.Sprintf("%s has its own settings already.", quote(name))}, nil
	}
	rec, err := Record(ctx, a, projectID)
	if err != nil {
		return nil, err
	}
	before := rec != nil && !RecordEmpty(ctx, a, rec)
	if copy && before {
		return nil, apperr.New(apperr.Conflict, "%s still has its own settings from before, so they can't be replaced by a copy.", quote(name)).
			WithHint("Give it its own settings without a copy to use them again, then change what you need.").
			WithCommand("metarush content own --project " + projectID)
	}
	if rec, err = ensure(ctx, a, projectID); err != nil {
		return nil, err
	}
	out := &Changed{}
	from := r.Owner
	if copy && from != "" {
		src, err := Record(ctx, a, from)
		if err != nil {
			return nil, err
		}
		if src != nil {
			for _, p := range Parts() {
				if p.Copy != nil {
					if err := p.Copy(ctx, a, src, rec); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	actor := a.Actor
	use := models.ContentUse{Uses: models.ContentOwn, Since: time.Now().UTC(), By: &actor}
	if err := a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), projectID, bson.M{"$set": bson.M{"content": use, "updatedAt": time.Now().UTC()}}); err != nil {
		return nil, err
	}
	switch {
	case before:
		out.Back = true
		out.Message = fmt.Sprintf("%s uses its own settings again: the ones it had before.", quote(name))
	case copy && from != "":
		out.Message = fmt.Sprintf("%s has its own settings now, starting as a copy of %s's: changes to either stay where they are made.", quote(name), quote(g.Name(from)))
	default:
		out.Message = fmt.Sprintf("%s has its own settings now. They start empty: add its brand, spellings, voice, house rules, house style and sign-offs.", quote(name))
	}
	if users := g.UsedBy(projectID); len(users) > 0 {
		out.Message += fmt.Sprintf(" Also used by %s.", projectsWord(len(users)))
	}
	a.Audit(ctx, "content.own", "project", projectID, projectID, map[string]any{"copy": copy, "from": from, "back": before})
	return out, nil
}

// SetDefault makes a project's settings the ones new projects use (its "Use
// for new projects"); there is one default, so the tick moves. A project that
// used the default itself keeps what it had: it follows the old default.
func SetDefault(ctx context.Context, a *app.App, projectID string) (*Changed, error) {
	g, err := Load(ctx, a)
	if err != nil {
		return nil, err
	}
	if !g.Has(projectID) {
		return nil, apperr.New(apperr.NotFound, "That project isn't in this workspace.")
	}
	name := g.Name(projectID)
	if g.Default == projectID {
		return &Changed{Message: fmt.Sprintf("New projects use %s's settings already.", quote(name))}, nil
	}
	old := g.Default
	uses, _ := g.Uses(projectID)
	out := &Changed{}
	actor := a.Actor
	now := time.Now().UTC()
	// A project that used the default keeps what it had: it follows the old
	// default (or, with none, has its own settings).
	var use *models.ContentUse
	if uses == models.ContentDefault || uses == "" {
		use = &models.ContentUse{Uses: models.ContentOwn, Since: now, By: &actor}
		if old != "" && g.Has(old) {
			use = &models.ContentUse{Uses: models.ContentProject, Project: old, Since: now, By: &actor}
		}
	}
	c := g.clone()
	if use != nil {
		c.nodes[projectID].Uses, c.nodes[projectID].Follows = use.Uses, use.Project
	}
	if c.wouldLoop(projectID, "", true) {
		return nil, apperr.New(apperr.Conflict, "%s's settings come from a project that uses the default, so they would go round in a circle.", quote(name)).
			WithHint("Give it its own settings first, or choose a project with settings of its own.")
	}
	if use != nil {
		if use.Uses == models.ContentOwn {
			if _, err := ensure(ctx, a, projectID); err != nil {
				return nil, err
			}
		}
		if err := a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), projectID, bson.M{"$set": bson.M{"content": *use}}); err != nil {
			return nil, err
		}
	}
	ref := models.ContentDefaultRef{ID: a.WorkspaceID(), ProjectID: projectID, SetAt: now, SetBy: &actor}
	if _, err := a.DB.C(store.CContentDefaults).ReplaceOne(ctx, bson.M{"_id": a.WorkspaceID()}, bson.M{"_id": ref.ID, "workspaceId": a.WorkspaceID(), "projectId": ref.ProjectID,
		"setAt": ref.SetAt, "setBy": ref.SetBy}, options.Replace().SetUpsert(true)); err != nil {
		return nil, err
	}
	g2, _ := Load(ctx, a)
	owner := projectID
	if g2 != nil {
		owner = g2.Resolve(projectID).Owner
	}
	out.Message = fmt.Sprintf("New projects use %s's settings now.", quote(name))
	if owner != "" && owner != projectID {
		out.Message = fmt.Sprintf("New projects use %s's settings now, which come from %s.", quote(name), quote(g.Name(owner)))
	}
	if old != "" && g.Has(old) {
		out.Message += fmt.Sprintf(" Projects that used the default use them too (before: %s's).", quote(g.Name(old)))
	}
	a.Audit(ctx, "content.setDefault", "project", projectID, projectID, map[string]any{"before": old})
	return out, nil
}

// whoseWords names the settings a project would use: another project's, or the default.
func whoseWords(g *Graph, target string) string {
	if target == "" {
		return "the default settings"
	}
	return quote(g.Name(target)) + "'s settings"
}

// repairGone sends projects that followed a project that is gone back to the
// default and tells the people involved; a default that is gone is cleared.
func repairGone(ctx context.Context, a *app.App, g *Graph, gone []goneRef) {
	for _, x := range gone {
		if x.follower == "" {
			res, err := a.DB.C(store.CContentDefaults).DeleteOne(ctx, bson.M{"_id": a.WorkspaceID(), "projectId": x.target})
			if err == nil && res.DeletedCount > 0 {
				g.Default = ""
				a.Audit(ctx, "content.defaultGone", "workspace", a.WorkspaceID(), "", map[string]any{"project": x.target})
			}
			continue
		}
		res, err := a.DB.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": x.follower, "workspaceId": a.WorkspaceID(), "content.uses": models.ContentProject, "content.project": x.target},
			bson.M{"$unset": bson.M{"content": ""}})
		if err != nil || res.ModifiedCount == 0 {
			continue
		}
		n := g.nodes[x.follower]
		n.Uses, n.Follows = models.ContentDefault, ""
		a.Audit(ctx, "content.followedGone", "project", x.follower, x.follower, map[string]any{"project": x.target})
		tellGone(ctx, a, g, n, x.target)
	}
}

// tellGone tells the people involved that a project went back to the default:
// whoever chose what it followed, and whoever made the project.
func tellGone(ctx context.Context, a *app.App, g *Graph, n *node, target string) {
	owner := g.Resolve(n.ID).Owner
	body := "It uses the default settings now: " + quote(g.Name(owner)) + "'s brand, spelling list, narration voice, house rules, house style and sign-offs."
	if owner == "" {
		body = "It uses the default settings now, but no project's settings are the default yet."
	}
	seen := map[string]bool{}
	for _, who := range []string{actorID(n.By), n.CreatedBy.UserID} {
		if who == "" || seen[who] {
			continue
		}
		seen[who] = true
		_, _ = notify.Upsert(ctx, a, models.Notification{Kind: models.NotifyContentSettings, UserID: who, GroupKey: "content_gone:" + n.ID + ":" + target,
			ProjectID: n.ID, Tone: "warning", Title: quote(n.Name) + " uses the default settings again",
			Body: "The project whose settings it used is gone. " + body, Link: links.ContentSettings(n.ID, ""),
			Details: map[string]any{"projectId": n.ID, "gone": target, "owner": owner}}, notify.Once)
	}
}

func actorID(a *models.Actor) string {
	if a == nil {
		return ""
	}
	return a.UserID
}

// ProjectGone is for whatever removes a project: its followers go back to the
// default (and are told), and a default that was it is cleared.
func ProjectGone(ctx context.Context, a *app.App, projectID string) error {
	_, err := Load(ctx, a)
	return err
}

// FirstProject gives a new project the workspace's settings when no project's
// are the default yet (a workspace's first project): it gets its own settings
// with what the workspace kept before Content Settings, and becomes the default.
func FirstProject(ctx context.Context, a *app.App, projectID string) {
	if store.ContentDefault(ctx, a.DB, a.WorkspaceID()) != "" {
		return
	}
	if def, err := store.MoveContentSettings(ctx, a.DB, a.WorkspaceID()); err == nil && def != "" {
		a.Audit(ctx, "content.setDefault", "project", def, def, map[string]any{"first": true})
	}
}
