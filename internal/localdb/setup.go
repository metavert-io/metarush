package localdb

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/secretpath"
)

// Saved are the addresses an earlier run of setup kept for this home (from
// the keychain or the home's secret files), when there are any. Prepare uses
// one only when it is this server's (Ours): an address of another database
// (MongoDB Atlas kept by an earlier choice, say) is never taken for it.
type Saved struct {
	URI      string // MetaRush's own sign-in
	AdminURI string // the administrator's
}

// Ours reports whether an address is a sign-in of this server for user:
// 127.0.0.1 at its port, that user with a password, its replica set.
func (s *Server) Ours(uri, user string) bool {
	if uri == "" {
		return false
	}
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "mongodb" || u.User == nil {
		return false
	}
	pw, ok := u.User.Password()
	return ok && pw != "" && u.User.Username() == user && u.Host == "127.0.0.1:"+strconv.Itoa(s.Port) && u.Query().Get("replicaSet") == ReplSet
}

func passwordOf(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.User == nil {
		return ""
	}
	pw, _ := u.User.Password()
	return pw
}

// Prepare sets the database up the first time, or checks it the next: the
// server downloaded (Install), the port recorded, the server started (it
// always requires a sign-in), the replica set, the administrator, then
// MetaRush's own sign-in, allowed to read and write the database named dbName
// only. Every step looks at what is already there first, so a setup stopped
// anywhere carries on where it stopped:
//
//   - the replica set is made only when the server has none (hello's setName),
//     and one that already has sign-ins counts as made;
//   - a saved address that is this server's (Ours) and signs in is used; one
//     that doesn't sign in yet gives its password to the sign-in made now, so
//     nothing saved is ever replaced;
//   - a new password is passed to keep (with its user) before its sign-in is
//     made, and only when that sign-in will be made: the administrator only
//     while MongoDB's first-sign-in exception is still open.
//
// The home counts as set up only once the replica set is checked to be
// MetaRush's and MetaRush's own address signs in. On a home already set up
// Prepare only makes sure the server runs.
func (s *Server) Prepare(ctx context.Context, dbName string, saved Saved, progress func(done, total int64), keep func(user, uri string) error) (fresh bool, err error) {
	if dbName == "" || dbName == "admin" || dbName == "local" || dbName == "config" {
		return false, fmt.Errorf("%q can't be MetaRush's database", dbName)
	}
	if !ValidPort(s.Port) {
		return false, fmt.Errorf("port %d can't be the database's", s.Port)
	}
	st, have := s.ReadState()
	if have && st.Initiated {
		if st.Port != s.Port {
			return false, fmt.Errorf("the database on this computer was set up on port %d, not %d", st.Port, s.Port)
		}
		return false, s.Start(ctx)
	}
	if err := s.checkPlace(); err != nil {
		return false, err
	}
	if err := s.Install(ctx, progress); err != nil {
		return false, err
	}
	// The port is this home's from now on: no other home picks it, and one
	// another home chose is refused.
	if err := reserve(s.Home, s.Port); err != nil {
		return false, err
	}
	if !have {
		st = State{Version: s.pin().Version, FeatureVersion: Series(s.pin().Version), Port: s.Port, SetUpAt: time.Now().UTC()}
		if err := s.writeState(st); err != nil {
			return false, err
		}
	}
	// Until the administrator exists, MongoDB's first-sign-in exception is
	// open while the server runs: a setup that stops before then never
	// leaves it running (setup run again starts it and carries on).
	adminReady := false
	defer func() {
		if err != nil && !adminReady {
			sctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			_ = s.Stop(sctx)
			cancel()
		}
	}()
	if err := s.Start(ctx); err != nil {
		return false, err
	}
	if err := s.ensureReplicaSet(ctx); err != nil {
		return false, err
	}
	adminURI, err := s.ensureAdmin(ctx, saved, keep)
	if err != nil {
		return false, err
	}
	adminReady = true
	uri, err := s.ensureOwn(ctx, dbName, adminURI, saved, keep)
	if err != nil {
		return false, err
	}
	// Checked, not assumed.
	if h, ok := s.hello(ctx); !ok || h["setName"] != ReplSet {
		return false, errors.New("the database on this computer isn't the one MetaRush set up")
	}
	if !s.SignsIn(ctx, uri) {
		return false, errors.New("MetaRush can't sign in to the database it set up")
	}
	st.Initiated = true
	return true, s.writeState(st)
}

// ensureReplicaSet makes the one-member replica set unless the server has it.
func (s *Server) ensureReplicaSet(ctx context.Context) error {
	h, ok := s.hello(ctx)
	if !ok {
		return errors.New("the database on this computer doesn't answer")
	}
	switch name, _ := h["setName"].(string); {
	case name == ReplSet:
		return s.waitPrimary(ctx)
	case name != "":
		return plainErr("the database on this computer was set up by something else", fmt.Errorf("replica set %q", name))
	}
	c, err := s.connect(ctx, "")
	if err != nil {
		return err
	}
	defer c.Disconnect(context.Background())
	cfg := bson.D{{Key: "_id", Value: ReplSet}, {Key: "members", Value: bson.A{bson.D{{Key: "_id", Value: 0}, {Key: "host", Value: "127.0.0.1:" + strconv.Itoa(s.Port)}}}}}
	err = c.Database("admin").RunCommand(ctx, bson.D{{Key: "replSetInitiate", Value: cfg}}).Err()
	switch codeOf(err) {
	case 0, 23: // made now, or AlreadyInitialized
	case 13: // Unauthorized: sign-ins exist, so it was made before them
		if h, ok := s.hello(ctx); !ok || h["setName"] != ReplSet {
			return plainErr("the database on this computer has sign-ins MetaRush doesn't know", err)
		}
	default:
		return plainErr("the database on this computer couldn't be prepared for MetaRush", err)
	}
	return s.waitPrimary(ctx)
}

func (s *Server) waitPrimary(ctx context.Context) error {
	c, err := s.connect(ctx, "")
	if err != nil {
		return err
	}
	defer c.Disconnect(context.Background())
	return waitPrimary(ctx, c.Database("admin"), 2*time.Minute)
}

// exceptionOpen reports whether MongoDB's first-sign-in exception is still
// open (no sign-in exists yet): only then may a connection without a sign-in
// read the replica set's status.
func (s *Server) exceptionOpen(ctx context.Context) bool {
	c, err := s.connect(ctx, "")
	if err != nil {
		return false
	}
	defer c.Disconnect(context.Background())
	err = c.Database("admin").RunCommand(ctx, bson.D{{Key: "replSetGetStatus", Value: 1}}).Err()
	return err == nil
}

var errUnknownSignIns = errors.New("the database on this computer already has sign-ins MetaRush doesn't know")

// ensureAdmin returns the administrator's address, making the administrator
// through the first-sign-in exception when it isn't there yet.
func (s *Server) ensureAdmin(ctx context.Context, saved Saved, keep func(user, uri string) error) (string, error) {
	have := s.Ours(saved.AdminURI, AdminUser)
	if have && s.SignsIn(ctx, saved.AdminURI) {
		return saved.AdminURI, nil
	}
	if !s.exceptionOpen(ctx) {
		return "", errUnknownSignIns
	}
	uri := saved.AdminURI
	if !have {
		uri = s.AdminURI(NewPassword())
		if err := keep(AdminUser, uri); err != nil {
			return "", err
		}
	}
	c, err := s.connect(ctx, "")
	if err != nil {
		return "", err
	}
	defer c.Disconnect(context.Background())
	roles := bson.A{bson.D{{Key: "role", Value: "root"}, {Key: "db", Value: "admin"}}}
	err = c.Database("admin").RunCommand(ctx, bson.D{{Key: "createUser", Value: AdminUser}, {Key: "pwd", Value: passwordOf(uri)}, {Key: "roles", Value: roles}}).Err()
	switch code := codeOf(err); {
	case err == nil:
		return uri, nil
	case code == 51003 || code == 13: // someone made a sign-in in between
		return "", plainErr(errUnknownSignIns.Error(), err)
	default:
		return "", plainErr("the database's administrator couldn't be made", err)
	}
}

// ensureOwn returns MetaRush's own address, making (or mending) its sign-in
// as the administrator: readWrite on dbName and nothing else.
func (s *Server) ensureOwn(ctx context.Context, dbName, adminURI string, saved Saved, keep func(user, uri string) error) (string, error) {
	have := s.Ours(saved.URI, User)
	if have && s.SignsIn(ctx, saved.URI) {
		return saved.URI, nil
	}
	uri := saved.URI
	if !have {
		uri = s.URI(NewPassword())
		if err := keep(User, uri); err != nil {
			return "", err
		}
	}
	ac, err := s.connect(ctx, adminURI)
	if err != nil {
		return "", err
	}
	defer ac.Disconnect(context.Background())
	admin := ac.Database("admin")
	roles := bson.A{bson.D{{Key: "role", Value: "readWrite"}, {Key: "db", Value: dbName}}}
	err = admin.RunCommand(ctx, bson.D{{Key: "createUser", Value: User}, {Key: "pwd", Value: passwordOf(uri)}, {Key: "roles", Value: roles}}).Err()
	if codeOf(err) == 51003 { // it exists with another password (a try that stopped half way): the kept one wins
		err = admin.RunCommand(ctx, bson.D{{Key: "updateUser", Value: User}, {Key: "pwd", Value: passwordOf(uri)}, {Key: "roles", Value: roles}}).Err()
	}
	if err != nil {
		return "", plainErr("MetaRush's sign-in to the database couldn't be made", err)
	}
	return uri, nil
}

// ErrUnsafePlace means the home is somewhere the database must not be: a
// folder that syncs to the cloud, or a git repository — even one that ignores
// it, where cleaning the repository's ignored files (git clean -fdX) deletes
// the projects. The key file alone opens every project, and the data is the
// projects themselves.
var ErrUnsafePlace = errors.New("the database can't be kept in a folder that syncs to the cloud or that a git repository holds: a copy of that folder would open every project")

// checkPlace refuses <home>/database in a sync folder or a git repository,
// ignored or not, with one exception: a repository whose root is the
// person's home folder (a dotfiles repository) holds every folder there, the
// suggested home included, so it is allowed with a warning (PlaceWarning).
func (s *Server) checkPlace() error {
	v := secretpath.Inspect(s.keyFile())
	if !v.Unsafe() || v.Kind == secretpath.KindGit && homeRepository(v.Root) {
		return nil
	}
	return &Error{Plain: ErrUnsafePlace.Error(), Cause: &secretpath.UnsafeError{Verdict: v, What: "the database"}}
}

// PlaceWarning is a plain warning about where the database is kept, or ""
// when there is nothing to say.
func (s *Server) PlaceWarning() string {
	if v := secretpath.Inspect(s.keyFile()); v.Kind == secretpath.KindGit && homeRepository(v.Root) {
		return "The folder that holds your projects is inside a git repository that covers your whole home folder (a dotfiles repository). Cleaning that repository's ignored files with git would delete them, so leave this folder out of such clean-ups."
	}
	return ""
}

// homeRepository reports whether a repository's root (found by walking up to
// its .git folder or file, so worktrees and submodules count) is the
// person's home folder.
func homeRepository(root string) bool {
	h, err := os.UserHomeDir()
	return err == nil && h != "" && root != "" && samePath(root, h)
}
