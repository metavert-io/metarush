package motion

import (
	"context"
	"fmt"
	"sort"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Shared libraries (D171): a motion component folder with library.json is a
// library — the project's theme (colours, fonts, components) that several
// graphics import as @metarush/lib/<name>. A library is checked, versioned
// and approved like a graphic, once per version for every graphic that uses
// it; a graphic's check draws it with the newest approved version of each
// library it imports, and a library's check draws every graphic that uses it
// with the new version. A cut pins the library versions each graphic renders
// with (MotionRef.Libs); approving a new version moves the latest cuts' pins.

// LatestLibrary is the version of a library graphics are checked with: the
// newest approved one, else the newest checked.
func LatestLibrary(ctx context.Context, db *store.DB, workspaceID, name string) (*models.MotionComponent, error) {
	var cs []models.MotionComponent
	if err := db.Find(ctx, store.CMotion, workspaceID, bson.M{"name": name, "kind": models.MotionKindLibrary}, &cs,
		options.Find().SetSort(bson.D{{Key: "check.at", Value: -1}}).SetLimit(200)); err != nil {
		return nil, err
	}
	if len(cs) == 0 {
		return nil, nil
	}
	best := -1
	for i := range cs {
		if cs[i].State != models.MotionApproved || cs[i].ApprovedAt == nil {
			continue
		}
		if best < 0 || cs[i].ApprovedAt.After(*cs[best].ApprovedAt) {
			best = i
		}
	}
	if best < 0 {
		best = 0
	}
	return &cs[best], nil
}

// GraphicsUsing lists the latest checked version of each graphic that imports a library.
func GraphicsUsing(ctx context.Context, db *store.DB, workspaceID, library string) ([]models.MotionComponent, error) {
	cs, err := List(ctx, db, workspaceID, false)
	if err != nil {
		return nil, err
	}
	var out []models.MotionComponent
	for _, c := range cs {
		if c.Kind == models.MotionKindLibrary {
			continue
		}
		for _, l := range c.Libraries {
			if l == library {
				out = append(out, c)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// maxLibraryDraws is how many graphics a library's check draws.
const maxLibraryDraws = 6

// libraryArgs are the check's library arguments: for a graphic, each library
// it imports at the version it is checked with (--lib); for a library, the
// graphics that use it (--draw). Problems: a library the workspace has no
// version of, a name a graphic and a library would share.
func libraryArgs(ctx context.Context, a *app.App, name string, sc *archiveScan, res *CheckResult, pinned map[string]string) ([]string, []models.MotionProblem, error) {
	var args []string
	var problems []models.MotionProblem
	// One name is one kind of thing.
	if latest, err := Find(ctx, a.DB, a.WorkspaceID(), Ref{Name: name}); err == nil && len(latest) > 0 {
		wasLib := latest[0].Kind == models.MotionKindLibrary
		if wasLib != sc.Library {
			what := map[bool]string{true: "a shared library", false: "a motion graphic"}[wasLib]
			problems = append(problems, models.MotionProblem{Severity: "error", Code: "name_taken", File: "index.tsx", Line: 1,
				Message: fmt.Sprintf("%s is already the name of %s in this workspace.", name, what), Hint: "Give the folder another name."})
			return nil, problems, nil
		}
	}
	if sc.Library {
		gs, err := GraphicsUsing(ctx, a.DB, a.WorkspaceID(), name)
		if err != nil {
			return nil, nil, err
		}
		if len(gs) > maxLibraryDraws {
			gs = gs[:maxLibraryDraws]
		}
		for i := range gs {
			p, err := ArchivePath(ctx, a, &gs[i])
			if err != nil {
				return nil, nil, err
			}
			args = append(args, "--draw", gs[i].Name+"="+gs[i].Hash+"="+p)
			res.UsedBy = append(res.UsedBy, gs[i].Name)
			res.drawn = append(res.drawn, gs[i])
		}
		return args, nil, nil
	}
	res.Libraries = sc.Libraries
	for _, l := range sc.Libraries {
		var c *models.MotionComponent
		var err error
		if h, ok := pinned[l]; ok {
			if c, err = Get(ctx, a.DB, a.WorkspaceID(), h); err != nil {
				return nil, nil, err
			}
		} else if c, err = LatestLibrary(ctx, a.DB, a.WorkspaceID(), l); err != nil {
			return nil, nil, err
		}
		if c == nil {
			problems = append(problems, models.MotionProblem{Severity: "error", Code: "library_unknown", File: "index.tsx", Line: 1,
				Message: fmt.Sprintf("It uses the shared library %s, which this workspace has no checked version of.", l), Hint: "Check the library first (its folder, like the graphic's), then check this graphic again."})
			continue
		}
		p, err := ArchivePath(ctx, a, c)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, "--lib", l+"="+c.Hash+"="+p)
		res.CheckedWith = append(res.CheckedWith, models.MotionLibraryPin{Name: l, Hash: c.Hash})
		res.libRefs = append(res.libRefs, c.Resources...)
	}
	return args, problems, nil
}
