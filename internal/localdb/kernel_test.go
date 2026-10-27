package localdb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/tools"
)

// Kernels as computers report them (uname -r, uname -v, /proc/version_signature).
var (
	ubuntuBroken = Kernel{Release: "7.0.0-27-generic", Version: "#27-Ubuntu SMP PREEMPT_DYNAMIC Thu Jul  9 10:01:12 UTC 2026",
		Signature: "Ubuntu 7.0.0-27.27-generic 7.0.11"} // built from 7.0.11: the problem is there
	ubuntuFixed = Kernel{Release: "7.0.0-38-generic", Version: "#38-Ubuntu SMP PREEMPT_DYNAMIC Fri Sep  4 09:10:14 UTC 2026",
		Signature: "Ubuntu 7.0.0-38.38-generic 7.0.14"} // built from 7.0.14: fixed, still called 7.0.0
	fedora619 = Kernel{Release: "6.19.14-200.fc43.x86_64", Version: "#1 SMP PREEMPT_DYNAMIC"}
	ubuntuGA  = Kernel{Release: "6.8.0-134-generic", Version: "#134-Ubuntu SMP", Signature: "Ubuntu 6.8.0-134.134-generic 6.8.12"}
	arch713   = Kernel{Release: "7.0.13-arch1-1", Version: "#1 SMP PREEMPT_DYNAMIC"}
	arch714   = Kernel{Release: "7.0.14-arch1-1", Version: "#1 SMP PREEMPT_DYNAMIC"}
	gcp617    = Kernel{Release: "6.17.0-1022-gcp", Version: "#22~24.04.1-Ubuntu SMP", Signature: "Ubuntu 6.17.0-1022.25-gcp 6.17.13"}
	newest    = Kernel{Release: "7.1.3", Version: "#1 SMP"}
)

// MongoDB reads the first three numbers of the release like strtol: what
// follows them is ignored, and a release without three numbers isn't judged.
func TestKernelNumbers(t *testing.T) {
	for _, c := range []struct {
		release string
		want    [3]int
		ok      bool
	}{
		{"7.0.0-38-generic", [3]int{7, 0, 0}, true},
		{"6.19.14-200.fc43.x86_64", [3]int{6, 19, 14}, true},
		{"7.0.14", [3]int{7, 0, 14}, true},
		{"  6.17.0-1022-gcp", [3]int{6, 17, 0}, true},
		{"7.1.3+", [3]int{7, 1, 3}, true},
		{"6.19-rc1", [3]int{}, false},
		{"6.19", [3]int{}, false},
		{"linux", [3]int{}, false},
		{"", [3]int{}, false},
	} {
		got, ok := kernelNumbers(c.release)
		if ok != c.ok || ok && got != c.want {
			t.Errorf("%q: %v %v, want %v %v", c.release, got, ok, c.want, c.ok)
		}
	}
}

// Which MongoDB runs on which kernel, as MongoDB's own table says
// (SERVER-125742) and its startup check reads it.
func TestKernelRefuses(t *testing.T) {
	for _, c := range []struct {
		version string
		kernel  Kernel
		refused bool
	}{
		// The pin: kernels 6.19 through 7.0.13 by their release.
		{"8.0.34", ubuntuGA, false},
		{"8.0.34", gcp617, false},
		{"8.0.34", fedora619, true},
		{"8.0.34", ubuntuBroken, true},
		{"8.0.34", ubuntuFixed, true}, // fixed, but called 7.0.0: 8.0.34 can't tell
		{"8.0.34", arch713, true},
		{"8.0.34", arch714, false},
		{"8.0.34", newest, false},
		{"8.0.34", Kernel{Release: "6.19-rc1"}, false}, // MongoDB doesn't judge what it can't read
		{"8.0.34", Kernel{}, false},                    // not Linux
		// 8.0.35 on reads what an Ubuntu kernel is built from.
		{"8.0.35", ubuntuFixed, false},
		{"8.0.35", ubuntuBroken, true},
		{"8.0.35", Kernel{Release: "7.0.0-38-generic", Version: "#1 SMP", Signature: ubuntuFixed.Signature}, true}, // not an Ubuntu kernel
		{"8.0.35", fedora619, true},
		// Before 8.0.30 the fixed kernels were refused too; before 8.0.21 it crashed.
		{"8.0.25", newest, true},
		{"8.0.25", ubuntuGA, false},
		{"8.0.12", fedora619, true},
		{"8.0.12", newest, false},
		// MongoDB 7.0 runs everywhere.
		{"7.0.45", fedora619, false},
		{"7.0.45", ubuntuBroken, false},
		{"7.0.45", arch713, false},
	} {
		if got := KernelRefuses(c.version, c.kernel); got != c.refused {
			t.Errorf("MongoDB %s on %q: refused %v, want %v", c.version, c.kernel.Release, got, c.refused)
		}
	}
	if !ubuntuFixed.HasTheFix() || ubuntuBroken.HasTheFix() || fedora619.HasTheFix() {
		t.Error("HasTheFix reads Ubuntu's signature")
	}
}

// Older is MongoDB's own 7.0 for every Linux computer the pin serves, one
// release before the pin (so an upgrade later is one step).
func TestOlderPinsAreMongoDBs(t *testing.T) {
	hexRe := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for target := range Current.Archives {
		if !strings.HasPrefix(target, "linux/") {
			continue
		}
		a, ok := Older.For(target)
		if !ok {
			t.Errorf("no MongoDB %s for %s", Older.Version, target)
			continue
		}
		if !strings.HasPrefix(a.URL, "https://fastdl.mongodb.org/linux/") || !strings.HasSuffix(a.URL, "-"+Older.Version+".tgz") || !hexRe.MatchString(a.SHA256) {
			t.Errorf("%s: %s %s", target, a.URL, a.SHA256)
		}
		if arch := strings.Split(target, "/")[1]; arch == "arm64" && !strings.Contains(a.URL, "aarch64") || arch == "amd64" && !strings.Contains(a.URL, "x86_64") {
			t.Errorf("%s gets %s", target, a.URL)
		}
	}
	if Series(Older.Version) != "7.0" || !nextSeries(Series(Older.Version), Series(Current.Version)) {
		t.Errorf("Older %s → Current %s isn't one step", Older.Version, Current.Version)
	}
}

// kernelServer is a home on a Linux computer with the given kernel.
func kernelServer(t *testing.T, k Kernel) *Server {
	t.Helper()
	t.Setenv(EnvPin, "")
	s := New(t.TempDir(), 0, tools.New(config.Tools{}))
	s.TargetName, s.Kernel = "linux/amd64/ubuntu2404", &k
	return s
}

// The database a new home gets: MongoDB 8 where the kernel lets it run,
// else 7.0 — said in plain words before anything downloads.
func TestThePickPerKernel(t *testing.T) {
	ok := kernelServer(t, ubuntuGA)
	if ok.PinnedVersion() != Current.Version || ok.KernelNote() != "" {
		t.Errorf("a kernel 8.0 runs on: %s, %q", ok.PinnedVersion(), ok.KernelNote())
	}
	for _, k := range []Kernel{ubuntuBroken, ubuntuFixed, fedora619} {
		s := kernelServer(t, k)
		a, has := s.pin().For(s.target())
		if s.PinnedVersion() != Older.Version || !s.Available() || !has || !strings.Contains(a.URL, "ubuntu2204-7.0.45") || s.DownloadMB() != Older.SizeMB {
			t.Errorf("%s: sets up %s from %s", k.Release, s.PinnedVersion(), a.URL)
		}
		note := s.KernelNote()
		for _, want := range []string{"MongoDB " + Current.Version + " can't start on this computer's Linux kernel (" + k.Release + ")",
			"6.19 through 7.0.13", "MongoDB " + Older.Version + " instead", "works the same for MetaRush", "upgrade"} {
			if !strings.Contains(note, want) {
				t.Errorf("%s: the note lacks %q: %s", k.Release, want, note)
			}
		}
		if f := plainwords.Check(note); len(f) > 0 {
			t.Errorf("%s: not plain: %v\n%s", k.Release, f, note)
		}
	}
	// Off Linux nothing changes.
	mac := New(t.TempDir(), 0, tools.New(config.Tools{}))
	mac.TargetName, mac.Kernel = "darwin/arm64", &Kernel{}
	if mac.PinnedVersion() != Current.Version || mac.KernelNote() != "" {
		t.Errorf("a Mac: %s %q", mac.PinnedVersion(), mac.KernelNote())
	}

	// A stand-in download (browser runs) never falls back to MongoDB's own:
	// without a stand-in for the older one, the pin stays the stand-in.
	pin := filepath.Join(t.TempDir(), "pin.json")
	write := func(body string) {
		if err := os.WriteFile(pin, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tgt, _ := Target()
	s := New(t.TempDir(), 0, tools.New(config.Tools{}))
	s.Kernel = &fedora619
	write(`{"version":"8.0.34","url":"http://127.0.0.1:1/m.tgz","sha256":"` + strings.Repeat("a", 64) + `"}`)
	t.Setenv(EnvPin, pin)
	if a, _ := s.pin().For(tgt); s.PinnedVersion() != "8.0.34" || a.URL != "http://127.0.0.1:1/m.tgz" {
		t.Errorf("a stand-in without an older one: %s %s", s.PinnedVersion(), a.URL)
	}
	write(`{"version":"8.0.34","url":"http://127.0.0.1:1/m.tgz","sha256":"` + strings.Repeat("a", 64) +
		`","older":{"version":"7.0.45","url":"http://127.0.0.1:1/older.tgz","sha256":"` + strings.Repeat("b", 64) + `"}}`)
	if a, _ := s.pin().For(tgt); s.PinnedVersion() != "7.0.45" || a.URL != "http://127.0.0.1:1/older.tgz" {
		t.Errorf("a stand-in with an older one: %s %s", s.PinnedVersion(), a.URL)
	}
	write(`{"version":"8.0.34","url":"http://127.0.0.1:1/m.tgz","sha256":"` + strings.Repeat("a", 64) +
		`","older":{"version":"7.0.45","url":"https://fastdl.mongodb.org/older.tgz","sha256":"` + strings.Repeat("b", 64) + `"}}`)
	if _, err := PinFromEnv(); err == nil {
		t.Error("an older stand-in elsewhere than this computer was accepted")
	}
}

// A database MongoDB 8 set up, on a kernel that stops MongoDB 8 (the computer
// updated its kernel since): never started, never moved back to 7.0, and
// said plainly with what to do — by Start, by the status, by an upgrade.
func TestADatabaseTheKernelStopsIsSaidPlainly(t *testing.T) {
	for _, k := range []Kernel{ubuntuBroken, ubuntuFixed} {
		s := kernelServer(t, k)
		s.Port = 27151
		if err := s.writeState(State{Version: Current.Version, FeatureVersion: "8.0", Port: s.Port, Initiated: true}); err != nil {
			t.Fatal(err)
		}
		err := s.Start(context.Background())
		var le *Error
		if !errors.Is(err, ErrKernel) || !errors.As(err, &le) {
			t.Fatalf("%s: started: %v", k.Release, err)
		}
		if _, e := os.Stat(s.pidFile()); !os.IsNotExist(e) {
			t.Errorf("%s: a server was started", k.Release)
		}
		words := le.Words()
		if f := plainwords.Check(words); len(f) > 0 {
			t.Errorf("%s: not plain: %v\n%s", k.Release, f, words)
		}
		wants := []string{"MongoDB " + Current.Version + " can't start on this computer's Linux kernel (" + k.Release + ")", "Your projects stay safe in it"}
		if k.HasTheFix() {
			wants = append(wants, "already has the fix", "built from 7.0.14", "MetaRush update")
		} else {
			wants = append(wants, "7.0.14 or later", "older than 6.19")
		}
		for _, w := range wants {
			if !strings.Contains(words, w) {
				t.Errorf("%s: lacks %q: %s", k.Release, w, words)
			}
		}
		st := s.Status(context.Background())
		if st.CantStart != words || st.Kernel != k.Release || st.UpgradeReady || st.UpgradeWaits != "" {
			t.Errorf("%s: status %+v", k.Release, st)
		}
		// Never back to 7.0: there is nothing to upgrade to.
		if _, err := s.PlanUpgrade(); err != ErrUpToDate {
			t.Errorf("%s: an upgrade plan: %v", k.Release, err)
		}
	}

	// MongoDB's own check stopped it (one MetaRush didn't foresee): its log line is read.
	s := kernelServer(t, newest)
	if err := s.writeState(State{Version: "8.0.40", Port: 27152, Initiated: true}); err != nil {
		t.Fatal(err)
	}
	line := `{"t":{"$date":"2026-10-04T19:15:33.043+00:00"},"s":"F","c":"CONTROL","id":12257600,"ctx":"main","msg":"MongoDB cannot start: Linux kernel versions 6.19 and newer has a known incompatibility with this version of MongoDB."}` + "\n"
	if err := os.WriteFile(s.LogFile(), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.whyStopped(); !errors.Is(err, ErrKernel) || !strings.Contains(err.Error(), "MongoDB 8.0.40 can't start on this computer's Linux kernel (7.1.3)") {
		t.Errorf("MongoDB's own refusal: %v", err)
	}
}

// A database set up with 7.0 because of the kernel stays at 7.0 until the
// kernel lets MongoDB 8 run, then moves on in one step.
func TestUpgradeWaitsForTheKernel(t *testing.T) {
	s := kernelServer(t, ubuntuBroken)
	if err := s.writeState(State{Version: Older.Version, FeatureVersion: "7.0", Port: 27153, Initiated: true}); err != nil {
		t.Fatal(err)
	}
	_, err := s.PlanUpgrade()
	var le *Error
	if !errors.Is(err, ErrKernel) || !errors.As(err, &le) || !strings.Contains(le.Plain, "stays at MongoDB "+Older.Version) {
		t.Fatalf("an upgrade to 8.0 on a kernel that stops it: %v", err)
	}
	st := s.Status(context.Background())
	if st.UpgradeReady || st.UpgradeWaits == "" || st.CantStart != "" || st.Kernel != ubuntuBroken.Release {
		t.Errorf("status: %+v", st)
	}
	if f := plainwords.Check(st.UpgradeWaits); len(f) > 0 {
		t.Errorf("not plain: %v\n%s", f, st.UpgradeWaits)
	}
	// The kernel was updated: one step on to 8.0.
	s.Kernel = &arch714
	step, err := s.PlanUpgrade()
	if err != nil || step.From != Older.Version || step.To != Current.Version || !step.SeriesChange {
		t.Fatalf("after the kernel update: %+v %v", step, err)
	}
	if st := s.Status(context.Background()); !st.UpgradeReady || st.UpgradeWaits != "" {
		t.Errorf("status after the kernel update: %+v", st)
	}
}
