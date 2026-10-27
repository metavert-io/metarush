package providers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/fakeeleven"
)

// fakeKey builds a key-shaped value at run time (no literal for the secret scanner).
func fakeKey(tail string) string { return "sk" + "_" + strings.Repeat("0a1b", 10) + tail }

type fx struct {
	a       *app.App
	fake    *fakeeleven.Server
	key     string
	project string
}

func setup(t *testing.T, secrets string) *fx {
	t.Helper()
	keyring.MockInit() // never touch the real keychain
	key := fakeKey("3f2a")
	fake := fakeeleven.New(key)
	t.Cleanup(fake.Close)
	t.Setenv(EnvElevenLabsBaseURL, fake.URL)
	t.Setenv(EnvVar(ElevenLabsName), "")
	db := testutil.DB(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Secrets = secrets
	wsID := ids.New(ids.Workspace)
	cfg.Workspace = config.Ref{ID: wsID, Name: "T"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: "studio-mac"}
	cfg.User = config.UserRef{ID: ids.New(ids.User), Name: "Ada"}
	cfg.Database = config.Database{URI: testutil.URI(), Name: db.Name}
	cfg.Media = config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: filepath.Join(h, "media")}}
	ctx := context.Background()
	a, err := app.OpenWith(ctx, cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	now := time.Now().UTC()
	_ = a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: wsID, WorkspaceID: wsID, Name: "T", CreatedAt: now})
	pid := ids.New(ids.Project)
	_ = a.DB.Insert(ctx, store.CProjects, models.Project{ID: pid, WorkspaceID: wsID, Name: "Film", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now})
	// Content Settings (D192): Film's are the default, like a workspace's first project.
	if _, err := store.MoveContentSettings(ctx, a.DB, wsID); err != nil {
		t.Fatal(err)
	}
	return &fx{a: a, fake: fake, key: key, project: pid}
}

func call[O any](t *testing.T, a *app.App, name string, in any, human bool) (*O, error) {
	t.Helper()
	return ops.CallTyped[O](context.Background(), a, name, in, ops.Caller{Human: human})
}

func TestMask(t *testing.T) {
	if Mask(fakeKey("3f2a")) != "••••3f2a" || Mask("short") != "••••" {
		t.Fatal("mask")
	}
	if ValidateKeyShape("has space in it 1234567") == nil || ValidateKeyShape("x") == nil || ValidateKeyShape(fakeKey("abcd")) != nil {
		t.Fatal("key shape")
	}
}

func TestBaseURLRefusesForeignHosts(t *testing.T) {
	t.Setenv(EnvElevenLabsBaseURL, "https://evil.example.com")
	if _, err := ElevenLabsBaseURL(""); err == nil {
		t.Fatal("a foreign host must be refused (the key would be sent there)")
	}
	t.Setenv(EnvElevenLabsBaseURL, "http://127.0.0.1:9999")
	if b, err := ElevenLabsBaseURL(""); err != nil || b != "http://127.0.0.1:9999" {
		t.Fatalf("loopback test server: %v %v", b, err)
	}
	if b, _ := elevenRegionBase("eu"); b != "https://api.eu.residency.elevenlabs.io" {
		t.Fatal(b)
	}
}

// A test that forgets its fake can never reach ElevenLabs: under go test every
// real host is refused, whether it comes from a region or is set on purpose.
func TestTestsNeverReachElevenLabs(t *testing.T) {
	for _, region := range []string{"", "us", "eu", "in", "sg"} {
		t.Setenv(EnvElevenLabsBaseURL, "")
		if b, err := ElevenLabsBaseURL(region); err == nil || !strings.Contains(err.Error(), "Tests never call ElevenLabs") {
			t.Fatalf("region %q: the real host must be refused under go test, got %q %v", region, b, err)
		}
		real, _ := elevenRegionBase(region)
		t.Setenv(EnvElevenLabsBaseURL, real)
		if _, err := ElevenLabsBaseURL(region); err == nil {
			t.Fatalf("%s set on purpose must be refused under go test", real)
		}
	}
	t.Setenv(EnvElevenLabsBaseURL, "")
	if _, err := NewElevenClient("sk_"+strings.Repeat("0", 48), ""); err == nil {
		t.Fatal("a client without a fake must not be built")
	}
	if _, err := ElevenLabsBaseURL("mars"); err == nil || !strings.Contains(err.Error(), "no region") {
		t.Fatalf("an unknown region still says so: %v", err)
	}
}

// A browser run's programs aren't under go test: they refuse real services
// because the run sets METARUSH_NO_PAID_SERVICES (scripts/lib/e2e.sh).
func TestBrowserRunsRefuseRealServices(t *testing.T) {
	t.Setenv(EnvNoPaidServices, "")
	if noPaidServices() {
		t.Fatal("refused without being asked")
	}
	t.Setenv(EnvNoPaidServices, "1")
	if !noPaidServices() || !RealServicesRefused() {
		t.Fatal("a browser run must refuse real services")
	}
}

// The key goes to the keychain only; status, op results, state and audit never contain it.
func TestSetStatusRemoveKeychain(t *testing.T) {
	f := setup(t, "keychain")
	st, err := call[Status](t, f.a, "provider.set", SetIn{Name: ElevenLabsName, Key: f.key}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Configured || st.Source != SourceKeychain || st.Masked != "••••3f2a" || st.LastTest == nil || !st.LastTest.OK {
		t.Fatalf("status after set: %+v", st)
	}
	b, _ := json.Marshal(st)
	if strings.Contains(string(b), f.key) {
		t.Fatal("op result contains the key")
	}
	if raw, _ := os.ReadFile(filepath.Join(f.a.Cfg.StateDir(), "providers.json")); strings.Contains(string(raw), f.key) || len(raw) == 0 {
		t.Fatalf("state file: %s", raw)
	}
	if raw, _ := os.ReadFile(config.Path(f.a.Cfg.Home())); strings.Contains(string(raw), f.key) {
		t.Fatal("key in config.yaml")
	}
	if _, err := os.Stat(config.DotEnvPath(f.a.Cfg.Home())); err == nil {
		t.Fatal("keychain mode must not write a .env")
	}
	var evs []models.AuditEvent
	_ = f.a.DB.Find(context.Background(), store.CAudit, f.a.WorkspaceID(), bson.M{"targetType": "provider"}, &evs)
	if len(evs) == 0 {
		t.Fatal("set was not audited")
	}
	for _, ev := range evs {
		eb, _ := json.Marshal(ev)
		if strings.Contains(string(eb), f.key) || strings.Contains(string(eb), "3f2a") {
			t.Fatalf("audit details carry key material: %s", eb)
		}
	}
	// Status from another call: masked, tested, usage known.
	so, err := call[StatusOut](t, f.a, "provider.status", StatusIn{Name: ElevenLabsName, Refresh: true}, false)
	if err != nil || so.Status.LastTest == nil || so.Status.LastTest.Usage == nil || so.Status.LastTest.Usage.Limit != 100000 {
		t.Fatalf("status: %+v %v", so, err)
	}
	// Remove: gone from the keychain and the status.
	st, err = call[Status](t, f.a, "provider.remove", NameIn{Name: ElevenLabsName}, false)
	if err != nil || st.Configured {
		t.Fatalf("remove: %+v %v", st, err)
	}
	if _, err := keyring.Get(config.KeychainService, f.a.Cfg.KeychainAccount(secretName(ElevenLabsName))); err == nil {
		t.Fatal("key still in the keychain")
	}
}

func TestSetRejectsBadKeysAndAssistant(t *testing.T) {
	f := setup(t, "keychain")
	_, err := call[Status](t, f.a, "provider.set", SetIn{Name: ElevenLabsName, Key: fakeKey("zzzz")}, false)
	if !apperr.Is(err, apperr.Provider) || !strings.Contains(err.Error(), "did not accept") {
		t.Fatalf("a wrong key must be refused: %v", err)
	}
	if strings.Contains(err.Error(), fakeKey("zzzz")) {
		t.Fatal("the error echoes the key")
	}
	if st := StatusFor(f.a.Cfg, ElevenLabs{}); st.Configured {
		t.Fatal("a refused key was stored")
	}
	// The dashboard assistant (read-only channel) cannot set keys.
	as := *f.a
	as.Actor.Via = models.ViaAssistant
	if _, err := ops.CallTyped[Status](context.Background(), &as, "provider.set", SetIn{Name: ElevenLabsName, Key: f.key}, ops.Caller{}); !apperr.Is(err, apperr.Unauthorized) {
		t.Fatalf("assistant: %v", err)
	}
	// Test a candidate key without storing it.
	out, err := call[TestOut](t, f.a, "provider.test", TestIn{Name: ElevenLabsName, Key: f.key}, false)
	if err != nil || !out.Result.OK || out.Tested != "candidate" || out.Status.Configured {
		t.Fatalf("candidate test: %+v %v", out, err)
	}
	// Not configured: an honest message.
	if _, err := call[TestOut](t, f.a, "provider.test", TestIn{Name: ElevenLabsName}, false); !apperr.Is(err, apperr.NotConfigured) || !strings.Contains(err.Error(), "not set up") {
		t.Fatalf("not configured: %v", err)
	}
}

func TestEnvModeKeyFile(t *testing.T) {
	f := setup(t, "env")
	if _, err := call[Status](t, f.a, "provider.set", SetIn{Name: ElevenLabsName, Key: f.key}, false); err != nil {
		t.Fatal(err)
	}
	p := config.DotEnvPath(f.a.Cfg.Home())
	b, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(b), EnvVar(ElevenLabsName)+"=") {
		t.Fatalf(".env: %v %s", err, b)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Fatalf(".env mode %v", st.Mode().Perm())
	}
	key, src, _ := Key(f.a.Cfg, ElevenLabsName)
	if key != f.key || src != SourceFile {
		t.Fatalf("key source %q", src)
	}
	// A key from the process environment cannot be removed by MetaRush.
	if _, err := call[Status](t, f.a, "provider.remove", NameIn{Name: ElevenLabsName}, false); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvVar(ElevenLabsName), f.key)
	if _, err := call[Status](t, f.a, "provider.remove", NameIn{Name: ElevenLabsName}, false); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("environment key: %v", err)
	}
}

func TestSettingsScopesAndResolution(t *testing.T) {
	f := setup(t, "keychain")
	if _, err := call[Status](t, f.a, "provider.set", SetIn{Name: ElevenLabsName, Key: f.key}, false); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// The default's settings (Film's, D193): Ada (by name), multilingual model, stability 0.4, a favourite.
	so, err := call[SettingsOut](t, f.a, "provider.settings", SettingsIn{Name: ElevenLabsName, Voice: "ada", ModelID: "eleven_multilingual_v2", Stability: fptr(0.4), Favorite: "voice_bram"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if so.Settings == nil || so.Settings.VoiceID != "voice_ada" || so.Settings.ModelName != "Eleven Multilingual v2" || len(so.Settings.Favorites) != 1 || so.Settings.Favorites[0].Name != "Bram" ||
		so.From == nil || so.From.ID != f.project || !strings.Contains(so.Note, "which new projects use") {
		t.Fatalf("the default's settings: %+v (note %q)", so.Settings, so.Note)
	}
	// Another project follows the default: its voice can't be changed from it; its budget can.
	other := ids.New(ids.Project)
	now := time.Now().UTC()
	_ = f.a.DB.Insert(ctx, store.CProjects, models.Project{ID: other, WorkspaceID: f.a.WorkspaceID(), Name: "Short", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now})
	if _, err := call[SettingsOut](t, f.a, "provider.settings", SettingsIn{Name: ElevenLabsName, ProjectID: other, Voice: "voice_bram"}, false); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("a follower's voice: %v", err)
	}
	// A project's budget is a person's to set (D241): not an agent's.
	if _, err := call[SettingsOut](t, f.a, "provider.settings", SettingsIn{Name: ElevenLabsName, ProjectID: other, BudgetCredits: fptr(250)}, false); !apperr.Is(err, apperr.HumanOnly) {
		t.Fatalf("a budget set by an agent: %v", err)
	}
	if so, err := call[SettingsOut](t, f.a, "provider.settings", SettingsIn{Name: ElevenLabsName, ProjectID: other, BudgetCredits: fptr(250)}, true); err != nil || so.Budget == nil || *so.Budget.LimitCredits != 250 || !so.ReadOnly || so.Settings.VoiceID != "voice_ada" {
		t.Fatalf("a follower's budget: %+v %v", so, err)
	}
	if _, err := content.Own(ctx, f.a, other, true); err != nil {
		t.Fatal(err)
	}
	// Unknown model and a non-TTS model are refused.
	if _, err := call[SettingsOut](t, f.a, "provider.settings", SettingsIn{Name: ElevenLabsName, ModelID: "eleven_english_sts_v2"}, false); !apperr.Is(err, apperr.InvalidInput) {
		t.Fatalf("speech-to-speech model accepted: %v", err)
	}
	// Its own settings (a copy of the default's): Bram, faster; budget.
	so, err = call[SettingsOut](t, f.a, "provider.settings", SettingsIn{Name: ElevenLabsName, ProjectID: other, Voice: "voice_bram", Speed: fptr(1.1), BudgetCredits: fptr(500)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if so.Budget == nil || so.Budget.LimitCredits == nil || *so.Budget.LimitCredits != 500 || so.ReadOnly {
		t.Fatalf("budget: %+v", so.Budget)
	}
	ws, proj, _, _ := LoadSettings(ctx, f.a, ElevenLabsName, other)
	r := ResolveVoice(ws, proj, nil)
	if r.VoiceID != "voice_bram" || r.From["voice"] != "settings" || r.ModelID != "eleven_multilingual_v2" || r.From["model"] != "settings" ||
		*r.Settings.Stability != 0.4 || *r.Settings.Speed != 1.1 {
		t.Fatalf("project resolution: %+v", r)
	}
	if d, _ := ResolveFor(ctx, f.a, f.project, nil); d.VoiceID != "voice_ada" {
		t.Fatalf("the default's voice changed: %+v", d)
	}
	// Following the default again: its voice is the default's, its budget stays its own.
	if _, err := content.Follow(ctx, f.a, other, ""); err != nil {
		t.Fatal(err)
	}
	if back, _ := ResolveFor(ctx, f.a, other, nil); back.VoiceID != "voice_ada" || back.Settings.Speed != nil {
		t.Fatalf("following the default: %+v", back)
	}
	if b, _ := ProjectBudget(ctx, f.a, other); b.LimitCredits == nil || *b.LimitCredits != 500 {
		t.Fatalf("budgets are never followed: %+v", b)
	}
	if b, _ := ProjectBudget(ctx, f.a, f.project); b.LimitCredits != nil {
		t.Fatalf("the default's project budget: %+v", b)
	}
	if _, err := content.Own(ctx, f.a, other, false); err != nil { // its own settings from before come back
		t.Fatal(err)
	}
	ws, proj, _, _ = LoadSettings(ctx, f.a, ElevenLabsName, other)
	r = ResolveVoice(ws, proj, nil)
	if r.VoiceID != "voice_bram" {
		t.Fatalf("its own settings from before: %+v", r)
	}
	// A line's own voice wins (two narrators).
	beat := &editspec.VoiceRef{Provider: ElevenLabsName, VoiceID: "voice_cleo", Settings: &editspec.TTSVoiceSettings{Stability: fptr(0.8)}}
	rb := ResolveVoice(ws, proj, beat)
	if rb.VoiceID != "voice_cleo" || rb.From["voice"] != "beat" || *rb.Settings.Stability != 0.8 || *rb.Settings.Speed != 1.1 {
		t.Fatalf("beat resolution: %+v", rb)
	}
	// The cache key changes with any setting, model, voice or text.
	base := CacheKey(r, "Hello there.")
	for name, mod := range map[string]func(ResolvedVoice) ResolvedVoice{
		"voice":  func(v ResolvedVoice) ResolvedVoice { v.VoiceID = "voice_ada"; return v },
		"model":  func(v ResolvedVoice) ResolvedVoice { v.ModelID = "eleven_flash_v2_5"; return v },
		"speed":  func(v ResolvedVoice) ResolvedVoice { s := 1.05; v.Settings.Speed = &s; return v },
		"format": func(v ResolvedVoice) ResolvedVoice { v.OutputFormat = "mp3_44100_192"; return v },
	} {
		if CacheKey(mod(r), "Hello there.") == base {
			t.Errorf("cache key ignores %s", name)
		}
	}
	if CacheKey(r, "Hello there. ") != base || CacheKey(r, "Hello, there.") == base {
		t.Error("cache key text handling")
	}
	// Capability validation against the live model list.
	c, _ := ElevenClientFor(ctx, f.a)
	ms, err := c.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m := PickDefaultModel(ms); m == nil || m.ModelID != "eleven_multilingual_v2" {
		t.Fatalf("default model: %+v", m)
	}
	flash := r
	flash.ModelID = "eleven_flash_v2_5"
	flash.Settings.Style = fptr(0.5)
	if w, err := ValidateVoice(flash, ms, "Hi."); err != nil || len(w) != 1 || !strings.Contains(w[0], "style") {
		t.Fatalf("style on a model without style: %v %v", w, err)
	}
	if _, err := ValidateVoice(ResolvedVoice{ModelID: "eleven_multilingual_v2"}, ms, "x"); !apperr.Is(err, apperr.NotConfigured) {
		t.Fatalf("no voice: %v", err)
	}
	if _, err := ValidateVoice(r, ms, strings.Repeat("x", 10001)); err == nil {
		t.Fatal("over the model's text limit")
	}
	if Credits("Hello.", FindModel(ms, "eleven_flash_v2_5")) != 3 {
		t.Fatal("credits use the model's cost multiplier")
	}
	// Library voices are added to the account when chosen.
	so, err = call[SettingsOut](t, f.a, "provider.settings", SettingsIn{Name: ElevenLabsName, Voice: "lib_dawn", LibraryOwnerID: "owner_1"}, false)
	if err != nil || !strings.HasPrefix(so.Settings.VoiceID, "voice_lib") || so.Settings.VoiceName != "Dawn Documentary" {
		t.Fatalf("library voice: %+v %v", so.Settings, err)
	}
}

func TestWordsFromAlignment(t *testing.T) {
	chars := strings.Split("Hi, there you.", "")
	st, en := make([]float64, len(chars)), make([]float64, len(chars))
	for i := range chars {
		st[i], en[i] = float64(i)*0.1, float64(i+1)*0.1
	}
	ws := WordsFromAlignment(chars, st, en)
	if len(ws) != 3 || ws[0].Text != "Hi," || ws[1].Text != "there" || ws[1].From != 0.4 || ws[1].To != 0.9 || ws[2].Text != "you." {
		t.Fatalf("%+v", ws)
	}
}

func fptr(v float64) *float64 { return &v }
