package sound

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// fakeStudioEnv makes this test binary, re-executed, a fake voice cleaner
// (deep-filter: copies its input) or a fake studio runner (the component's
// Python: copies each job's input to its output), counting its starts in the
// folder the variable names.
const fakeStudioEnv = "METARUSH_TEST_FAKE_STUDIO"

func TestMain(m *testing.M) {
	if dir := os.Getenv(fakeStudioEnv); dir != "" && len(os.Args) > 1 {
		switch os.Args[1] {
		case "-I":
			fakeStudioRunner(dir, os.Args[2:])
			os.Exit(0)
		case "-D", "--version":
			fakeVoiceCleaner(dir, os.Args[1:])
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func countStart(dir, name string) {
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = f.WriteString("start\n")
		_ = f.Close()
	}
}

func copyFile(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, b, 0o600)
}

func fakeVoiceCleaner(dir string, args []string) {
	if args[0] == "--version" {
		fmt.Println("deep-filter " + DeepFilterVersion)
		return
	}
	countStart(dir, "cleaner")
	// -D -o <out dir> <in.wav>
	out, in := args[2], args[3]
	_ = os.MkdirAll(out, 0o755)
	if err := copyFile(in, filepath.Join(out, filepath.Base(in))); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func fakeStudioRunner(dir string, args []string) {
	countStart(dir, "runner")
	var jobsFile, device string
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--jobs":
			jobsFile = args[i+1]
		case "--device":
			device = args[i+1] // the last one counts, as with argparse
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "device"), []byte(device), 0o600)
	b, err := os.ReadFile(jobsFile)
	if err != nil {
		os.Exit(2)
	}
	var jobs []studioJob
	_ = json.Unmarshal(b, &jobs)
	fmt.Println(`{"type": "ready", "device": "cpu", "torch": "fake", "load_seconds": 0}`)
	for i, j := range jobs {
		if err := copyFile(j.In, j.Out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf(`{"type": "done", "index": %d, "device": "cpu", "torch": "fake", "seconds": 0.01, "audio_seconds": 1}`+"\n", i)
	}
}

// fakeComponents sets up clean and studio in a temporary home whose programs
// are this test binary.
func fakeComponents(t *testing.T) (*Components, string) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	self, err := os.Executable()
	if err != nil {
		t.Skip("cannot find the test binary")
	}
	counts := t.TempDir()
	t.Setenv(fakeStudioEnv, counts)
	c := &Components{Home: t.TempDir(), Tools: tools.New(config.Tools{})}
	link := func(target, p string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p string, b []byte) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link(self, c.cleanBinary())
	write(c.scoreModel(), []byte("model"))
	link(self, c.studioPython())
	for _, f := range studioModelFiles {
		write(filepath.Join(c.studioDir(), "model", f.path), []byte("model"))
	}
	man, _ := json.Marshal(studioManifest{Version: StudioVersion, Device: "cpu", Speed: 0.5})
	write(filepath.Join(c.studioDir(), "component.json"), man)
	if ok, why := c.Ready("studio"); !ok {
		t.Fatal(why)
	}
	return c, counts
}

func starts(t *testing.T, dir, name string) int {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	for sc := bufio.NewScanner(f); sc.Scan(); {
		n++
	}
	return n
}

// The studio voice loads its model once for every recording a run makes
// clearer (it used to load once per recording).
func TestStudioOneModelLoadAcrossRecordings(t *testing.T) {
	c, counts := fakeComponents(t)
	eng := &Engine{Tools: c.Tools, Comp: c, Scratch: t.TempDir()}
	var recs []string
	for i := 0; i < 2; i++ {
		p := filepath.Join(t.TempDir(), fmt.Sprintf("rec-%d.wav", i))
		if err := WriteWAV(p, &Audio{Rate: WetRate, Channels: 1, Data: testVoice(3, WetRate)}, true); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, p)
	}
	parts := []StudioPart{{Src: recs[0], From: 0, To: 1.5}, {Src: recs[0], From: 1.5, To: 3}, {Src: recs[1], From: 0.5, To: 2.5}}
	var progress []int
	ws, err := eng.StudioMany(context.Background(), parts, func(n int) { progress = append(progress, n) })
	if err != nil {
		t.Fatal(err)
	}
	if n := starts(t, counts, "runner"); n != 1 {
		t.Fatalf("the studio voice started %d times for 3 parts of 2 recordings; want once", n)
	}
	if n := starts(t, counts, "cleaner"); n != 3 {
		t.Fatalf("the voice cleaner ran %d times, want 3", n)
	}
	if fmt.Sprint(progress) != "[1 2 3]" {
		t.Fatalf("progress %v", progress)
	}
	if b, _ := os.ReadFile(filepath.Join(counts, "device")); string(b) != "auto" {
		t.Fatalf("device %q: the runner picks the graphics card itself", b)
	}
	for i, w := range ws {
		want := (parts[i].To - parts[i].From) * WetRate
		if w == nil || w.Audio == nil || math.Abs(float64(w.Audio.Frames())-want) > 0.01*want || w.Device != "cpu" {
			t.Fatalf("part %d: %+v", i, w)
		}
	}
}

// The torch build the studio voice installs: Metal on Apple silicon, none on
// Intel Macs, CUDA 12.8 or 12.6 for NVIDIA cards with a driver new enough,
// the CPU build otherwise.
func TestPickTorch(t *testing.T) {
	ampere := nvidiaGPU{Name: "NVIDIA GeForce RTX 3090", Compute: "8.6", Driver: "550.54.14"}
	for _, c := range []struct {
		name, goos, goarch string
		gpus               []nvidiaGPU
		build, device      string
		note, err          bool
	}{
		{"Apple silicon", "darwin", "arm64", nil, "pypi", "mps", false, false},
		{"Intel Mac", "darwin", "amd64", nil, "", "", false, true},
		{"Linux without a card", "linux", "amd64", nil, "cpu", "cpu", false, false},
		{"Ampere", "linux", "amd64", []nvidiaGPU{ampere}, "cu128", "cuda", false, false},
		{"Blackwell", "linux", "amd64", []nvidiaGPU{{Name: "NVIDIA GeForce RTX 5090", Compute: "12.0", Driver: "575.51.03"}}, "cu128", "cuda", false, false},
		{"Volta", "linux", "amd64", []nvidiaGPU{{Name: "Tesla V100", Compute: "7.0", Driver: "535.104.05"}}, "cu128", "cuda", false, false},
		{"Pascal", "linux", "amd64", []nvidiaGPU{{Name: "GeForce GTX 1080", Compute: "6.1", Driver: "535.104.05"}}, "cu126", "cuda", false, false},
		{"Pascal next to Ampere", "linux", "amd64", []nvidiaGPU{ampere, {Name: "GeForce GTX 1080", Compute: "6.1", Driver: "550.54.14"}}, "cu126", "cuda", false, false},
		{"Kepler", "linux", "amd64", []nvidiaGPU{{Name: "Tesla K80", Compute: "3.7", Driver: "535.104.05"}}, "cpu", "cpu", true, false},
		{"driver too old", "linux", "amd64", []nvidiaGPU{{Name: "GeForce RTX 2080", Compute: "7.5", Driver: "470.256.02"}}, "cpu", "cpu", true, false},
		{"driver 525.60 exactly", "linux", "amd64", []nvidiaGPU{{Name: "A100", Compute: "8.0", Driver: "525.60.13"}}, "cu128", "cuda", false, false},
		{"card unknown, driver known", "linux", "amd64", []nvidiaGPU{{Driver: "550.54.14"}}, "cu128", "cuda", false, false},
		{"Grace Hopper", "linux", "arm64", []nvidiaGPU{{Name: "NVIDIA GH200", Compute: "9.0", Driver: "560.35.03"}}, "cu128", "cuda", false, false},
		{"Linux arm64 without a card", "linux", "arm64", nil, "cpu", "cpu", false, false},
		{"Windows", "windows", "amd64", nil, "", "", false, true},
	} {
		b, err := pickTorch(c.goos, c.goarch, c.gpus)
		if (err != nil) != c.err || b.Name != c.build || b.Device != c.device || (b.Note != "") != c.note {
			t.Errorf("%s: %+v, %v", c.name, b, err)
			continue
		}
		switch {
		case b.Name == "pypi" && b.Index != "":
			t.Errorf("%s: Apple silicon installs PyPI's torch", c.name)
		case b.Name != "pypi" && b.Name != "" && b.Index != torchIndex+b.Name:
			t.Errorf("%s: index %q", c.name, b.Index)
		}
	}
	if got := strings.Join(torchPins(torchBuild{Name: "cu128"}), " "); got != "torch==2.11.0+cu128 torchaudio==2.11.0+cu128" {
		t.Fatalf("pins %s", got)
	}
}

// nvidia-smi's answers and the driver's version file.
func TestReadNvidiaCards(t *testing.T) {
	gs := parseNvidiaSMI("NVIDIA GeForce RTX 3090, 8.6, 550.54.14\nTesla K80, 3.7, 470.256.02\n\n")
	if len(gs) != 2 || gs[0] != (nvidiaGPU{Name: "NVIDIA GeForce RTX 3090", Compute: "8.6", Driver: "550.54.14"}) || gs[1].Compute != "3.7" {
		t.Fatalf("%+v", gs)
	}
	if gs := parseNvidiaSMI("NVIDIA GeForce GTX 1080, 470.256.02\n"); len(gs) != 1 || gs[0].Compute != "" || gs[0].Driver != "470.256.02" {
		t.Fatalf("without compute_cap: %+v", gs)
	}
	if gs := parseNvidiaSMI("Field \"compute_cap\" is not a valid field to query.\n"); len(gs) != 0 {
		t.Fatalf("an error is no card: %+v", gs)
	}
	for text, want := range map[string]string{
		"NVRM version: NVIDIA UNIX x86_64 Kernel Module  550.54.14  Thu Feb 22 01:44:30 UTC 2024\nGCC version:  gcc version 12.2.0\n": "550.54.14",
		"NVRM version: NVIDIA UNIX Open Kernel Module for x86_64  570.86.15  Release Build  (dvs-builder@U16-I3-B03-4-3)\n":           "570.86.15",
		"no driver here\n": "",
	} {
		if got := nvidiaDriverVersion(text); got != want {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
	// On Linux: nvidia-smi first, else the driver's file.
	c := &Components{Tools: tools.New(config.Tools{})}
	dir := t.TempDir()
	smi := filepath.Join(dir, "nvidia-smi")
	if err := os.WriteFile(smi, []byte("#!/bin/sh\ncase \"$1\" in --query-gpu=name,compute_cap,driver_version) echo 'NVIDIA A100-SXM4-40GB, 8.0, 535.104.05';; *) exit 2;; esac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !c.Tools.Use(tools.NvidiaSMI, smi) {
		t.Fatal("use")
	}
	old := nvidiaProcFile
	nvidiaProcFile = filepath.Join(dir, "version")
	t.Cleanup(func() { nvidiaProcFile = old })
	gs = c.nvidiaCards(context.Background())
	if len(gs) != 1 || gs[0].Compute != "8.0" {
		t.Fatalf("from nvidia-smi: %+v", gs)
	}
	if err := os.WriteFile(smi, []byte("#!/bin/sh\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nvidiaProcFile, []byte("NVRM version: NVIDIA UNIX x86_64 Kernel Module  535.104.05  Sat Aug 19 01:15:15 UTC 2023\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if gs = c.nvidiaCards(context.Background()); len(gs) != 1 || gs[0].Driver != "535.104.05" || gs[0].Compute != "" {
		t.Fatalf("from the driver's file: %+v", gs)
	}
}

// The runner's own choice when it starts: CUDA when the card works and the
// build has code for it, the Apple GPU, else the CPU with the reason (a stub
// torch module; needs python3).
func TestStudioRunnerPicksADevice(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	dir := t.TempDir()
	runner := filepath.Join(dir, "run.py")
	if err := os.WriteFile(runner, studioRunner, 0o644); err != nil {
		t.Fatal(err)
	}
	check := filepath.Join(dir, "check.py")
	script := `import importlib.util, json, sys, types
spec = importlib.util.spec_from_file_location("run", sys.argv[1])
run = importlib.util.module_from_spec(spec)
spec.loader.exec_module(run)

def torch(cuda=False, cap=(8, 6), archs=("sm_80", "sm_86", "sm_90"), mps=False, raises=False):
    def is_available():
        return cuda
    def get_device_capability(i):
        if raises:
            raise RuntimeError("The NVIDIA driver on your system is too old")
        return cap
    c = types.SimpleNamespace(is_available=is_available, get_device_capability=get_device_capability, get_arch_list=lambda: list(archs))
    return types.SimpleNamespace(cuda=c, backends=types.SimpleNamespace(mps=types.SimpleNamespace(is_available=lambda: mps)))

out = {
    "ampere": run.pick_device(torch(cuda=True), "auto"),
    "ada on an ampere build": run.pick_device(torch(cuda=True, cap=(8, 9)), "auto"),
    "pascal on a volta+ build": run.pick_device(torch(cuda=True, cap=(6, 1), archs=("sm_70", "sm_80")), "auto"),
    "ptx": run.pick_device(torch(cuda=True, cap=(12, 0), archs=("sm_90", "compute_90")), "auto"),
    "old driver": run.pick_device(torch(cuda=True, raises=True), "auto"),
    "mac": run.pick_device(torch(mps=True), "auto"),
    "plain cpu": run.pick_device(torch(), "auto"),
    "asked for cuda": run.pick_device(torch(), "cuda"),
    "asked for cpu": run.pick_device(torch(cuda=True), "cpu"),
    "oom": run.out_of_memory(RuntimeError("CUDA out of memory. Tried to allocate 2.00 GiB")),
    "other error": run.out_of_memory(RuntimeError("shape mismatch")),
}
print(json.dumps(out))
`
	if err := os.WriteFile(check, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(py, "-I", check, runner)
	cmd.Stderr = io.Discard
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("python: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	device := func(k string) (string, string) {
		v := got[k].([]any)
		return v[0].(string), v[1].(string)
	}
	for k, want := range map[string]string{"ampere": "cuda", "ada on an ampere build": "cuda", "pascal on a volta+ build": "cpu", "ptx": "cuda",
		"old driver": "cpu", "mac": "mps", "plain cpu": "cpu", "asked for cuda": "cpu", "asked for cpu": "cpu"} {
		if d, _ := device(k); d != want {
			t.Errorf("%s: %s, want %s", k, d, want)
		}
	}
	if _, why := device("pascal on a volta+ build"); !strings.Contains(why, "no code for the graphics card") {
		t.Errorf("pascal: %q", why)
	}
	if _, why := device("old driver"); !strings.Contains(why, "CUDA does not start") {
		t.Errorf("old driver: %q", why)
	}
	if _, why := device("plain cpu"); why != "" {
		t.Errorf("no card is no problem: %q", why)
	}
	if got["oom"] != true || got["other error"] != false {
		t.Errorf("out of memory: %v %v", got["oom"], got["other error"])
	}
}
