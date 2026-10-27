package assistant

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
)

// Names in the panel (D140, D150). Every clip, version, render, cut and
// project that a step line or a card mentions is named here and nowhere else:
// the parser's labels (through Manager.displayName) and the card builders
// (nameOf, cutName, theirs). A clip is called by its descriptive name ("Lena's
// call with Maya (32 min)": the title MetaRush gave it from its first look,
// or the one a person typed; the file name only when nothing better is known);
// a version by its number and note ("version 4, “flicker fixes and headline
// cards”"); a cut by its descriptive name ("Main open day video"). Names
// people gave (clip titles, file names, notes, projects) are their own words
// and are quoted with theirs() in sentences (D113); "version 4" is MetaRush's
// own words and is not.

// displayName is the readable name of an ID ("version 4", a clip's title, a
// project's name), or "" when it is not known. Cached per manager.
func (m *Manager) displayName(ctx context.Context, id string) string {
	if !idRe.MatchString(id) {
		return ""
	}
	m.namesMu.Lock()
	if n, ok := m.names[id]; ok {
		m.namesMu.Unlock()
		return n
	}
	m.namesMu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	a := m.App
	name := ""
	switch ids.Prefix(id) {
	case ids.Asset:
		var as models.Asset
		if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &as) == nil {
			name = theirs(clipName(&as))
		}
	case ids.Project:
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), id, &p) == nil {
			name = theirs(p.Name)
		}
	case ids.Revision:
		var r models.Revision
		if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), id, &r) == nil {
			name = versionName(&r)
		}
	case ids.Render:
		var r models.Render
		if a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), id, &r) == nil && r.RevisionNumber > 0 {
			name = "version " + strconv.Itoa(r.RevisionNumber)
		}
	case ids.Resource:
		var r models.Resource
		if a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), id, &r) == nil && r.Title != "" {
			name = theirs(r.Title)
		}
	}
	if name != "" {
		m.namesMu.Lock()
		if len(m.names) > 2000 {
			m.names = map[string]string{}
		}
		m.names[id] = name
		m.namesMu.Unlock()
	}
	return name
}

// clipName is what a clip is called in the panel: its descriptive name (D150).
// Every card, step line and message to Claude names a clip through here (or
// displayName).
func clipName(as *models.Asset) string { return as.DisplayTitle() }

// versionName is a version as the panel says it: "version 4", with its note
// when it has one ("version 4, “flicker fixes and headline cards”").
func versionName(r *models.Revision) string {
	name := "version " + strconv.Itoa(r.Number)
	if n := strings.TrimSpace(r.Note); n != "" {
		name += ", " + theirs(n)
	}
	return name
}

// cutName is what a project's cut is called (D150): the name a person or
// MetaRush gave it, its version's title when that is descriptive, else
// MetaRush's suggestion ("Main open day video"); "" when the project is
// unknown.
func (m *Manager) cutName(ctx context.Context, projectID, branch string) string {
	var p models.Project
	if projectID == "" || m.App.DB.GetByID(ctx, store.CProjects, m.App.WorkspaceID(), projectID, &p) != nil {
		return ""
	}
	return ops.CutName(ctx, m.App, &p, branch)
}

// lookupName is displayName (kept for the card builders that read it).
func (m *Manager) lookupName(ctx context.Context, id string) string { return m.displayName(ctx, id) }

// nameOf is displayName, or a plain fallback ("the cut", "the footage"):
// never the ID itself.
func (m *Manager) nameOf(ctx context.Context, id string) string {
	if n := m.displayName(ctx, id); n != "" {
		return n
	}
	switch ids.Prefix(id) {
	case ids.Revision:
		return "the cut"
	case ids.Render:
		return "the render"
	case ids.Project:
		return "the project"
	case ids.Asset:
		return "the footage"
	}
	return "it"
}

// theirs marks people's own words in a sentence (a clip's title, a project's
// or a graphic's name): curly quotes, which the plain-words guards read as
// theirs (D113). Already-quoted words are left alone.
func theirs(s string) string {
	if s == "" {
		return s
	}
	if len(s) >= len(plainwords.TheirsOpen)+len(plainwords.TheirsClose) && s[:len(plainwords.TheirsOpen)] == plainwords.TheirsOpen &&
		s[len(s)-len(plainwords.TheirsClose):] == plainwords.TheirsClose {
		return s
	}
	return plainwords.TheirsOpen + s + plainwords.TheirsClose
}
