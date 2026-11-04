// Package vision runs metarush-vision, MetaRush's on-device helper that looks
// at video frames with Apple Vision (macOS only; D73): image labels, animals,
// people, faces with their capture quality, and on-screen text. Nothing leaves
// the machine and nothing is paid for.
//
// The helper's Swift source is embedded in the binary (tools/vision), so the
// helper always matches this build: it is built with swiftc (Xcode Command Line
// Tools) into <home>/tools/metarush-vision by `metarush doctor` or by the
// worker before the first clip it looks at, or by ./bootstrap.sh into bin/
// next to metarush. Arguments
// are fixed options and frame folders MetaRush wrote, never model text.
package vision

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
	visionsrc "github.com/metavert-io/metarush/tools/vision"
)

// Protocol is the helper output format this build asks for. Protocol 2 adds
// face landmarks, attention, hands and body poses, and text in any language;
// protocol 3 adds telling people from their background in footage (segment,
// D507). Helpers of older protocols (built by an older MetaRush, where Swift
// is missing to build it again) still work, without what came later.
const Protocol = "3"

// richProtocol is the first protocol with landmarks, attention, hands, poses
// and text in any language.
const richProtocol = 2

// knownProtocol reports a helper output format this build reads.
func knownProtocol(p string) bool { return p == "1" || p == "2" || p == Protocol }

// protocolOf is a helper's protocol as a number (0 when unknown).
func protocolOf(p string) int {
	switch p {
	case "1":
		return 1
	case "2":
		return 2
	case Protocol:
		return 3
	}
	return 0
}

// ErrNotMac means Apple Vision is not available on this operating system.
var ErrNotMac = errors.New("seeing what's on screen uses Apple Vision, which only runs on a Mac")

// CanRun reports whether this machine can run the on-screen index: a Mac with
// ffmpeg and the helper, or swiftc to build the helper on first use.
func CanRun(ts *tools.Set) bool {
	return runtime.GOOS == "darwin" && ts.Has(tools.FFmpeg) && (ts.Has(tools.Vision) || ts.Has(tools.Swiftc) || cached() != "")
}

// cached is this Mac's build of the embedded source, when one was kept.
func cached() string {
	if d := sharedDir(); d != "" {
		if p := filepath.Join(d, SourceHash()[:16], tools.Vision); fileOK(p) {
			return p
		}
	}
	return ""
}

// SourceHash identifies the embedded helper source.
func SourceHash() string {
	sum := sha256.Sum256(visionsrc.Source)
	return hex.EncodeToString(sum[:])
}

// Status says whether this machine can look at what's on screen.
type Status struct {
	// Supported: this is a Mac (Apple Vision exists here).
	Supported bool `json:"supported"`
	// Ready: the helper is installed, speaks this build's protocol and is up to date.
	Ready    bool   `json:"ready"`
	Path     string `json:"path,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	// Stale: built from an older MetaRush (it is rebuilt automatically when Swift is installed).
	Stale bool `json:"stale,omitempty"`
	// CanBuild: swiftc (Xcode Command Line Tools) is installed.
	CanBuild bool   `json:"canBuild"`
	Text     string `json:"text" jsonschema:"description=Plain words for people."`
	Fix      string `json:"fix,omitempty"`
}

func sidecar(path string) string { return path + ".source-sha256" }

// Check reads the helper's state without changing anything.
func Check(ctx context.Context, ts *tools.Set) Status {
	st := Status{Supported: runtime.GOOS == "darwin", CanBuild: ts.Has(tools.Swiftc) || cached() != ""}
	if !st.Supported {
		st.Text = "Not available on this computer: looking at what's on screen uses Apple Vision, which only runs on a Mac. Everything else works; a Mac in this workspace can index your clips."
		return st
	}
	st.Path = ts.Path(tools.Vision)
	if st.Path == "" {
		st.Text = "Not set up yet: MetaRush's on-screen helper isn't built on this Mac."
		if st.CanBuild {
			st.Fix = "MetaRush builds it the next time it starts, or right away when you run its checkup (doctor)."
		} else {
			st.Fix = "Install Apple's Command Line Tools for Xcode, then run MetaRush's checkup (doctor)."
		}
		return st
	}
	st.Protocol = ts.Version(ctx, tools.Vision)
	if b, err := os.ReadFile(sidecar(st.Path)); err == nil && strings.TrimSpace(string(b)) != SourceHash() {
		st.Stale = true
	}
	switch {
	case !knownProtocol(st.Protocol):
		st.Text = "The on-screen helper on this Mac is from another version of MetaRush."
		st.Stale = true
	case protocolOf(st.Protocol) < richProtocol:
		st.Text = "Ready, but built by an older MetaRush: it doesn't recognize faces or follow movement until it is built again."
		st.Stale, st.Ready = true, true
	case st.Protocol != Protocol:
		st.Text = "Ready, but built by an older MetaRush: it can't tell people from their background in footage until it is built again."
		st.Stale, st.Ready = true, true
	case st.Stale:
		st.Text = "Ready, but built from an older MetaRush; it is rebuilt automatically."
		st.Ready = true
	default:
		st.Text = "Ready: clips are indexed on this Mac with Apple Vision (nothing is uploaded)."
		st.Ready = true
	}
	if st.Stale {
		if st.CanBuild {
			st.Fix = "MetaRush builds it again the next time it starts, or right away when you run its checkup (doctor)."
		} else {
			st.Fix = "Install Apple's Command Line Tools for Xcode, then run MetaRush's checkup (doctor)."
		}
	}
	return st
}

// sharedDir is where builds are kept per source version, so every
// installation on this Mac (and every test home) builds a version only once:
// METARUSH_VISION_CACHE, else the user's cache folder.
func sharedDir() string {
	if d := os.Getenv("METARUSH_VISION_CACHE"); d != "" {
		return d
	}
	if testing.Testing() { // tests never write to the person's own cache folder
		return filepath.Join(os.TempDir(), "metarush-vision-test-cache")
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "metarush", "vision")
	}
	return ""
}

// Build compiles the embedded helper (or reuses this Mac's build of the same
// source) into <home>/tools/metarush-vision and points ts at it.
func Build(ctx context.Context, ts *tools.Set, home string) (string, error) {
	buildMu.Lock()
	defer buildMu.Unlock()
	return build(ctx, ts, home)
}

// buildMu keeps to one build of the helper at a time in this process: the
// worker runs several visual jobs at once, and after an upgrade each of them
// finds the helper out of date.
var buildMu sync.Mutex

func build(ctx context.Context, ts *tools.Set, home string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", ErrNotMac
	}
	dest := config.VisionToolPath(home)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	hash := SourceHash()
	built := ""
	if d := sharedDir(); d != "" {
		if p := filepath.Join(d, hash[:16], tools.Vision); fileOK(p) {
			built = p
		}
	}
	if built == "" {
		if !ts.Has(tools.Swiftc) {
			return "", errors.New("Apple's Command Line Tools for Xcode are not installed, so the helper can't be built (xcode-select --install installs them)")
		}
		work, err := os.MkdirTemp(filepath.Dir(dest), ".build-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(work)
		src := filepath.Join(work, "main.swift")
		if err := os.WriteFile(src, visionsrc.Source, 0o644); err != nil {
			return "", err
		}
		out := filepath.Join(work, tools.Vision)
		if _, err := ts.Run(ctx, tools.Spec{Tool: tools.Swiftc, Args: []string{"-O", "-swift-version", "5", "-o", out, src}, Dir: work, Background: true}); err != nil {
			return "", fmt.Errorf("building the on-screen helper: %w", err)
		}
		built = out
		if d := sharedDir(); d != "" {
			// Keep it for the other installations on this Mac (best effort).
			if err := os.MkdirAll(filepath.Join(d, hash[:16]), 0o755); err == nil {
				_ = copyFile(out, filepath.Join(d, hash[:16], tools.Vision))
			}
		}
	}
	if err := copyFile(built, dest); err != nil {
		return "", err
	}
	if err := os.WriteFile(sidecar(dest), []byte(hash+"\n"), 0o644); err != nil {
		return "", err
	}
	ts.Use(tools.Vision, dest)
	return dest, nil
}

func fileOK(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

// copyFile writes src to dst atomically: a temporary file of its own next to
// dst, then a rename, so two copies at once (two processes, say) both land.
func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o755)
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// Ensure builds the helper when it is missing or stale and this Mac can build
// it. It reports the state afterwards (and whether it built).
func Ensure(ctx context.Context, ts *tools.Set, home string) (Status, bool, error) {
	// One at a time: a job that waited here finds the helper the first one built.
	buildMu.Lock()
	defer buildMu.Unlock()
	st := Check(ctx, ts)
	if st.Supported && !st.Ready && choice.Declined(home, "screen-index") {
		// A person left it out or removed it (D325): never built by itself.
		return st, false, choice.LeftOut("screen-index", "Seeing what's on screen needs the on-screen helper (about 5 MB). Add it in Settings → Components.")
	}
	if theirs(st, home) {
		// Someone else's build (bootstrap, a configured path) that still speaks our protocol: leave it.
		if st.Stale {
			st.Fix = theirsFix
		}
		return st, false, nil
	}
	if !st.Supported || (st.Ready && !st.Stale) || !st.CanBuild {
		return st, false, nil
	}
	if _, err := build(ctx, ts, home); err != nil {
		return st, false, err
	}
	return Check(ctx, ts), true, nil
}

// theirsFix is what to do about an older helper MetaRush didn't build.
const theirsFix = "MetaRush only builds its own helper again: build this one again from this version of MetaRush, or take its path out of MetaRush's settings so MetaRush builds its own."

// theirs: the helper is someone else's build (bootstrap, a path in the
// settings) that speaks a protocol MetaRush knows, which Ensure leaves as it
// is, older or not.
func theirs(st Status, home string) bool {
	return st.Path != "" && st.Path != config.VisionToolPath(home) && knownProtocol(st.Protocol)
}

// Theirs says whether the helper this Mac uses is someone else's build that
// MetaRush never builds again (Ensure leaves it as it is).
func Theirs(ctx context.Context, ts *tools.Set, home string) bool {
	return theirs(Check(ctx, ts), home)
}

// Rebuilds says whether Ensure builds the helper again when it is older than
// this build's: MetaRush's own (or one of a protocol it doesn't know), on a
// Mac that can build it.
func Rebuilds(ctx context.Context, ts *tools.Set, home string) bool {
	st := Check(ctx, ts)
	switch {
	case !st.Supported || !st.CanBuild || theirs(st, home):
		return false
	case !st.Ready && choice.Declined(home, "screen-index"):
		return false
	}
	return true
}

// Box is [x, y, w, h], 0–1 from the top-left of the picture.
type Box [4]float64

// Label is an image classification.
type Label struct {
	ID      string  `json:"id"`
	Conf    float64 `json:"c"`
	Precise bool    `json:"p"`
}

// Detection is something found at a place in the picture.
type Detection struct {
	ID   string  `json:"id,omitempty"`
	Conf float64 `json:"c"`
	Box  Box     `json:"box"`
}

// Face is a detected face.
type Face struct {
	Conf    float64  `json:"c"`
	Box     Box      `json:"box"`
	Quality *float64 `json:"q,omitempty"` // capture quality 0–1 (how usable the face is as a picture)
	Yaw     *float64 `json:"yaw,omitempty"`
	Roll    *float64 `json:"roll,omitempty"`
	// LM are five landmarks for face recognition (protocol 2, on request): the
	// eye on the picture's left, the other eye, the nose tip, the left and right
	// mouth corners, [x, y] 0–1 from the top-left. LMC is how sure Vision is.
	LM  [][2]float64 `json:"lm,omitempty"`
	LMC float64      `json:"lmc,omitempty"`
}

// Attention is where the eye is drawn in a frame (attention saliency): the
// heat map's weighted middle and spread ([x, y], 0–1 from the top-left), its
// peak, and the most salient regions.
type Attention struct {
	At      [2]float64  `json:"at"`
	Spread  [2]float64  `json:"spread"`
	Peak    float64     `json:"peak"`
	Objects []Detection `json:"objects"`
}

// Joint is [x, y, confidence], x and y 0–1 from the top-left (confidence 0:
// not found).
type Joint [3]float64

// Hand joints, in the order the helper writes them.
const (
	HandWrist = iota
	HandThumbCMC
	HandThumbMP
	HandThumbIP
	HandThumbTip
	HandIndexMCP
	HandIndexPIP
	HandIndexDIP
	HandIndexTip
	HandMiddleMCP
	HandMiddlePIP
	HandMiddleDIP
	HandMiddleTip
	HandRingMCP
	HandRingPIP
	HandRingDIP
	HandRingTip
	HandLittleMCP
	HandLittlePIP
	HandLittleDIP
	HandLittleTip
	HandJoints
)

// Body joints, in the order the helper writes them ("left" is the person's left).
const (
	BodyNose = iota
	BodyLeftEye
	BodyRightEye
	BodyLeftEar
	BodyRightEar
	BodyLeftShoulder
	BodyRightShoulder
	BodyNeck
	BodyLeftElbow
	BodyRightElbow
	BodyLeftWrist
	BodyRightWrist
	BodyLeftHip
	BodyRightHip
	BodyRoot
	BodyLeftKnee
	BodyRightKnee
	BodyLeftAnkle
	BodyRightAnkle
	BodyJoints
)

// Hand is a hand's pose (protocol 2, on request).
type Hand struct {
	Conf   float64 `json:"c"`
	Joints []Joint `json:"j"`
	Side   string  `json:"side,omitempty"` // left | right (the person's), when Vision can tell
}

// Body is a person's body pose (protocol 2, on request).
type Body struct {
	Conf   float64 `json:"c"`
	Joints []Joint `json:"j"`
}

// Text is a line of on-screen text.
type Text struct {
	S    string  `json:"s"`
	Conf float64 `json:"c"`
	Box  Box     `json:"box"`
}

// Frame is what the helper saw in one image.
type Frame struct {
	File    string      `json:"file"`
	W       int         `json:"w"`
	H       int         `json:"h"`
	Labels  []Label     `json:"labels"`
	Animals []Detection `json:"animals"`
	Humans  []Detection `json:"humans"`
	Faces   []Face      `json:"faces"`
	Text    []Text      `json:"text"`
	// Protocol 2, on request.
	Attention *Attention `json:"attention,omitempty"`
	Hands     []Hand     `json:"hands,omitempty"`
	Bodies    []Body     `json:"bodies,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// Options are the helper settings MetaRush uses.
type Options struct {
	// Text: accurate (default), fast or off.
	Text string
	// AnyLanguage reads text in every language Apple Vision knows (it finds
	// the language itself), not only English.
	AnyLanguage bool
	// Landmarks, Attention, Hands and Pose ask for face landmarks, where the
	// eye is drawn, hand poses and body poses (protocol 2 helpers only; an
	// older helper answers without them).
	Landmarks, Attention, Hands, Pose bool
}

// Rich reports whether the helper here answers the protocol 2 requests.
func Rich(ctx context.Context, ts *tools.Set) bool {
	return runtime.GOOS == "darwin" && ts.Has(tools.Vision) && protocolOf(ts.Version(ctx, tools.Vision)) >= richProtocol
}

// CanSegment reports whether the helper here tells people from their
// background in footage (protocol 3, D507).
func CanSegment(ctx context.Context, ts *tools.Set) bool {
	return runtime.GOOS == "darwin" && ts.Has(tools.Vision) && protocolOf(ts.Version(ctx, tools.Vision)) >= 3
}

// Analyze looks at every JPEG in dir (sorted by name) and calls each with the
// result, in order.
func Analyze(ctx context.Context, ts *tools.Set, dir string, o Options, each func(Frame)) error {
	if runtime.GOOS != "darwin" {
		return ErrNotMac
	}
	if !ts.Has(tools.Vision) {
		return &tools.ErrMissing{Name: tools.Vision}
	}
	text := o.Text
	switch text {
	case "":
		text = "accurate"
	case "accurate", "fast", "off":
	default:
		return fmt.Errorf("unknown text mode %q", o.Text)
	}
	if !filepath.IsAbs(dir) || strings.HasPrefix(filepath.Base(dir), "-") {
		return fmt.Errorf("refusing frame folder %q", dir)
	}
	args := []string{"analyze", "--dir", dir, "--text", text}
	if (o.Landmarks || o.Attention || o.Hands || o.Pose || o.AnyLanguage) && Rich(ctx, ts) {
		if o.AnyLanguage && text != "off" {
			args = append(args, "--languages", "auto")
		}
		if o.Landmarks {
			args = append(args, "--landmarks")
		}
		if o.Attention {
			args = append(args, "--saliency")
		}
		if o.Hands {
			args = append(args, "--hands")
		}
		if o.Pose {
			args = append(args, "--pose")
		}
	}
	var perr error
	_, err := ts.Run(ctx, tools.Spec{Tool: tools.Vision, Args: args, Background: true,
		OnLine: func(line string) {
			if perr != nil || strings.TrimSpace(line) == "" {
				return
			}
			var f Frame
			if err := json.Unmarshal([]byte(line), &f); err != nil {
				perr = fmt.Errorf("unreadable helper output: %w", err)
				return
			}
			each(f)
		}})
	if err != nil {
		return err
	}
	return perr
}

// ReadFrames parses helper output saved to a file (tests, debugging).
func ReadFrames(path string) ([]Frame, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Frame
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var fr Frame
		if err := json.Unmarshal(sc.Bytes(), &fr); err != nil {
			return nil, err
		}
		out = append(out, fr)
	}
	return out, sc.Err()
}

// Mask cuts the subject of a picture out (Remove the background, D152):
// Apple Vision's subject lifting, written to out as a PNG the size of the
// picture with its background see-through. found is false when Vision sees
// no subject. It needs macOS 14 or later and a helper built from this source.
func Mask(ctx context.Context, ts *tools.Set, in, out string) (found bool, err error) {
	if runtime.GOOS != "darwin" {
		return false, ErrNotMac
	}
	if !ts.Has(tools.Vision) {
		return false, &tools.ErrMissing{Name: tools.Vision}
	}
	for _, p := range []string{in, out} {
		if !filepath.IsAbs(p) || strings.HasPrefix(filepath.Base(p), "-") {
			return false, fmt.Errorf("refusing picture path %q", p)
		}
	}
	var res struct {
		Found     bool `json:"found"`
		Instances int  `json:"instances"`
	}
	var perr error
	_, err = ts.Run(ctx, tools.Spec{Tool: tools.Vision, Args: []string{"mask", "--", in, out}, Background: true,
		OnLine: func(line string) {
			if strings.TrimSpace(line) == "" {
				return
			}
			if e := json.Unmarshal([]byte(line), &res); e != nil {
				perr = fmt.Errorf("unreadable helper output: %w", e)
			}
		}})
	if err != nil {
		return false, err
	}
	if perr != nil {
		return false, perr
	}
	if st, e := os.Stat(out); res.Found && (e != nil || st.Size() == 0) {
		return false, errors.New("the helper wrote no picture")
	}
	return res.Found, nil
}

// Segment qualities: Apple Vision's person segmentation levels. Accurate
// refines the edges (hair, hands) and is the slowest; fast is meant for live
// video.
const (
	SegmentAccurate = "accurate"
	SegmentBalanced = "balanced"
	SegmentFast     = "fast"
)

// Segment tells the people in a stream of frames from their background (D507,
// protocol 3): frames, w×h pixels of four bytes each (blue, green, red, one
// unused), are read from in, and for each frame in order its matte (w×h bytes,
// 0 the background, 255 a person) is written to out. One request runs the
// whole stream, so what Apple Vision saw in the frames before steadies the
// next. It ends when in ends between frames; canceling ctx stops the helper.
func Segment(ctx context.Context, ts *tools.Set, w, h int, quality string, in io.Reader, out io.Writer) error {
	if runtime.GOOS != "darwin" {
		return ErrNotMac
	}
	if !ts.Has(tools.Vision) {
		return &tools.ErrMissing{Name: tools.Vision}
	}
	switch quality {
	case "":
		quality = SegmentAccurate
	case SegmentAccurate, SegmentBalanced, SegmentFast:
	default:
		return fmt.Errorf("unknown segment quality %q", quality)
	}
	if w < 16 || h < 16 || w > 8192 || h > 8192 {
		return fmt.Errorf("frames of %d×%d can't be segmented", w, h)
	}
	if !CanSegment(ctx, ts) {
		return errors.New("the on-screen helper here is from an older MetaRush and can't tell people from their background; it is built again on its next use")
	}
	_, err := ts.Run(ctx, tools.Spec{Tool: tools.Vision, Args: []string{"segment", "--width", strconv.Itoa(w), "--height", strconv.Itoa(h), "--quality", quality},
		Stdin: in, Stdout: out, Background: true})
	return err
}
