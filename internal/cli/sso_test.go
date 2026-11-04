package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/sso"
	"github.com/metavert-io/metarush/internal/testutil/fakeoidc"
)

// runSSO runs `metarush auth sso <args>` in home with stdinText as standard
// input; it returns what went to standard output and standard error.
func runSSO(t *testing.T, home, stdinText string, args ...string) (string, string, error) {
	t.Helper()
	old := homeFlag
	homeFlag = home
	defer func() { homeFlag = old }()
	cmd := authSSOCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.PersistentFlags().Bool("json", false, "")
	cmd.SetArgs(args)
	cmd.SetIn(strings.NewReader(stdinText))
	cmd.SetContext(context.Background())
	or, ow, _ := os.Pipe()
	er, ew, _ := os.Pipe()
	savedOut, savedErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = ow, ew
	var outBuf, errBuf bytes.Buffer
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(&outBuf, or); done <- struct{}{} }()
	go func() { _, _ = io.Copy(&errBuf, er); done <- struct{}{} }()
	err := cmd.Execute()
	ow.Close()
	ew.Close()
	<-done
	<-done
	os.Stdout, os.Stderr = savedOut, savedErr
	return outBuf.String(), errBuf.String(), err
}

// codeOf is the error code a --json answer carries ("" when it worked).
func codeOf(t *testing.T, out string) string {
	t.Helper()
	var doc struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not one JSON document: %v\n%s", err, out)
	}
	if doc.OK {
		return ""
	}
	return doc.Error.Code
}

// The reviewer's run (D450): `auth sso secret --stdin` and `auth sso set
// --secret-stdin` with a pipe got E_HUMAN_ONLY, as the person check wanted a
// terminal on standard input, and at a terminal the secret was echoed. A
// terminal on standard error now is a person, the secret comes through the
// pipe, a terminal reads it hidden, and an AI app at the keys is still refused.
func TestSSOSecretComesThroughAPipeOrHidden(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(sso.Forget)
	t.Setenv("CLAUDECODE", "")
	t.Setenv("METARUSH_AGENT", "")
	h := testHome(t)
	secret := sso.Random(24)
	p := fakeoidc.New("cli-app", secret)
	t.Cleanup(p.Close)
	oldTerm, oldStdin, oldHidden := atTerminal, stdinIsTerminal, readHidden
	t.Cleanup(func() { atTerminal, stdinIsTerminal, readHidden = oldTerm, oldStdin, oldHidden })
	atTerminal = func() bool { return true }       // a person: a terminal on standard error
	stdinIsTerminal = func() bool { return false } // the secret comes through a pipe
	cfg, err := config.Load(h)
	if err != nil {
		t.Fatal(err)
	}
	app := &models.SSOSettings{Provider: sso.OIDC, Issuer: p.Issuer, ClientID: "cli-app"}
	setArgs := []string{"set", "--provider", "oidc", "--issuer", p.Issuer, "--client-id", "cli-app", "--label", "Test provider"}

	// The setup with its secret through the pipe: kept, waiting for its test.
	out, errOut, err := runSSO(t, h, secret+"\n", append(setArgs, "--secret-stdin", "--json")...)
	if err != nil || codeOf(t, out) != "" {
		t.Fatalf("set --secret-stdin: %v\n%s\n%s", err, out, errOut)
	}
	if s, _, _ := auth.SSONextSecret(cfg, app); s != secret {
		t.Fatal("the secret from the pipe wasn't kept as the one waiting")
	}
	if strings.Contains(out+errOut, secret) {
		t.Fatal("the secret was printed")
	}
	// A new secret through the pipe: it waits too.
	next := sso.Random(24)
	out, errOut, err = runSSO(t, h, next, "secret", "--stdin", "--json")
	if err != nil || codeOf(t, out) != "" {
		t.Fatalf("secret --stdin: %v\n%s\n%s", err, out, errOut)
	}
	if s, _, _ := auth.SSONextSecret(cfg, app); s != next || strings.Contains(out+errOut, next) {
		t.Fatal("secret --stdin")
	}
	// A pipe without saying so: nothing is asked, nothing kept.
	if out, _, _ := runSSO(t, h, "x", append(setArgs, "--json")...); codeOf(t, out) != "E_INVALID_INPUT" {
		t.Fatalf("set with a pipe and no --secret-stdin: %s", out)
	}
	if out, _, _ := runSSO(t, h, next, "secret", "--json"); codeOf(t, out) != "E_INVALID_INPUT" {
		t.Fatalf("secret with a pipe and no --stdin: %s", out)
	}
	if out, _, _ := runSSO(t, h, "", "set", "--provider", "oidc", "--secret-stdin", "--json"); codeOf(t, out) != "E_INVALID_INPUT" {
		t.Fatalf("set with a pipe and questions left: %s", out)
	}

	// An AI app at the keys, or no terminal at all: people only.
	t.Setenv("CLAUDECODE", "1")
	for _, args := range [][]string{append(setArgs, "--secret-stdin", "--json"), {"secret", "--stdin", "--json"}, {"discard", "--json"}, {"test", "--json"}} {
		if out, _, _ := runSSO(t, h, secret, args...); codeOf(t, out) != "E_HUMAN_ONLY" {
			t.Fatalf("an AI app running %v: %s", args, out)
		}
	}
	t.Setenv("CLAUDECODE", "")
	atTerminal = func() bool { return false }
	if out, _, _ := runSSO(t, h, secret, "secret", "--stdin", "--json"); codeOf(t, out) != "E_HUMAN_ONLY" {
		t.Fatalf("no terminal anywhere: %s", out)
	}
	atTerminal = func() bool { return true }

	// At a terminal the secret is read without echo.
	stdinIsTerminal = func() bool { return true }
	typed := sso.Random(24)
	asked := ""
	readHidden = func(prompt string) (string, error) { asked = prompt; return typed, nil }
	out, errOut, err = runSSO(t, h, "", "secret", "--json")
	if err != nil || codeOf(t, out) != "" || !strings.Contains(asked, "client secret") {
		t.Fatalf("secret at a terminal: %v %q\n%s\n%s", err, asked, out, errOut)
	}
	if s, _, _ := auth.SSONextSecret(cfg, app); s != typed || strings.Contains(out+errOut, typed) {
		t.Fatal("the hidden secret")
	}

	// Forgetting the setup that waits takes its secret along.
	out, errOut, err = runSSO(t, h, "", "discard", "--json")
	if err != nil || codeOf(t, out) != "" {
		t.Fatalf("discard: %v\n%s\n%s", err, out, errOut)
	}
	if s, _, _ := auth.SSONextSecret(cfg, app); s != "" {
		t.Fatal("the forgotten setup's secret stayed")
	}
}
