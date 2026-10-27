package power

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeferHeavyPolicyMatrix(t *testing.T) {
	cases := []struct {
		name      string
		b         Battery
		threshold int
		defer_    bool
	}{
		{"desktop on mains", Battery{OnAC: true}, 25, false},
		{"plugged in, charged", Battery{Present: true, Percent: 100, OnAC: true}, 25, false},
		{"plugged in, low but charging", Battery{Present: true, Percent: 5, OnAC: true, Charging: true}, 25, false},
		{"plugged in, low, not charging", Battery{Present: true, Percent: 5, OnAC: true}, 25, false},
		{"battery above threshold", Battery{Present: true, Percent: 60}, 25, false},
		{"battery exactly at threshold", Battery{Present: true, Percent: 25}, 25, false},
		{"battery below threshold", Battery{Present: true, Percent: 18}, 25, true},
		{"threshold 0 never defers", Battery{Present: true, Percent: 3}, 0, false},
		{"higher threshold", Battery{Present: true, Percent: 45}, 50, true},
	}
	for _, c := range cases {
		got, reason := DeferHeavy(c.b, c.threshold)
		if got != c.defer_ {
			t.Errorf("%s: defer=%v, want %v", c.name, got, c.defer_)
		}
		if got && reason == "" {
			t.Errorf("%s: deferring without a reason", c.name)
		}
	}
	if _, r := DeferHeavy(Battery{Present: true, Percent: 18}, 25); r != "battery at 18%, below the 25% set for big jobs" {
		t.Errorf("reason %q", r)
	}
}

func TestParsePmset(t *testing.T) {
	cases := []struct {
		out  string
		want Battery
	}{
		{"Now drawing from 'Battery Power'\n -InternalBattery-0 (id=4653155)\t18%; discharging; 1:12 remaining present: true\n",
			Battery{Present: true, Percent: 18, Source: "pmset"}},
		{"Now drawing from 'AC Power'\n -InternalBattery-0 (id=4653155)\t76%; charging; 0:52 remaining present: true\n",
			Battery{Present: true, Percent: 76, Charging: true, OnAC: true, Source: "pmset"}},
		{"Now drawing from 'AC Power'\n -InternalBattery-0 (id=4653155)\t80%; AC attached; not charging present: true\n",
			Battery{Present: true, Percent: 80, OnAC: true, Source: "pmset"}},
		{"Now drawing from 'AC Power'\n -InternalBattery-0 (id=4653155)\t100%; charged; 0:00 remaining present: true\n",
			Battery{Present: true, Percent: 100, OnAC: true, Source: "pmset"}},
		{"Now drawing from 'AC Power'\n", Battery{OnAC: true, Source: "pmset"}},
	}
	for _, c := range cases {
		if got := ParsePmset(c.out); got != c.want {
			t.Errorf("ParsePmset(%q) = %+v, want %+v", c.out, got, c.want)
		}
	}
}

func TestReadSysfs(t *testing.T) {
	root := t.TempDir()
	write := func(dir, file, v string) {
		_ = os.MkdirAll(filepath.Join(root, dir), 0o755)
		_ = os.WriteFile(filepath.Join(root, dir, file), []byte(v+"\n"), 0o644)
	}
	write("BAT0", "type", "Battery")
	write("BAT0", "capacity", "18")
	write("BAT0", "status", "Discharging")
	write("AC", "type", "Mains")
	write("AC", "online", "0")
	if got := ReadSysfs(root); !got.Present || got.Percent != 18 || got.OnAC || got.Charging {
		t.Fatalf("discharging laptop: %+v", got)
	}
	write("AC", "online", "1")
	write("BAT0", "status", "Charging")
	if got := ReadSysfs(root); !got.OnAC || !got.Charging {
		t.Fatalf("charging laptop: %+v", got)
	}
	if got := ReadSysfs(filepath.Join(root, "missing")); got.Present || !got.OnAC {
		t.Fatalf("no power_supply dir: %+v", got)
	}
}

func TestFakeBattery(t *testing.T) {
	t.Setenv(FakeEnv, "18,discharging")
	if b := Read(); !b.Present || b.Percent != 18 || b.OnAC || b.Charging || b.Source != "fake" {
		t.Fatalf("fake discharging: %+v", b)
	}
	t.Setenv(FakeEnv, "18,charging")
	if b := Read(); !b.Charging || !b.OnAC {
		t.Fatalf("fake charging: %+v", b)
	}
	t.Setenv(FakeEnv, "ac")
	if b := Read(); b.Present || !b.OnAC {
		t.Fatalf("fake ac: %+v", b)
	}
}
