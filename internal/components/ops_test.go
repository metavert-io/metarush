package components_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/components"
	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/testutil/fakebrew"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

func call[T any](t *testing.T, a *app.App, name string, in any, c ops.Caller) (*T, error) {
	t.Helper()
	b, _ := json.Marshal(in)
	out, err := ops.Call(context.Background(), a, name, b, c)
	if err != nil {
		return nil, err
	}
	return out.(*T), nil
}

// A computer with only a stand-in Homebrew and what it installs.
func brewApp(t *testing.T) (*app.App, string) {
	t.Helper()
	brew, bin, log := fakebrew.Write(t)
	t.Setenv(tools.EnvToolDirs, bin)
	a := testapp.New(t)
	if err := config.SetField(a.Cfg.Home(), []string{"tools", "brew"}, brew); err != nil {
		t.Fatal(err)
	}
	c := a.Cfg.Tools
	c.Brew = brew
	a.Tools.Rescan(c)
	return a, log
}

// Installing programs is a person's: never Claude in the dashboard, an AI
// app that only proposes, or a call no person confirmed (D327). Listing is
// for anyone.
func TestAddingIsAPersonsDecision(t *testing.T) {
	a, log := brewApp(t)
	list, err := call[components.ComponentsOut](t, a, "components.list", components.ComponentsIn{}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Components) < 20 || !list.Homebrew || list.Missing == 0 || list.Summary == "" {
		t.Fatalf("list: %d components, homebrew %v, missing %d, %q", len(list.Components), list.Homebrew, list.Missing, list.Summary)
	}
	for _, name := range []string{"components.add", "components.remove"} {
		op, _ := ops.Get(name)
		if !op.HumanOnly || !op.OwnerOnly || !op.Mutating {
			t.Errorf("%s: %+v", name, op)
		}
	}
	if _, err := call[components.ComponentAddOut](t, a, "components.add", components.ComponentAddIn{Name: "pdf-pages"}, ops.Caller{}); !apperr.Is(err, apperr.HumanOnly) {
		t.Fatalf("no person: %v", err)
	}
	claude := *a
	claude.Actor.Via = models.ViaAssistant
	if _, err := call[components.ComponentAddOut](t, &claude, "components.add", components.ComponentAddIn{Name: "pdf-pages"}, ops.Caller{Human: true}); !apperr.Is(err, apperr.Unauthorized) {
		t.Fatalf("Claude in the dashboard: %v", err)
	}
	propose := *a
	propose.ProposeOnly = true
	if _, err := call[components.ComponentAddOut](t, &propose, "components.add", components.ComponentAddIn{Name: "pdf-pages"}, ops.Caller{Human: true}); err == nil {
		t.Fatal("an AI app that only proposes added a component")
	}
	if calls := fakebrew.Calls(t, log); len(calls) != 0 {
		t.Fatalf("Homebrew ran: %v", calls)
	}
	out, err := call[components.ComponentAddOut](t, a, "components.add", components.ComponentAddIn{Name: "pdf-pages"}, ops.Caller{Human: true})
	if err != nil || out.Added == nil || !out.Added.Done || !out.Component.Ready || out.Component.Status != components.StatusReady {
		t.Fatalf("a person: %+v %v", out, err)
	}
	if !a.Tools.Has(tools.Pdftoppm) {
		t.Fatal("the running MetaRush doesn't see what was added")
	}
}

// The dashboard starts an add and goes on; the list says how far along it is
// and when it is ready, and a renderer added says Remotion's licence.
func TestAddInTheBackground(t *testing.T) {
	a, _ := brewApp(t)
	changed := make(chan string, 4)
	components.OnChanged = func(*app.App) { changed <- "x" }
	defer func() { components.OnChanged = nil }()
	out, err := call[components.ComponentAddOut](t, a, "components.add", components.ComponentAddIn{Name: "ffmpeg", Background: true}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Started && !out.Component.Ready {
		t.Fatalf("started: %+v", out)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		l, err := call[components.ComponentsOut](t, a, "components.list", components.ComponentsIn{Name: "ffmpeg"}, ops.Caller{})
		if err != nil {
			t.Fatal(err)
		}
		if l.Components[0].Ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never ready: %+v", l.Components[0])
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("the running MetaRush wasn't told")
	}
	for p := filepath.Join(a.Cfg.Home(), "state", "components", "ffmpeg.adding.json"); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the progress file outlived the add")
		}
	}
}

// A component added in a terminal (or by Homebrew outside MetaRush) is seen
// by the running MetaRush: the list looks again for programs, and the
// watcher looks again when config.yaml or the choices change, telling the
// background work (D324).
func TestTheRunningMetaRushSeesTerminalAdds(t *testing.T) {
	bin := t.TempDir()
	t.Setenv(tools.EnvToolDirs, bin)
	a := testapp.New(t)
	ctx := context.Background()
	if a.Tools.Has(tools.FFmpeg) {
		t.Fatal("a bare computer has FFmpeg")
	}
	for _, p := range []string{"ffmpeg", "ffprobe"} {
		if err := os.WriteFile(filepath.Join(bin, p), []byte("#!/bin/sh\necho 'version 7'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	l, err := call[components.ComponentsOut](t, a, "components.list", components.ComponentsIn{Name: "ffmpeg"}, ops.Caller{})
	if err != nil || !l.Components[0].Ready {
		t.Fatalf("the list doesn't see FFmpeg added in a terminal: %+v %v", l, err)
	}
	// The watcher: a choice recorded in a terminal (pdf-pages added by Homebrew).
	told := make(chan struct{}, 4)
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go components.Watch(wctx, a, 50*time.Millisecond, func() { told <- struct{}{} })
	time.Sleep(150 * time.Millisecond)
	for _, p := range []string{"pdftoppm", "pdfinfo"} {
		if err := os.WriteFile(filepath.Join(bin, p), []byte("#!/bin/sh\necho 'poppler 25'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := choice.Set(a.Cfg.Home(), map[string]bool{"pdf-pages": true}, "Sam"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-told:
	case <-time.After(5 * time.Second):
		t.Fatal("the running MetaRush wasn't told about a component added in a terminal")
	}
	if !a.Tools.Has(tools.Pdftoppm) {
		t.Fatal("the running MetaRush doesn't see poppler")
	}
}
