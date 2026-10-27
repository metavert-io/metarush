package assistant

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Deleted and cleared-out projects (D196). A deleted (archived) project's
// conversations stay readable: the panel says the project is archived and
// offers Restore, and a new message there is refused until it is restored.
// Clearing the project out for good removes its conversations with their
// saved proposals: this machine's folders at once, another machine's the next
// time MetaRush starts there (sweepGone), and a conversation held in memory is
// dropped without being saved again.

func init() {
	ops.RegisterPurgeHook("Claude Code conversations", purgeProjectConversations)
}

// purgeProjectConversations removes the Claude Code conversations of a
// project cleared out for good, and their saved proposals on this machine.
func purgeProjectConversations(ctx context.Context, a *app.App, projectID string) (int, error) {
	var docs []struct {
		ID             string `bson:"_id"`
		InstallationID string `bson:"installationId"`
	}
	if err := a.DB.Find(ctx, store.CAssistantConversations, a.WorkspaceID(), bson.M{"projectId": projectID}, &docs, options.Find().SetProjection(bson.M{"installationId": 1})); err != nil {
		return 0, err
	}
	managers.Lock()
	m := managers.m[a.Cfg.Home()]
	managers.Unlock()
	for _, d := range docs {
		if !ValidID(d.ID) {
			continue
		}
		if m != nil {
			m.forget(d.ID)
		}
		if d.InstallationID == a.Cfg.Installation.ID {
			if err := os.RemoveAll(filepath.Join(a.Cfg.AssistantDir(), "conversations", d.ID)); err != nil {
				return 0, err
			}
		}
	}
	res, err := a.DB.C(store.CAssistantConversations).DeleteMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectId": projectID})
	if err != nil {
		return 0, err
	}
	return int(res.DeletedCount), nil
}

func (c *conv) isGone() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gone
}

// forget drops a conversation whose record is gone: it is never saved again,
// and its Claude Code process ends.
func (m *Manager) forget(id string) {
	m.mu.Lock()
	c := m.convs[id]
	delete(m.convs, id)
	m.mu.Unlock()
	if c == nil {
		return
	}
	c.mu.Lock()
	c.gone, c.stopping = true, true
	if c.saveTimer != nil {
		c.saveTimer.Stop()
		c.saveTimer = nil
	}
	c.mu.Unlock()
	go m.endProcess(c)
}

// sweepGone removes the folders (saved proposals, attachments) of this
// machine's conversations whose records are gone: their project was cleared
// out for good on another machine (D196). Only folders untouched for an hour,
// and only when the database answered.
func (m *Manager) sweepGone(ctx context.Context) {
	root := filepath.Join(m.dir(), "conversations")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	var ids []string
	for _, e := range entries {
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		if info, err := e.Info(); err != nil || time.Since(info.ModTime()) < time.Hour {
			continue
		}
		ids = append(ids, e.Name())
	}
	if len(ids) == 0 {
		return
	}
	qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var docs []struct {
		ID string `bson:"_id"`
	}
	if err := m.App.DB.Find(qctx, store.CAssistantConversations, m.App.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}}, &docs, options.Find().SetProjection(bson.M{"_id": 1})); err != nil {
		return // no answer: nothing is removed
	}
	have := map[string]bool{}
	for _, d := range docs {
		have[d.ID] = true
	}
	for _, id := range ids {
		if have[id] {
			continue
		}
		m.mu.Lock()
		held := m.convs[id] != nil
		m.mu.Unlock()
		if !held {
			_ = os.RemoveAll(filepath.Join(root, id))
		}
	}
}

// projectArchived reports whether a conversation's project is archived (deleted, D196).
func (m *Manager) projectArchived(ctx context.Context, projectID string) bool {
	if projectID == "" {
		return false
	}
	var p struct {
		Archived bool `bson:"archived"`
	}
	qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := m.App.DB.C(store.CProjects).FindOne(qctx, bson.M{"_id": projectID, "workspaceId": m.App.WorkspaceID()}, options.FindOne().SetProjection(bson.M{"archived": 1})).Decode(&p); err != nil {
		return false
	}
	return p.Archived
}

// archivedErr refuses a new message in a deleted project (D196).
func archivedErr(name string) error {
	what := "This project"
	if name != "" {
		what = "“" + name + "”"
	}
	return apperr.New(apperr.Conflict, "%s is archived, so its conversations can be read but not continued.", what).
		WithHint("Restore it first (Archived projects), then carry on here.").WithDetails(map[string]any{"reason": "project_archived"})
}
