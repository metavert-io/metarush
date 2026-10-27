package ops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"gopkg.in/yaml.v3"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/storagemove"
	"github.com/metavert-io/metarush/internal/store"
)

// Moving the media from the dashboard (D112). Settings → Storage describes the
// new storage in a form; the daemon writes it as a storage description in
// this machine's state (secrets only as references: a typed S3 secret goes to
// this machine's keychain) and storage.plan says what moving would copy. When
// the person confirms (a styled dialog), the move runs as `metarush storage
// migrate` in a process of its own — never inside the daemon, which restarts
// itself when config.yaml names the new storage (D94) — started with a
// one-time grant the daemon writes for it: a token on its stdin whose SHA-256
// is kept here (0600), valid two minutes and used once. Its progress is the
// storage_move job the activity bar shows; how it went is kept for the
// Settings page (storage.status). Nothing is ever deleted, here or by the move.

func init() {
	Def("storage.status", "Say which storage this machine uses and which one the workspace names (after another machine moved the media: this machine still uses the old storage, and the new one to switch to), whether a move runs here, and how the last move started from the dashboard on this machine went",
		Flags{}, StorageStatus)
}

// StorageTarget is a storage described in a form (Settings → Storage): what a
// storage description's media section says, with an S3 secret typed once.
type StorageTarget struct {
	Provider string `json:"provider" jsonschema:"enum=filesystem,enum=s3,enum=gcs,description=filesystem (a folder: on this computer or a shared drive)\\, s3 (S3-compatible online storage) or gcs (Google Cloud Storage)."`
	// A folder.
	Root   string `json:"root,omitempty" jsonschema:"maxLength=1024,description=filesystem: the folder that holds the media."`
	Shared bool   `json:"shared,omitempty" jsonschema:"description=filesystem: every machine of the workspace reaches the folder (a shared drive)."`
	// S3-compatible online storage.
	Preset         string `json:"preset,omitempty" jsonschema:"enum=,enum=tigris,enum=r2,enum=b2,enum=aws,enum=gcs,enum=custom"`
	Endpoint       string `json:"endpoint,omitempty" jsonschema:"maxLength=300"`
	Region         string `json:"region,omitempty" jsonschema:"maxLength=60"`
	Bucket         string `json:"bucket,omitempty" jsonschema:"maxLength=222"`
	Prefix         string `json:"prefix,omitempty" jsonschema:"maxLength=300"`
	AccessKeyID    string `json:"accessKeyId,omitempty" jsonschema:"maxLength=200"`
	ForcePathStyle bool   `json:"forcePathStyle,omitempty"`
	// SecretAccessKey is sent once and never returned: it is kept in this
	// computer's keychain and the description names it there.
	SecretAccessKey string `json:"secretAccessKey,omitempty" jsonschema:"maxLength=400,description=Sent once\\, never returned: kept in this computer's keychain."`
	// SecretVariable names the environment variable that holds the secret
	// (installations that keep their secrets in the environment).
	SecretVariable string `json:"secretVariable,omitempty" jsonschema:"maxLength=80,description=The environment variable that holds the secret (installations that keep their secrets in the environment)."`
	// Google Cloud Storage.
	CredentialsFile string `json:"credentialsFile,omitempty" jsonschema:"maxLength=1024,description=gcs: a service-account key file on this computer (empty: this computer's Google sign-in)."`
}

// DashboardMove is one move of the media started from the dashboard: where it
// goes, the person's confirmation and how it went. Kept per machine in
// <state>/storage-move/dashboard (0600).
type DashboardMove struct {
	ID       string      `json:"id"`
	To       StorageSide `json:"to"`
	Planned  time.Time   `json:"planned"`
	By       string      `json:"by,omitempty"`
	ReadBack bool        `json:"readBack,omitempty"`
	// The grant: only the token's SHA-256 is kept.
	Granted   *time.Time `json:"granted,omitempty"`
	TokenHash string     `json:"tokenHash,omitempty"`
	// The process that moves the media, and the daemon that started it.
	PID      int        `json:"pid,omitempty"`
	Parent   int        `json:"parent,omitempty"`
	Started  *time.Time `json:"started,omitempty"`
	Finished *time.Time `json:"finished,omitempty"`
	// State: planned → starting (confirmed) → running → done | failed.
	State   string   `json:"state"`
	Summary string   `json:"summary,omitempty"`
	Next    []string `json:"next,omitempty"`
	Error   string   `json:"error,omitempty"`
	Code    string   `json:"code,omitempty"`
}

// Dashboard move states.
const (
	MovePlanned  = "planned"
	MoveStarting = "starting"
	MoveRunning  = "running"
	MoveDone     = "done"
	MoveFailed   = "failed"
	// MoveStopped is only said (storage.status): the process ended without
	// saying how, or never took its grant.
	MoveStopped = "stopped"
)

// How long a plan may wait for its confirmation, and a grant for its process.
const (
	movePlanFresh  = time.Hour
	moveGrantFresh = 2 * time.Minute
)

func dashboardDir(a *app.App) string { return filepath.Join(moveDir(a), "dashboard") }

var moveIDRe = regexp.MustCompile(`^[0-9a-f]{24}$`)

func moveFile(a *app.App, id, ext string) (string, error) {
	if !moveIDRe.MatchString(id) {
		return "", apperr.New(apperr.InvalidInput, "That move isn't one this computer planned.")
	}
	return filepath.Join(dashboardDir(a), id+ext), nil
}

// DashboardMoveLog is where the process moving the media writes what it says.
func DashboardMoveLog(a *app.App, id string) (string, error) { return moveFile(a, id, ".log") }

func loadDashboardMove(a *app.App, id string) (*DashboardMove, error) {
	p, err := moveFile(a, id, ".json")
	if err != nil {
		return nil, err
	}
	var mv DashboardMove
	b, err := os.ReadFile(p)
	if err != nil || json.Unmarshal(b, &mv) != nil || mv.ID != id {
		return nil, apperr.New(apperr.NotFound, "That move isn't one this computer planned.").WithHint("Check the new storage again.")
	}
	return &mv, nil
}

func saveDashboardMove(a *app.App, mv *DashboardMove) error {
	p, err := moveFile(a, mv.ID, ".json")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dashboardDir(a), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(mv, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func newMoveID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func tokenHash(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// The storage description the daemon writes (config.yaml's media keys).
type (
	descFS struct {
		Root   string `yaml:"root"`
		Shared bool   `yaml:"shared"`
	}
	descS3 struct {
		Preset          string `yaml:"preset,omitempty"`
		Endpoint        string `yaml:"endpoint,omitempty"`
		Region          string `yaml:"region,omitempty"`
		Bucket          string `yaml:"bucket"`
		Prefix          string `yaml:"prefix,omitempty"`
		AccessKeyID     string `yaml:"accessKeyId"`
		SecretAccessKey string `yaml:"secretAccessKey"`
		ForcePathStyle  bool   `yaml:"forcePathStyle,omitempty"`
	}
	descGCS struct {
		Bucket          string `yaml:"bucket"`
		Prefix          string `yaml:"prefix,omitempty"`
		CredentialsFile string `yaml:"credentialsFile,omitempty"`
	}
	mediaDesc struct {
		Provider   string   `yaml:"provider"`
		Filesystem *descFS  `yaml:"filesystem,omitempty"`
		S3         *descS3  `yaml:"s3,omitempty"`
		GCS        *descGCS `yaml:"gcs,omitempty"`
	}
)

var envVarRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func tildePath(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

// describeTarget writes the storage a person described as a storage
// description for this move (secrets only as references) and returns its path.
// What was typed is taken as written: a storage description expands
// ${VARIABLE} and reads keychain:<name>, so typed text may do neither (else a
// form could read this computer's secrets into a name the dashboard shows, or
// send them to a storage it names).
func describeTarget(a *app.App, id string, t StorageTarget) (string, error) {
	bad := func(f string, x ...any) error { return apperr.New(apperr.InvalidInput, f, x...) }
	for _, v := range []string{t.Root, t.Preset, t.Endpoint, t.Region, t.Bucket, t.Prefix, t.AccessKeyID, t.SecretVariable, t.CredentialsFile} {
		if strings.Contains(v, "${") || strings.ContainsAny(v, "\x00\n\r") {
			return "", bad("Type the storage's details as they are: MetaRush doesn't fill in ${…} here.")
		}
	}
	if config.IsSecretRef(strings.TrimSpace(t.AccessKeyID)) {
		return "", bad("Type the access key's ID itself.")
	}
	home := a.Cfg.Home()
	doc := mediaDesc{Provider: t.Provider}
	switch t.Provider {
	case "filesystem":
		root := tildePath(strings.TrimSpace(t.Root))
		if root == "" || !filepath.IsAbs(root) {
			return "", bad("Choose the folder for the media by its full place on this computer, not only its name.")
		}
		doc.Filesystem = &descFS{Root: filepath.Clean(root), Shared: t.Shared}
	case "s3":
		preset := t.Preset
		if preset == "" {
			preset = "custom"
		}
		ps, known := media.S3Presets[preset]
		if !known {
			return "", bad("Choose which online storage it is.")
		}
		endpoint, region := strings.TrimSpace(t.Endpoint), strings.TrimSpace(t.Region)
		if endpoint == "" && !strings.Contains(ps.Endpoint, "<") {
			endpoint = ps.Endpoint
		}
		if region == "" {
			region = ps.Region
		}
		host := ""
		if endpoint != "" {
			u, err := url.Parse(endpoint)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || strings.Contains(endpoint, "<") {
				return "", bad("The storage's address should look like https://… (the service shows it with the bucket).")
			}
			host = u.Host
		} else if preset != "aws" {
			return "", bad("Add the storage's address (the service shows it with the bucket).")
		}
		bucket, keyID, prefix := strings.TrimSpace(t.Bucket), strings.TrimSpace(t.AccessKeyID), strings.Trim(strings.TrimSpace(t.Prefix), "/")
		if bucket == "" || keyID == "" {
			return "", bad("Add the bucket's name and the access key's ID.")
		}
		var secret string
		switch v := strings.TrimSpace(t.SecretVariable); {
		case t.SecretAccessKey != "" && a.Cfg.SecretMode() == "keychain":
			logbook.RememberSecret(t.SecretAccessKey)
			loc := "s3://" + bucket
			if prefix != "" {
				loc += "/" + prefix
			}
			if host != "" {
				loc += "@" + host
			}
			ref, err := config.StoreSecret(home, "s3-secret-access-key-"+storagemove.ID(a.WorkspaceID(), "", loc)[:8], strings.TrimSpace(t.SecretAccessKey))
			if err != nil {
				return "", apperr.New(apperr.NotConfigured, "This computer's keychain didn't take the secret key: %v.", err)
			}
			secret = ref
		case t.SecretAccessKey != "":
			return "", bad("This computer keeps MetaRush's keys in its environment file, not the keychain: put the secret key there as a variable and give its name instead.")
		case v != "":
			if !envVarRe.MatchString(v) {
				return "", bad("A variable's name has letters, digits and _ only.")
			}
			if val, ok := os.LookupEnv(v); !ok || val == "" {
				return "", bad("MetaRush on this computer doesn't see a variable called %s. Add it to MetaRush's environment file, restart MetaRush, then check again.", v)
			}
			secret = "${" + v + "}"
		default:
			return "", bad("Add the access key's secret.")
		}
		doc.S3 = &descS3{Preset: preset, Endpoint: endpoint, Region: region, Bucket: bucket, Prefix: prefix, AccessKeyID: keyID,
			SecretAccessKey: secret, ForcePathStyle: t.ForcePathStyle || ps.PathStyle}
	case "gcs":
		bucket := strings.TrimSpace(t.Bucket)
		if bucket == "" {
			return "", bad("Add the bucket's name.")
		}
		cred := tildePath(strings.TrimSpace(t.CredentialsFile))
		if cred != "" {
			st, err := os.Stat(cred)
			if err != nil || !st.Mode().IsRegular() || !filepath.IsAbs(cred) || !strings.EqualFold(filepath.Ext(cred), ".json") || st.Size() > 64<<10 {
				return "", bad("There is no service-account key file (a .json file) at that place on this computer.")
			}
		}
		doc.GCS = &descGCS{Bucket: bucket, Prefix: strings.Trim(strings.TrimSpace(t.Prefix), "/"), CredentialsFile: cred}
	default:
		return "", bad("Choose where the media goes: a folder, online storage or Google Cloud Storage.")
	}
	b, err := yaml.Marshal(doc)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dashboardDir(a), 0o700); err != nil {
		return "", err
	}
	p, err := moveFile(a, id, ".yaml")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(p, append([]byte("# The storage a person described in the dashboard (Settings → Storage). Secrets only as references.\n"), b...), 0o600); err != nil {
		return "", err
	}
	return p, nil
}

// PlanDashboardMove writes the storage a person described as a description
// and checks what moving to it would copy (storage.plan, on this machine);
// the move it returns is what they confirm next.
func PlanDashboardMove(ctx context.Context, a *app.App, t StorageTarget) (*DashboardMove, *StoragePlanOut, error) {
	pruneDashboardMoves(a)
	mv := &DashboardMove{ID: newMoveID(), Planned: time.Now().UTC(), By: a.Actor.UserName, State: MovePlanned}
	path, err := describeTarget(a, mv.ID, t)
	if err != nil {
		return nil, nil, err
	}
	plan, err := CallTyped[StoragePlanOut](ctx, a, "storage.plan", StoragePlanIn{To: path}, Caller{Human: true})
	if err != nil {
		_ = os.Remove(path) // the description written a moment ago, nothing else
		return nil, nil, err
	}
	mv.To = plan.To
	if err := saveDashboardMove(a, mv); err != nil {
		return nil, nil, err
	}
	return mv, plan, nil
}

// pruneDashboardMoves forgets plans nobody confirmed within a day (their
// record and description; never anything of the media).
func pruneDashboardMoves(a *app.App) {
	for _, mv := range dashboardMoves(a) {
		if mv.State == MovePlanned && time.Since(mv.Planned) > 24*time.Hour {
			for _, ext := range []string{".yaml", ".json"} {
				if p, err := moveFile(a, mv.ID, ext); err == nil {
					_ = os.Remove(p)
				}
			}
		}
	}
}

// MoveRunningHere says whether a move of the media runs on this computer now
// (started from the dashboard or a terminal).
func MoveRunningHere(ctx context.Context, a *app.App) bool {
	for _, mv := range dashboardMoves(a) {
		switch mv.State {
		case MoveStarting:
			if mv.Granted != nil && time.Since(*mv.Granted) <= moveGrantFresh {
				return true
			}
		case MoveRunning:
			if alive(mv.PID) {
				return true
			}
		}
	}
	unlock, err := lockMove(a)
	if err != nil {
		return true
	}
	unlock()
	return runningMoveJob(ctx, a) != nil
}

// GrantDashboardMove is the person's confirmation of a planned move: a
// one-time token for the process that moves the media (valid two minutes,
// used once; only its SHA-256 is kept).
func GrantDashboardMove(a *app.App, id string, readBack bool) (token string, mv *DashboardMove, err error) {
	if mv, err = loadDashboardMove(a, id); err != nil {
		return "", nil, err
	}
	if mv.State != MovePlanned {
		return "", nil, apperr.New(apperr.Conflict, "That move was started already.").WithHint("Check the new storage again to plan another.")
	}
	if time.Since(mv.Planned) > movePlanFresh {
		return "", nil, apperr.New(apperr.Conflict, "That plan is more than an hour old.").WithHint("Check the new storage again: the plan says what moving copies now.")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = hex.EncodeToString(b)
	now := time.Now().UTC()
	mv.Granted, mv.TokenHash, mv.ReadBack, mv.State = &now, tokenHash(token), readBack, MoveStarting
	if err := saveDashboardMove(a, mv); err != nil {
		return "", nil, err
	}
	return token, mv, nil
}

// ClaimDashboardMove is the moving process taking its grant: the token must
// match, be at most two minutes old and not used before; the move is then
// recorded as running in this process. It returns the description to move to.
func ClaimDashboardMove(a *app.App, id, token string) (*DashboardMove, string, error) {
	refused := apperr.New(apperr.HumanOnly, "That move wasn't confirmed by a person in the dashboard, or it was started already.")
	claim, err := moveFile(a, id, ".claim")
	if err != nil {
		return nil, "", refused
	}
	f, err := os.OpenFile(claim, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, "", refused // used once
	}
	_ = f.Close()
	mv, err := loadDashboardMove(a, id)
	if err != nil || mv.State != MoveStarting || mv.Granted == nil || time.Since(*mv.Granted) > moveGrantFresh ||
		subtle.ConstantTimeCompare([]byte(tokenHash(strings.TrimSpace(token))), []byte(mv.TokenHash)) != 1 {
		return nil, "", refused
	}
	now := time.Now().UTC()
	mv.PID, mv.Parent, mv.Started, mv.State, mv.TokenHash = os.Getpid(), os.Getppid(), &now, MoveRunning, ""
	if err := saveDashboardMove(a, mv); err != nil {
		return nil, "", err
	}
	path, _ := moveFile(a, id, ".yaml")
	return mv, path, nil
}

// FinishDashboardMove records how a move started from the dashboard went.
func FinishDashboardMove(a *app.App, id string, out *StorageMigrateOut, err error) {
	mv, lerr := loadDashboardMove(a, id)
	if lerr != nil {
		return
	}
	now := time.Now().UTC()
	mv.Finished, mv.TokenHash = &now, ""
	switch {
	case err != nil:
		ae := apperr.From(err)
		mv.State, mv.Error, mv.Code = MoveFailed, ae.Message, ae.Code
		if ae.Hint != "" {
			mv.Next = []string{ae.Hint}
		}
	case out != nil:
		mv.State, mv.Summary, mv.Next = MoveDone, out.Summary, out.Next
	default:
		mv.State = MoveDone
	}
	_ = saveDashboardMove(a, mv)
}

// alive says whether a process is still there (a zombie still is, until it is reaped).
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// dashboardMoves are this machine's moves started from the dashboard, newest first.
func dashboardMoves(a *app.App) []*DashboardMove {
	entries, _ := os.ReadDir(dashboardDir(a))
	var out []*DashboardMove
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !moveIDRe.MatchString(id) {
			continue
		}
		if mv, err := loadDashboardMove(a, id); err == nil {
			out = append(out, mv)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Planned.After(out[j].Planned) })
	return out
}

// ReapDashboardMoves waits, in a daemon that re-executed itself after a move
// it started switched the storage, for that move's process (still its child
// across the exec), so it never lingers once done. Only moves this very process
// started are waited for: another process's child is not this one's to reap.
func ReapDashboardMoves(a *app.App) {
	for _, mv := range dashboardMoves(a) {
		if mv.State != MoveRunning || mv.PID <= 0 || mv.Parent != os.Getpid() {
			continue
		}
		go func(pid int) {
			var ws syscall.WaitStatus
			_, _ = syscall.Wait4(pid, &ws, 0, nil)
		}(mv.PID)
	}
}

// StorageStatusIn has no fields.
type StorageStatusIn struct{}

// StorageMoveView is the last move started from the dashboard on this machine.
type StorageMoveView struct {
	State    string     `json:"state" jsonschema:"enum=starting,enum=running,enum=done,enum=failed,enum=stopped"`
	To       string     `json:"to" jsonschema:"description=The storage it moves to\\, in words."`
	Started  *time.Time `json:"started,omitempty"`
	Finished *time.Time `json:"finished,omitempty"`
	Summary  string     `json:"summary"`
	Next     []string   `json:"next,omitempty"`
}

// StorageStatusOut is this machine's storage and the workspace's.
type StorageStatusOut struct {
	This StorageSide `json:"this" jsonschema:"description=The storage this machine uses."`
	// Workspace is set when the workspace names another storage than this
	// machine uses: another machine moved the media.
	Workspace *StorageSide `json:"workspace,omitempty"`
	// SwitchMeToo: this machine still uses the old storage; moving it too
	// copies anything it has that the new one lacks, then switches it.
	SwitchMeToo bool `json:"switchMeToo"`
	// Target is the workspace's storage as a form to fill with this
	// machine's own keys (never a key).
	Target *StorageTarget `json:"target,omitempty"`
	// Moving: a move of the media runs on this machine (from the dashboard
	// or a terminal); Progress is the activity bar's words for it.
	Moving   bool   `json:"moving"`
	Progress string `json:"progress,omitempty"`
	JobID    string `json:"jobId,omitempty"`
	// Move is the last move started from the dashboard on this machine.
	Move *StorageMoveView `json:"move,omitempty"`
	// KeysIn is where this computer keeps MetaRush's keys: a new storage's
	// secret is typed once (keychain) or named by its variable (env).
	KeysIn  string `json:"keysIn" jsonschema:"enum=keychain,enum=env"`
	Summary string `json:"summary"`
}

// StorageStatus is storage.status.
func StorageStatus(ctx context.Context, a *app.App, _ *StorageStatusIn) (*StorageStatusOut, error) {
	out := &StorageStatusOut{This: storageSideOf(a.Cfg.Media, a.Media), KeysIn: a.Cfg.SecretMode()}
	var ws models.Workspace
	if err := a.DB.GetByID(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), &ws); err == nil && ws.MediaStore.Locator != "" && ws.MediaStore.Locator != a.Media.Locator() {
		t, side := TargetFromStore(ws.MediaStore)
		out.Workspace, out.Target = &side, &t
	}
	if mv := lastStartedMove(a); mv != nil {
		v := &StorageMoveView{State: mv.State, To: mv.To.Name, Started: mv.Started, Finished: mv.Finished, Next: mv.Next}
		switch mv.State {
		case MoveStarting:
			v.Summary = "Starting to move the media to " + mv.To.Name + "."
			if mv.Granted != nil && time.Since(*mv.Granted) > moveGrantFresh {
				v.State, v.Summary = MoveStopped, "Moving the media to "+mv.To.Name+" didn't start."
				v.Next = []string{"Check the new storage again, then move it."}
			}
		case MoveRunning:
			v.Summary = "Moving the media to " + mv.To.Name + "."
			if !alive(mv.PID) {
				v.State, v.Summary = MoveStopped, "Moving the media to "+mv.To.Name+" stopped before it finished. The old storage is unchanged."
				v.Next = []string{"Check the new storage again and move it: what was copied already isn't copied twice."}
			}
		case MoveDone:
			v.Summary = mv.Summary
		case MoveFailed:
			v.Summary = mv.Error
		}
		out.Move = v
	}
	if j := runningMoveJob(ctx, a); j != nil {
		out.Moving, out.Progress, out.JobID = true, j.Progress.Message, j.ID
	}
	dashMoving := out.Move != nil && (out.Move.State == MoveRunning || out.Move.State == MoveStarting)
	out.Moving = out.Moving || dashMoving
	out.SwitchMeToo = out.Workspace != nil && !out.Moving
	switch {
	case dashMoving:
		out.Summary = out.Move.Summary
	case out.Moving:
		out.Summary = "Moving the media to another storage on this computer."
	case out.SwitchMeToo:
		out.Summary = fmt.Sprintf("This computer still uses %s. The workspace moved to %s: switch this computer too, with its own keys.", out.This.Name, out.Workspace.Name)
	default:
		out.Summary = "This computer keeps the media in " + out.This.Name + "."
	}
	return out, nil
}

// lastStartedMove is the newest move started from the dashboard here (plans
// nobody confirmed are not moves).
func lastStartedMove(a *app.App) *DashboardMove {
	var last *DashboardMove
	for _, mv := range dashboardMoves(a) {
		if mv.State == MovePlanned {
			continue
		}
		if t := moveTime(mv); last == nil || t.After(moveTime(last)) {
			last = mv
		}
	}
	return last
}

// moveTime is when a move was confirmed (or planned).
func moveTime(mv *DashboardMove) time.Time {
	if mv.Granted != nil {
		return *mv.Granted
	}
	return mv.Planned
}

// runningMoveJob is this machine's storage_move job that is running, if any.
func runningMoveJob(ctx context.Context, a *app.App) *models.Job {
	js, err := a.Queue.List(ctx, bson.M{"type": jobs.TypeStorageMove, "state": models.JobRunning, "installation": a.Cfg.Installation.ID,
		"lease.expiresAt": bson.M{"$gte": time.Now().UTC()}}, 1)
	if err != nil || len(js) == 0 {
		return nil
	}
	return &js[0]
}

// TargetFromStore reads a store's identity (what the workspace's record
// keeps) as a form to fill — provider, bucket, folder, address; never keys —
// and names it in words.
func TargetFromStore(id models.StoreIdentity) (StorageTarget, StorageSide) {
	side := StorageSide{Provider: id.Provider, Locator: id.Locator, Shared: id.Shared}
	var t StorageTarget
	var m config.Media
	loc := id.Locator
	switch {
	case strings.HasPrefix(loc, "fs:"):
		t = StorageTarget{Provider: "filesystem", Root: strings.TrimPrefix(loc, "fs:"), Shared: id.Shared}
		m = config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: t.Root, Shared: id.Shared}}
	case strings.HasPrefix(loc, "s3://"):
		path, host, _ := strings.Cut(strings.TrimPrefix(loc, "s3://"), "@")
		bucket, prefix, _ := strings.Cut(path, "/")
		t = StorageTarget{Provider: "s3", Bucket: bucket, Prefix: prefix}
		if host != "" {
			t.Endpoint = "https://" + host
		}
		switch {
		case strings.Contains(host, "tigris.dev"):
			t.Preset = "tigris"
		case strings.Contains(host, "r2.cloudflarestorage.com"):
			t.Preset = "r2"
		case strings.Contains(host, "backblazeb2.com"):
			t.Preset = "b2"
		case host == "" || strings.Contains(host, "amazonaws.com"):
			t.Preset = "aws"
		case strings.Contains(host, "storage.googleapis.com"):
			t.Preset = "gcs"
		default:
			t.Preset = "custom"
		}
		if p, ok := media.S3Presets[t.Preset]; ok {
			t.Region, t.ForcePathStyle = p.Region, p.PathStyle
		}
		m = config.Media{Provider: "s3", S3: config.S3{Preset: t.Preset, Endpoint: t.Endpoint, Bucket: bucket, Prefix: prefix}}
	case strings.HasPrefix(loc, "gs://"):
		path, _, _ := strings.Cut(strings.TrimPrefix(loc, "gs://"), "@")
		bucket, prefix, _ := strings.Cut(path, "/")
		t = StorageTarget{Provider: "gcs", Bucket: bucket, Prefix: prefix}
		m = config.Media{Provider: "gcs", GCS: config.GCS{Bucket: bucket, Prefix: prefix}}
	default:
		side.Name = "another storage"
		return t, side
	}
	side.Name = StorageWords(m)
	return t, side
}
