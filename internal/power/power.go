// Package power reads this machine's battery state and decides whether heavy jobs
// (transcription, final renders, mezzanines) should wait. Heavy jobs run on
// battery by default; they are deferred only when the machine is on battery, not
// charging, and below worker.heavyBatteryThreshold percent (0 = never defer).
package power

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FakeEnv overrides the battery reading. It is a testing knob, documented as
// such: "18,discharging", "40,charging", "100,charged", or "ac" (no battery).
const FakeEnv = "METARUSH_FAKE_BATTERY"

// DefaultThreshold is the default worker.heavyBatteryThreshold (percent).
const DefaultThreshold = 25

// Battery is a snapshot of the power state.
type Battery struct {
	Present  bool   `json:"present"`  // the machine has a battery
	Percent  int    `json:"percent"`  // 0-100 when Present
	Charging bool   `json:"charging"` // actively charging
	OnAC     bool   `json:"onAc"`     // external power is connected (charging, charged or "not charging")
	Source   string `json:"source"`   // pmset | sysfs | fake | none
}

// OnBattery reports running from the battery (no external power).
func (b Battery) OnBattery() bool { return b.Present && !b.OnAC }

// DeferHeavy applies the policy: defer heavy jobs only when on battery, not
// charging, and below the threshold. threshold <= 0 never defers.
func DeferHeavy(b Battery, threshold int) (bool, string) {
	if threshold <= 0 || !b.Present || b.OnAC || b.Charging || b.Percent >= threshold {
		return false, ""
	}
	return true, fmt.Sprintf("battery at %d%%, below the %d%% set for big jobs", b.Percent, threshold)
}

var (
	mu       sync.Mutex
	cached   Battery
	cachedAt time.Time
)

// Read returns the current battery state (cached for a few seconds; the fake
// override is read every time).
func Read() Battery {
	if v, ok := os.LookupEnv(FakeEnv); ok && strings.TrimSpace(v) != "" {
		return ParseFake(v)
	}
	mu.Lock()
	defer mu.Unlock()
	if time.Since(cachedAt) < 10*time.Second && !cachedAt.IsZero() {
		return cached
	}
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("/usr/bin/pmset", "-g", "batt").Output()
		if err == nil {
			cached = ParsePmset(string(out))
		} else {
			cached = Battery{Source: "none"}
		}
	case "linux":
		cached = ReadSysfs("/sys/class/power_supply")
	default:
		cached = Battery{Source: "none"}
	}
	cachedAt = time.Now()
	return cached
}

// ParseFake parses the FakeEnv value.
func ParseFake(v string) Battery {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "ac" || v == "none" || v == "desktop" {
		return Battery{Source: "fake", OnAC: true}
	}
	pct, state, _ := strings.Cut(v, ",")
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(pct), "%"))
	if err != nil {
		return Battery{Source: "fake", OnAC: true}
	}
	b := Battery{Present: true, Percent: clamp(n), Source: "fake"}
	switch strings.TrimSpace(state) {
	case "charging":
		b.Charging, b.OnAC = true, true
	case "charged", "full", "ac", "not charging", "notcharging":
		b.OnAC = true
	}
	return b
}

var pmsetLine = regexp.MustCompile(`(\d{1,3})%;\s*([^;]+);`)

// ParsePmset parses `pmset -g batt` output.
func ParsePmset(out string) Battery {
	b := Battery{Source: "pmset", OnAC: strings.Contains(out, "'AC Power'")}
	m := pmsetLine.FindStringSubmatch(out)
	if m == nil {
		b.OnAC = true // no battery: a desktop on mains
		return b
	}
	n, _ := strconv.Atoi(m[1])
	b.Present, b.Percent = true, clamp(n)
	state := strings.ToLower(strings.TrimSpace(m[2]))
	if state == "charging" || strings.Contains(state, "finishing charge") {
		b.Charging, b.OnAC = true, true
	}
	return b
}

// ReadSysfs reads Linux /sys/class/power_supply (BAT* and mains supplies).
func ReadSysfs(root string) Battery {
	b := Battery{Source: "sysfs"}
	entries, err := os.ReadDir(root)
	if err != nil {
		b.Source, b.OnAC = "none", true
		return b
	}
	mains := false
	read := func(dir, f string) string {
		v, _ := os.ReadFile(filepath.Join(root, dir, f))
		return strings.TrimSpace(string(v))
	}
	for _, e := range entries {
		name := e.Name()
		switch typ := read(name, "type"); {
		case typ == "Mains" || strings.HasPrefix(name, "AC") || strings.HasPrefix(name, "ADP"):
			mains = mains || read(name, "online") == "1"
		case typ == "Battery" || strings.HasPrefix(name, "BAT"):
			if b.Present {
				continue // first battery is enough
			}
			n, err := strconv.Atoi(read(name, "capacity"))
			if err != nil {
				continue
			}
			b.Present, b.Percent = true, clamp(n)
			switch read(name, "status") {
			case "Charging":
				b.Charging, b.OnAC = true, true
			case "Full", "Not charging":
				b.OnAC = true
			}
		}
	}
	if mains || !b.Present {
		b.OnAC = true
	}
	return b
}

func clamp(n int) int {
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
}
