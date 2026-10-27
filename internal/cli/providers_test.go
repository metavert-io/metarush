package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/fakeeleven"
)

// testHome creates an initialized env-mode installation on a throwaway database.
func testHome(t *testing.T) string {
	t.Helper()
	db := testutil.DB(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Secrets = "env"
	wsID := ids.New(ids.Workspace)
	cfg.Workspace = config.Ref{ID: wsID, Name: "T"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: "laptop"}
	cfg.User = config.UserRef{ID: ids.New(ids.User), Name: "Ada"}
	cfg.Database = config.Database{URI: testutil.URI(), Name: db.Name}
	cfg.Media = config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: filepath.Join(h, "media")}}
	if err := config.Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	_ = db.Insert(context.Background(), store.CWorkspaces, models.Workspace{ID: wsID, WorkspaceID: wsID, Name: "T", CreatedAt: time.Now().UTC()})
	return h
}

func runProviders(t *testing.T, home, stdinText string, args ...string) (string, error) {
	t.Helper()
	old := homeFlag
	homeFlag = home
	defer func() { homeFlag = old }()
	cmd := providersCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.PersistentFlags().Bool("json", false, "")
	cmd.SetArgs(args)
	cmd.SetIn(strings.NewReader(stdinText))
	cmd.SetContext(context.Background())
	// Capture stdout (commands print with fmt).
	r, w, _ := os.Pipe()
	saved := os.Stdout
	os.Stdout = w
	err := cmd.Execute()
	w.Close()
	os.Stdout = saved
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), err
}

func TestProvidersSetFromStdinPromptAndEnv(t *testing.T) {
	keyring.MockInit()
	key := "sk" + "_" + strings.Repeat("9c8d", 10) + "77aa"
	fake := fakeeleven.New(key)
	defer fake.Close()
	t.Setenv(providers.EnvElevenLabsBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), "")
	h := testHome(t)

	// stdin
	out, err := runProviders(t, h, key+"\n", "set", "elevenlabs", "--key-stdin", "--json")
	if err != nil {
		t.Fatalf("set --key-stdin: %v\n%s", err, out)
	}
	if strings.Contains(out, key) || !strings.Contains(out, "••••77aa") {
		t.Fatalf("output must show only the masked key:\n%s", out)
	}
	env, _ := os.ReadFile(config.DotEnvPath(h))
	if !strings.Contains(string(env), providers.EnvVar(providers.ElevenLabsName)+"="+key) {
		t.Fatalf(".env does not hold the key: %q", env)
	}
	// A key given as an argument is never accepted.
	if _, err := runProviders(t, h, "", "set", "elevenlabs", key); err == nil {
		t.Fatal("a key on the command line must be refused")
	}
	// Remove, then the hidden prompt path.
	if _, err := runProviders(t, h, "", "remove", "elevenlabs", "--yes"); err != nil {
		t.Fatal(err)
	}
	isInteractive = func() bool { return true }
	asked := ""
	promptSecret = func(q string) string { asked = q; return key }
	defer func() { isInteractive, promptSecret = interactive, askSecret }()
	out, err = runProviders(t, h, "", "set", "elevenlabs")
	if err != nil || !strings.Contains(asked, "ElevenLabs API key") {
		t.Fatalf("prompt: %v %q\n%s", err, asked, out)
	}
	if strings.Contains(out, key) || !strings.Contains(out, "set up on this machine") {
		t.Fatalf("human output:\n%s", out)
	}
	// From a named environment variable.
	_, _ = runProviders(t, h, "", "remove", "elevenlabs", "--yes")
	t.Setenv("MY_TTS_KEY", key)
	if out, err := runProviders(t, h, "", "set", "elevenlabs", "--key-env", "MY_TTS_KEY", "--json"); err != nil || strings.Contains(out, key) {
		t.Fatalf("--key-env: %v\n%s", err, out)
	}
	// Status and test, plain words.
	out, err = runProviders(t, h, "", "status", "elevenlabs")
	if err != nil || !strings.Contains(out, "set up (key ••••77aa") || strings.Contains(out, key) {
		t.Fatalf("status:\n%s %v", out, err)
	}
	out, err = runProviders(t, h, "", "test", "elevenlabs")
	if err != nil || !strings.Contains(out, "Connected") {
		t.Fatalf("test:\n%s %v", out, err)
	}
	// A wrong candidate key, tested from stdin without saving.
	out, err = runProviders(t, h, "sk_"+strings.Repeat("0", 30), "test", "elevenlabs", "--key-stdin")
	if err != nil || !strings.Contains(out, "did not accept") {
		t.Fatalf("candidate test:\n%s %v", out, err)
	}
}
