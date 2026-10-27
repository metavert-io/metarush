package remotion

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

func docID(ws, hash string) string { return ws + "/" + hash }

// NamePattern is a source's short name in its project.
var NamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// getSource loads a source by ID.
func getSource(ctx context.Context, a *app.App, id string) (*models.RemotionSource, error) {
	var s models.RemotionSource
	if err := a.DB.GetByID(ctx, store.CRemotionSources, a.WorkspaceID(), id, &s); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apperr.New(apperr.NotFound, "MetaRush doesn't know that Remotion project.").WithCommand("metarush remotion list")
		}
		return nil, err
	}
	return &s, nil
}

// listSources lists a project's sources (every project's when projectID is empty).
func listSources(ctx context.Context, a *app.App, projectID string) ([]models.RemotionSource, error) {
	filter := bson.M{"removed": bson.M{"$ne": true}}
	if projectID != "" {
		filter["projectId"] = projectID
	}
	var out []models.RemotionSource
	err := a.DB.Find(ctx, store.CRemotionSources, a.WorkspaceID(), filter, &out, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(500))
	return out, err
}

// findSource resolves a source by ID, short name or title (within a project
// when one is given). A name used in two projects needs the project.
func findSource(ctx context.Context, a *app.App, projectID, ref string) (*models.RemotionSource, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, apperr.New(apperr.InvalidInput, "Say which Remotion project (its short name).").WithCommand("metarush remotion list")
	}
	if ids.Valid(ids.RemotionSource, ref) {
		return getSource(ctx, a, ref)
	}
	all, err := listSources(ctx, a, projectID)
	if err != nil {
		return nil, err
	}
	var hits []models.RemotionSource
	for _, s := range all {
		if s.Name == ref {
			hits = append(hits, s)
		}
	}
	if len(hits) == 0 {
		for _, s := range all {
			if strings.EqualFold(s.Title, ref) || strings.EqualFold(s.Name, ref) {
				hits = append(hits, s)
			}
		}
	}
	switch len(hits) {
	case 0:
		return nil, apperr.New(apperr.NotFound, "There's no Remotion project called “%s”%s.", ref, inProject(ctx, a, projectID)).
			WithHint("Bring one in first, or use a name from the list.").WithCommand("metarush remotion list")
	case 1:
		return &hits[0], nil
	}
	return nil, apperr.New(apperr.Conflict, "More than one project has a Remotion project called “%s”.", ref).WithHint("Say which project it is in.").
		WithCommand("metarush remotion show " + ref + " --project <project>")
}

func inProject(ctx context.Context, a *app.App, projectID string) string {
	if projectID == "" {
		return ""
	}
	var p models.Project
	if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), projectID, &p) == nil {
		return " in “" + p.Name + "”"
	}
	return " in that project"
}

// getVersion loads a version by its full hash.
func getVersion(ctx context.Context, a *app.App, hash string) (*models.RemotionVersion, error) {
	var v models.RemotionVersion
	if err := a.DB.GetByID(ctx, store.CRemotionVersions, a.WorkspaceID(), docID(a.WorkspaceID(), hash), &v); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apperr.New(apperr.NotFound, "MetaRush doesn't know that version of the Remotion project.")
		}
		return nil, err
	}
	return &v, nil
}

// versionsOf lists a source's versions, newest first.
func versionsOf(ctx context.Context, a *app.App, sourceID string) ([]models.RemotionVersion, error) {
	var out []models.RemotionVersion
	err := a.DB.Find(ctx, store.CRemotionVersions, a.WorkspaceID(), bson.M{"sourceId": sourceID}, &out, options.Find().SetSort(bson.D{{Key: "number", Value: -1}}).SetLimit(1000))
	return out, err
}

var versionNumber = regexp.MustCompile(`^(?i:v|r|version\s*)?(\d{1,6})$`)

// resolveVersion finds one version of a source: "" the newest approved one
// (else the newest), "head" the newest, a number ("3", "v3"), at least 7
// characters of its version, or for the imported version the original
// commit it came from.
func resolveVersion(ctx context.Context, a *app.App, s *models.RemotionSource, ref string) (*models.RemotionVersion, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	switch ref {
	case "", "approved", "latest":
		if s.Approved != "" && ref != "latest" {
			return getVersion(ctx, a, s.Approved)
		}
		if ref == "approved" {
			return nil, apperr.New(apperr.Validation, "No version of “%s” is approved yet.", s.Title).WithHint("A person approves one under Sources in the dashboard.")
		}
		return getVersion(ctx, a, s.Head)
	case "head", "newest":
		return getVersion(ctx, a, s.Head)
	}
	vs, err := versionsOf(ctx, a, s.ID)
	if err != nil {
		return nil, err
	}
	if m := versionNumber.FindStringSubmatch(ref); m != nil && len(ref) < 7 {
		n, _ := strconv.Atoi(m[1])
		for i := range vs {
			if vs[i].Number == n {
				return &vs[i], nil
			}
		}
		return nil, apperr.New(apperr.NotFound, "“%s” has no version %d.", s.Title, n).WithCommand("metarush remotion show " + s.Name)
	}
	if len(ref) < 7 || !regexp.MustCompile(`^[0-9a-f]+$`).MatchString(ref) {
		return nil, apperr.New(apperr.InvalidInput, "“%s” isn't a version MetaRush can look up.", ref).WithHint("Use its number (like 3) or at least 7 characters of it.")
	}
	var hits []*models.RemotionVersion
	for i := range vs {
		if strings.HasPrefix(vs[i].Hash, ref) {
			hits = append(hits, &vs[i])
		}
	}
	if len(hits) == 0 && strings.HasPrefix(s.Origin.Commit, ref) {
		for i := range vs {
			if vs[i].Kind == models.RemotionKindImport && vs[i].Parent == "" {
				hits = append(hits, &vs[i])
			}
		}
	}
	switch len(hits) {
	case 0:
		return nil, apperr.New(apperr.NotFound, "“%s” has no version %s.", s.Title, ref).WithCommand("metarush remotion show " + s.Name)
	case 1:
		return hits[0], nil
	}
	return nil, apperr.New(apperr.Conflict, "More than one version of “%s” starts with %s.", s.Title, ref).WithHint("Give more of the version, or its number.")
}

// insertVersion records a new version (one per content: the same files are
// the same version) and moves the source's head. It returns the version and
// whether it was new.
func insertVersion(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion) (*models.RemotionVersion, bool, error) {
	if old, err := getVersion(ctx, a, v.Hash); err == nil {
		return old, false, nil
	}
	var after models.RemotionSource
	now := time.Now().UTC()
	err := a.DB.C(store.CRemotionSources).FindOneAndUpdate(ctx, bson.M{"_id": s.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$inc": bson.M{"versions": 1}, "$set": bson.M{"updatedAt": now}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&after)
	if err != nil {
		return nil, false, err
	}
	v.ID, v.WorkspaceID, v.SourceID, v.ProjectID = docID(a.WorkspaceID(), v.Hash), a.WorkspaceID(), s.ID, s.ProjectID
	v.Number = after.Versions
	v.CreatedAt, v.UpdatedAt = now, now
	if v.State == "" {
		v.State = models.RemotionWaiting
	}
	if v.Packages == nil {
		v.Packages = []models.RemotionPackage{}
	}
	if v.Compositions == nil {
		v.Compositions = []models.RemotionComposition{}
	}
	if _, err := a.DB.C(store.CRemotionVersions).InsertOne(ctx, v); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			old, gerr := getVersion(ctx, a, v.Hash)
			return old, false, gerr
		}
		return nil, false, err
	}
	if _, err := a.DB.C(store.CRemotionSources).UpdateOne(ctx, bson.M{"_id": s.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$set": bson.M{"head": v.Hash, "headNumber": v.Number, "updatedAt": now}}); err != nil {
		return nil, false, err
	}
	s.Head, s.HeadNumber, s.Versions = v.Hash, v.Number, after.Versions
	return v, true, nil
}

// setVersion updates fields of a version.
func setVersion(ctx context.Context, a *app.App, hash string, set bson.M) error {
	set["updatedAt"] = time.Now().UTC()
	_, err := a.DB.C(store.CRemotionVersions).UpdateOne(ctx, bson.M{"_id": docID(a.WorkspaceID(), hash), "workspaceId": a.WorkspaceID()}, bson.M{"$set": set})
	return err
}

// Decision verbs.
const (
	verbApprove = "approve"
	verbReject  = "reject"
	verbRevoke  = "revoke"
)

// decide records a person's decision about a version (compare-and-swap on
// its state, like motion graphics).
func decide(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, verb string, by models.Actor, note string) (*models.RemotionVersion, error) {
	who := fmt.Sprintf("Version %d of “%s”", v.Number, s.Title)
	state := ""
	switch verb {
	case verbApprove:
		if v.State == models.RemotionApproved {
			return nil, apperr.New(apperr.Conflict, "%s is already approved.", who)
		}
		if !v.PackagesAllowed {
			return nil, apperr.New(apperr.Validation, "%s changes the project's packages, and nobody has allowed them yet.", who).
				WithHint("Look at the packages it adds, allow them, let it be checked, then approve it.").WithCommand("metarush remotion allow " + s.Name + "@" + v.Short())
		}
		if v.Check != nil && !v.Check.OK {
			return nil, apperr.New(apperr.Validation, "%s didn't pass its check, so it can't be approved.", who).
				WithHint("Fix what the check found and check it again.").WithCommand("metarush remotion show " + s.Name + "@" + v.Short())
		}
		if v.Read == nil || !v.Read.OK {
			return nil, apperr.New(apperr.Validation, "MetaRush hasn't been able to read %s's compositions in its sandbox yet, so it can't be approved.", strings.ToLower(who[:1])+who[1:]).
				WithHint("Check it again; the check says what stopped it.").WithCommand("metarush remotion check --version " + s.Name + "@" + v.Short())
		}
		state = models.RemotionApproved
	case verbReject:
		if v.State == models.RemotionApproved {
			return nil, apperr.New(apperr.Conflict, "%s is approved, so it can't be turned down. Withdraw the approval instead.", who).
				WithCommand("metarush remotion revoke " + s.Name + "@" + v.Short())
		}
		if v.State == models.RemotionRejected {
			return nil, apperr.New(apperr.Conflict, "%s was already turned down.", who)
		}
		state = models.RemotionRejected
	case verbRevoke:
		if v.State != models.RemotionApproved {
			return nil, apperr.New(apperr.Conflict, "%s isn't approved, so there is no approval to withdraw.", who)
		}
		state = models.RemotionRevoked
	default:
		return nil, apperr.New(apperr.InvalidInput, "A version can be approved, turned down or have its approval withdrawn.")
	}
	now := time.Now().UTC()
	d := models.RemotionDecision{State: state, By: by, At: now, Note: note}
	set := bson.M{"state": state, "updatedAt": now}
	if state == models.RemotionApproved {
		set["approvedBy"], set["approvedAt"] = by, now
	}
	res, err := a.DB.C(store.CRemotionVersions).UpdateOne(ctx, bson.M{"_id": v.ID, "workspaceId": a.WorkspaceID(), "state": v.State},
		bson.M{"$set": set, "$push": bson.M{"decisions": d}})
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		return nil, apperr.New(apperr.Conflict, "Someone else decided about this version at the same moment. Look at it again.")
	}
	if err := refreshApproved(ctx, a, s); err != nil {
		return nil, err
	}
	fresh, err := getVersion(ctx, a, v.Hash)
	if err != nil {
		return nil, err
	}
	decided(ctx, a, s, fresh) // D172: its question is answered, wherever it was decided
	return fresh, nil
}

// refreshApproved points the source at its newest approved version.
func refreshApproved(ctx context.Context, a *app.App, s *models.RemotionSource) error {
	var vs []models.RemotionVersion
	if err := a.DB.Find(ctx, store.CRemotionVersions, a.WorkspaceID(), bson.M{"sourceId": s.ID, "state": models.RemotionApproved}, &vs,
		options.Find().SetSort(bson.D{{Key: "number", Value: -1}}).SetLimit(1)); err != nil {
		return err
	}
	set := bson.M{"updatedAt": time.Now().UTC()}
	unset := bson.M{}
	if len(vs) == 0 {
		s.Approved, s.ApprovedNumber = "", 0
		unset["approved"], unset["approvedNumber"] = "", ""
	} else {
		s.Approved, s.ApprovedNumber = vs[0].Hash, vs[0].Number
		set["approved"], set["approvedNumber"] = vs[0].Hash, vs[0].Number
	}
	upd := bson.M{"$set": set}
	if len(unset) > 0 {
		upd["$unset"] = unset
	}
	_, err := a.DB.C(store.CRemotionSources).UpdateOne(ctx, bson.M{"_id": s.ID, "workspaceId": a.WorkspaceID()}, upd)
	return err
}

// packagesAllowed reports whether a lockfile is one people allowed for the source.
func packagesAllowed(s *models.RemotionSource, lock string) bool {
	for _, p := range s.Allowed {
		if p.Lock == lock {
			return true
		}
	}
	return false
}

// getRender loads a cached render by key.
func getRender(ctx context.Context, a *app.App, key string) (*models.RemotionRender, error) {
	var r models.RemotionRender
	if err := a.DB.GetByID(ctx, store.CRemotionRenders, a.WorkspaceID(), docID(a.WorkspaceID(), key), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// setRender updates a render record.
func setRender(ctx context.Context, a *app.App, key string, set bson.M) error {
	set["updatedAt"] = time.Now().UTC()
	_, err := a.DB.C(store.CRemotionRenders).UpdateOne(ctx, bson.M{"_id": docID(a.WorkspaceID(), key), "workspaceId": a.WorkspaceID()}, bson.M{"$set": set})
	return err
}

// actorName is who did something, in a few words.
func actorName(x models.Actor) string {
	if x.UserName != "" {
		if f := strings.Fields(x.UserName); len(f) > 0 {
			return f[0]
		}
	}
	return "someone"
}

var _ = ops.CallerFrom
