package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/sound"
	"github.com/metavert-io/metarush/internal/tools"
)

// doctor says whether clean and studio are set up on this computer, in plain
// words, with studio's measured speed; versions and folders are behind
// --details (and in --json).
func TestDoctorSaysWhatMakesVoicesClearer(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default(home)
	cfg.SetHome(home)
	comps := filepath.Join(home, "components")
	put := func(rel string, mode os.FileMode, body string) {
		t.Helper()
		p := filepath.Join(comps, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	run := func() (map[string]Check, string, string) {
		t.Helper()
		var checks []Check
		add := func(area, name, status, detail, fix string, plain ...string) {
			ch := Check{Area: area, Name: name, Status: status, Detail: detail, Fix: fix}
			if len(plain) > 0 {
				ch.Say = plain[0]
			}
			if len(plain) > 1 {
				ch.Do = plain[1]
			}
			checks = append(checks, ch)
		}
		soundChecks(cfg, tools.New(cfg.Tools), add)
		by := map[string]Check{}
		for _, c := range checks {
			if c.Area != "clearer voices" {
				t.Errorf("area %q", c.Area)
			}
			by[c.Name] = c
		}
		plain := stdout(t, func() { printChecks(checks, false) })
		setupPlain(t, "doctor's clearer voices", plain)
		return by, plain, stdout(t, func() { printChecks(checks, true) })
	}
	comp := &sound.Components{Home: home}
	cleanAvailable := comp.CleanAvailable()

	// Nothing set up yet.
	by, plain, _ := run()
	if c := by["clean"]; c.Status != "info" || c.Fix != "metarush sound setup clean" ||
		(cleanAvailable && !strings.Contains(c.Say, "sets itself up the first time a voice is made clearer (about 30 MB)")) {
		t.Errorf("clean, not set up: %+v", c)
	}
	if c := by["studio"]; c.Status != "info" || !strings.Contains(c.Say, "Studio isn't set up") || !strings.Contains(c.Say, "about 1.8 GB") ||
		c.Do != "Set it up on the Sound tab if you want it." || c.Fix != "metarush sound setup studio" {
		t.Errorf("studio, not set up: %+v", c)
	}
	if !strings.Contains(plain, "CLEARER VOICES") || !strings.Contains(plain, "→ Set it up on the Sound tab if you want it.") {
		t.Errorf("plain:\n%s", plain)
	}

	// Both set up: studio measured 0.41 s of work per second of audio on the graphics chip.
	if cleanAvailable {
		put("voice-cleaner/deep-filter-"+sound.DeepFilterVersion, 0o755, "#!/bin/sh\n")
		put("voice-score/sig_bak_ovr.onnx", 0o644, "model")
	}
	put("studio-voice/venv/bin/python", 0o755, "#!/bin/sh\n")
	for _, f := range []string{"enhancer_stage2/hparams.yaml", "enhancer_stage2/ds/G/latest", "enhancer_stage2/ds/G/default/mp_rank_00_model_states.pt"} {
		put("studio-voice/model/"+f, 0o644, "x")
	}
	man, _ := json.Marshal(map[string]any{"version": sound.StudioVersion, "python": "3.13", "torch": "2.11.0", "device": "mps", "speed": 0.41, "measuredOn": 6.0, "at": "2026-10-19T10:00:00Z"})
	put("studio-voice/component.json", 0o644, string(man))
	by, plain, detailed := run()
	if cleanAvailable {
		if c := by["clean"]; c.Status != "ok" || c.Say != "Clean is set up: voices are made clearer on this computer, fast and free" {
			t.Errorf("clean, set up: %+v", c)
		}
	}
	if c := by["studio"]; c.Status != "ok" || c.Say != "Studio is set up and uses the graphics chip: about 1.6 seconds of work for each second of speech (measured when it was set up)" {
		t.Errorf("studio, set up: %+v", c)
	}
	for _, want := range []string{"DeepFilterNet " + sound.DeepFilterVersion, sound.DNSMOSVersion, sound.StudioVersion, "0.41 s of work per second of audio", "mps"} {
		if !cleanAvailable && strings.Contains(want, "DeepFilterNet") {
			continue
		}
		if !strings.Contains(detailed, want) {
			t.Errorf("--details lacks %q:\n%s", want, detailed)
		}
		if strings.Contains(plain, want) {
			t.Errorf("plain shows %q:\n%s", want, plain)
		}
	}

	// Set up in part (the model went missing), then for an earlier MetaRush.
	_ = os.Remove(filepath.Join(comps, "studio-voice", "model", "enhancer_stage2", "hparams.yaml"))
	if c := run1(t, run)["studio"]; c.Status != "warn" || c.Say != "Studio is set up only in part, so it can't be used" || c.Do != "Set it up again on the Sound tab." {
		t.Errorf("studio, in part: %+v", c)
	}
	old, _ := json.Marshal(map[string]any{"version": "resemble-enhance-0.0.1+model-old", "device": "cpu", "speed": 1.2})
	put("studio-voice/component.json", 0o644, string(old))
	if c := run1(t, run)["studio"]; c.Status != "warn" || !strings.Contains(c.Say, "earlier version of MetaRush") || !strings.Contains(c.Detail, "resemble-enhance-0.0.1+model-old") {
		t.Errorf("studio, set up for an earlier MetaRush: %+v", c)
	}
}

func run1(t *testing.T, run func() (map[string]Check, string, string)) map[string]Check {
	t.Helper()
	by, _, _ := run()
	return by
}
