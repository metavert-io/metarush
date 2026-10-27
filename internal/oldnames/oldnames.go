// Package oldnames keeps the stored names D210 took away, so documents that
// still carry them keep reading. "Recipe" is the Recipes feature's word only:
// a fixed copy's recipe became its fix steps (named by their fingerprint, the
// same hash as before), a generated picture's became how it was made, and a
// Remotion refresh's became its refresh steps.
//
// Migration 37 renames the fields in place. Until every computer that shares
// the database runs a build with D210, an older build can still write the old
// names (scripts/deploy.sh migrates before it restarts the background
// service, and a worker in a terminal — `metarush worker start`, `--wait` —
// never migrates), and a newer one can read before anything migrated. So
// readers take both names, the old one first: only older builds write it, so a
// document that has both got the old one last. The decoders in
// internal/models read documents this way and the filters here query them;
// every write of a renamed field removes its old name. Nothing else uses these
// names: the guard on the word (internal/plainwords) allows this package only.
package oldnames

import "go.mongodb.org/mongo-driver/v2/bson"

// The names before D210, and where they were.
const (
	// Fingerprint named a fixed copy's fingerprint on a source's fixes
	// (fixes.fingerprint), on the copy it uses (fixes.copy.fingerprint) and
	// on the copy's own record (source_copies: fingerprint).
	Fingerprint = "recipe"
	// FixSteps named a fixed copy's fix steps (source_copies: fixSteps).
	FixSteps = "recipeSteps"
	// MadeWith named how a generated resource was made (resources:
	// versions[].source.madeWith).
	MadeWith = "recipe"
	// RefreshSteps named the steps of a Remotion refresh (remotion_refreshes:
	// refreshSteps).
	RefreshSteps = "recipe"
)

// FixesFingerprint is the old path of a source's fingerprint; every write of
// fixes.fingerprint unsets it.
const FixesFingerprint = "fixes." + Fingerprint

// FingerprintIs matches clips and pictures whose ticked fixes have the
// fingerprint h — "" matches those whose ticked fixes make no copy — under the
// old name where a document still has it, else under fixes.fingerprint.
func FingerprintIs(h string) bson.M {
	oldIs, newIs := bson.M{"$exists": true, "$eq": h}, any(h)
	if h == "" {
		oldIs = bson.M{"$exists": true, "$in": bson.A{nil, ""}}
		newIs = bson.M{"$in": bson.A{nil, ""}}
	}
	return bson.M{"$or": bson.A{
		bson.M{FixesFingerprint: oldIs},
		bson.M{FixesFingerprint: bson.M{"$exists": false}, "fixes.fingerprint": newIs},
	}}
}

// MadeFrom matches resources with a version made from the repository, under
// either name of how it was made (callers check the version they decoded).
func MadeFrom(repoID string) bson.M {
	return bson.M{"$or": bson.A{
		bson.M{"versions.source.madeWith.repoId": repoID},
		bson.M{"versions.source." + MadeWith + ".repoId": repoID},
	}}
}
