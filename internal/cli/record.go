package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/probe"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// AudioDevice is a microphone ffmpeg can record from.
type AudioDevice struct {
	Index  string `json:"index"`
	Name   string `json:"name"`
	Format string `json:"format"` // avfoundation | pulse | alsa
}

var reAVDevice = regexp.MustCompile(`\]\s*\[(\d+)\]\s*(.+)$`)

// parseAVFoundationDevices reads `ffmpeg -f avfoundation -list_devices true -i ""` output.
func parseAVFoundationDevices(stderr string) []AudioDevice {
	var out []AudioDevice
	audio := false
	for _, l := range strings.Split(stderr, "\n") {
		switch {
		case strings.Contains(l, "AVFoundation audio devices"):
			audio = true
			continue
		case strings.Contains(l, "AVFoundation video devices"):
			audio = false
			continue
		}
		if !audio {
			continue
		}
		if m := reAVDevice.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			out = append(out, AudioDevice{Index: m[1], Name: strings.TrimSpace(m[2]), Format: "avfoundation"})
		}
	}
	return out
}

func listAudioDevices(ctx context.Context, a *app.App) ([]AudioDevice, error) {
	if runtime.GOOS == "darwin" {
		res, _ := a.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: []string{"-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", ""}})
		if res == nil {
			return nil, apperr.New(apperr.ToolFailed, "could not list microphones")
		}
		return parseAVFoundationDevices(string(res.Stderr)), nil
	}
	// Linux: PulseAudio/PipeWire's default source, else ALSA's default device.
	return []AudioDevice{{Index: "default", Name: "default input (PulseAudio/PipeWire)", Format: "pulse"}, {Index: "default", Name: "default input (ALSA)", Format: "alsa"}}, nil
}

// captureArgs builds the ffmpeg input for a device.
func captureArgs(format, device string) []string {
	switch format {
	case "avfoundation":
		return []string{"-f", "avfoundation", "-i", ":" + device}
	case "lavfi": // tests: a synthetic input
		return []string{"-f", "lavfi", "-i", device}
	default:
		return []string{"-f", format, "-i", device}
	}
}

// recorder records one take: Start begins capture; Stop finishes the file cleanly
// (ffmpeg reads "q" on stdin), never by killing it, so the WAV header is complete.
type recorder struct {
	a      *app.App
	format string
	device string
	stop   io.WriteCloser
	done   chan error
	start  time.Time
}

func (r *recorder) Start(ctx context.Context, out string) error {
	pr, pw := io.Pipe()
	args := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, captureArgs(r.format, r.device)...)
	args = append(args, "-t", "600", "-ac", "1", "-ar", "48000", "-c:a", "pcm_s16le", out)
	r.stop, r.done, r.start = pw, make(chan error, 1), time.Now()
	go func() {
		_, err := r.a.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args, Stdin: pr})
		r.done <- err
	}()
	return nil
}

func (r *recorder) Stop() error {
	_, _ = r.stop.Write([]byte("q"))
	_ = r.stop.Close()
	select {
	case err := <-r.done:
		return err
	case <-time.After(10 * time.Second):
		return apperr.New(apperr.ToolFailed, "the recording did not stop")
	}
}

func voRecordCmd() *cobra.Command {
	var rf revFlags
	var beats []string
	var device, format, from, until string
	var countIn int
	var listDevices, live bool
	cmd := &cobra.Command{Use: "record", Short: "Record narration: live while the video plays (--live --from), or line by line with an on-screen prompter",
		Long: `Line by line (the default): shows each line as a prompter with its delivery notes, counts
you in, records from the microphone and lets you redo a line. Each line becomes a take;
when you finish, the takes are transcribed, trimmed to the words and placed on their
lines, shown as a change to look over before it is saved.

Live (--live --from <time>): one recording while you play the video from that point
(in the dashboard's recorder the video plays for you). A prompter follows the clock:
each line shows about two seconds before its moment. Enter stops. The take lands where
you spoke: every line starts where you said it, cut only in the pauses between lines.

Controls: Enter to start and to stop · then Enter to keep, r to redo, s to skip, q to finish.`,
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			if listDevices {
				ds, err := listAudioDevices(c.Context(), a)
				if err != nil {
					return failErr(c, err)
				}
				return emit(c, ds, func() {
					for _, d := range ds {
						fmt.Printf("%-8s %s\n", d.Index, d.Name)
					}
				})
			}
			if !a.Tools.Has(tools.FFmpeg) {
				return failErr(c, &tools.ErrMissing{Name: tools.FFmpeg})
			}
			in := c.InOrStdin()
			if f, ok := in.(*os.File); ok && f == os.Stdin && !isInteractive() {
				return fail(c, apperr.HumanOnly, "The recorder needs a terminal: it shows a prompter and waits for Enter.", "Run it yourself in a terminal, or record in the dashboard's Voiceover tab.")
			}
			rev, err := rf.resolve(c, a)
			if err != nil {
				return err
			}
			show, err := ops.CallTyped[voiceover.ShowOut](c.Context(), a, "voiceover.show", voiceover.RevisionIn{RevisionID: rev}, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			if !show.HasScript {
				return failErr(c, apperr.New(apperr.InvalidInput, "This cut has no narration script yet.").
					WithHint("Write or import one first (the Voiceover tab in the dashboard).").WithCommand("metarush voiceover script import <file>"))
			}
			if format == "" {
				format = map[bool]string{true: "avfoundation", false: "pulse"}[runtime.GOOS == "darwin"]
			}
			if device == "" {
				device = "default"
				if format == "avfoundation" {
					device = "0"
				}
			}
			rec := &recorder{a: a, format: format, device: device}
			if live || from != "" {
				t, ok := editspec.ParseClock(from)
				if from == "" {
					t, ok = 0, true
				}
				if !ok {
					return fail(c, apperr.InvalidInput, "could not read --from (use m:ss.s)", "")
				}
				var u *float64
				if until != "" {
					v, ok := editspec.ParseClock(until)
					if !ok || v <= t {
						return fail(c, apperr.InvalidInput, "--until must be a time after --from (m:ss.s)", "")
					}
					u = &v
				}
				return liveSession(c, a, show, t, u, rec, countIn, bufio.NewReader(in))
			}
			return recordSession(c, a, show, beats, rec, countIn, bufio.NewReader(in))
		}}
	rf.add(cmd)
	cmd.Flags().BoolVar(&live, "live", false, "one recording while the video plays; it lands where you speak")
	cmd.Flags().StringVar(&from, "from", "", "with --live: where on the video you start (m:ss.s; default the start)")
	cmd.Flags().StringVar(&until, "until", "", "with --live: re-record only up to this point (m:ss.s)")
	cmd.Flags().StringSliceVar(&beats, "beat", nil, "only these lines (IDs or numbers; default all without a recording)")
	cmd.Flags().StringVar(&device, "device", "", "microphone (see --list-devices; macOS: the number)")
	cmd.Flags().StringVar(&format, "input-format", "", "avfoundation | pulse | alsa (default by OS)")
	cmd.Flags().IntVar(&countIn, "count-in", 3, "seconds of count-in before recording")
	cmd.Flags().BoolVar(&listDevices, "list-devices", false, "list microphones")
	_ = cmd.Flags().MarkHidden("input-format")
	return cmd
}

type recordedTake struct {
	beat    voiceover.BeatView
	path    string
	assetID string
}

func recordSession(c *cobra.Command, a *app.App, show *voiceover.ShowOut, only []string, rec *recorder, countIn int, keys *bufio.Reader) error {
	want := map[string]bool{}
	for _, b := range only {
		want[b] = true
	}
	var queue []voiceover.BeatView
	for _, b := range show.Beats {
		if len(want) > 0 {
			if want[b.ID] || want[fmt.Sprint(b.Line)] {
				queue = append(queue, b)
			}
			continue
		}
		if b.Take == nil || (b.Take.Source != models.TakeRecorded && b.Take.Source != models.TakeImported) {
			queue = append(queue, b)
		}
	}
	if len(queue) == 0 {
		return emit(c, map[string]any{"recorded": 0}, func() { fmt.Println("Every line already has a recording (use --beat to redo one).") })
	}
	dir := filepath.Join(a.Cfg.Scratch.Dir, "recordings", ids.New("rec"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	readKey := func() string {
		l, err := keys.ReadString('\n')
		if err != nil && l == "" {
			return "q"
		}
		return strings.ToLower(strings.TrimSpace(l))
	}
	say := func(f string, x ...any) { fmt.Fprintf(c.OutOrStdout(), f, x...) }
	var takes []recordedTake
	var mu sync.Mutex
outer:
	for qi, b := range queue {
		for attempt := 1; ; attempt++ {
			say("\n── Line %d of %d %s\n", b.Line, len(show.Beats), strings.Repeat("─", 40))
			if b.Delivery != "" {
				say("   (%s)\n", b.Delivery)
			}
			for _, l := range wrapText(b.Text, 60) {
				say("   %s\n", l)
			}
			say("   about %.0f seconds · Enter to start, s to skip, q to finish: ", b.Window)
			switch readKey() {
			case "s":
				continue outer
			case "q":
				break outer
			}
			for i := countIn; i > 0; i-- {
				say("%d… ", i)
				time.Sleep(time.Second)
			}
			path := filepath.Join(dir, fmt.Sprintf("line-%02d-take-%d.wav", b.Line, attempt))
			if err := rec.Start(c.Context(), path); err != nil {
				return failErr(c, err)
			}
			say("\n   ● Recording — Enter to stop ")
			_ = readKey()
			if err := rec.Stop(); err != nil {
				return failErr(c, apperr.Wrap(apperr.ToolFailed, err, "the microphone recording failed (check the device and microphone permission)"))
			}
			mi, err := probe.Probe(c.Context(), a.Tools, path)
			if err != nil || mi.Duration < 0.3 {
				say("\n   That take is empty or too short. Try again.\n")
				continue
			}
			say("\n   Saved take %d (%.1f s). Enter to keep, r to redo, q to keep and finish: ", attempt, mi.Duration)
			k := readKey()
			if k == "r" {
				continue
			}
			mu.Lock()
			takes = append(takes, recordedTake{beat: b, path: path})
			mu.Unlock()
			if k == "q" || qi == len(queue)-1 {
				break outer
			}
			continue outer
		}
	}
	if len(takes) == 0 {
		return emit(c, map[string]any{"recorded": 0}, func() { fmt.Println("Nothing recorded.") })
	}
	say("\nSaving %d take(s) and transcribing them on this machine…\n", len(takes))
	r, err := getRev(c, a, show.RevisionID)
	if err != nil {
		return err
	}
	var eops []editspec.Op
	var placed []map[string]any
	for i := range takes {
		t := &takes[i]
		assetID, err := importRecording(c, a, r.ProjectID, t.path, true)
		if err != nil {
			return err
		}
		t.assetID = assetID
		in, out := trimToWords(c.Context(), a, t)
		tv, err := ops.CallTyped[voiceover.TakeView](c.Context(), a, "voiceover.addTake", voiceover.AddTakeIn{ProjectID: r.ProjectID, BeatID: t.beat.ID, Text: t.beat.Text,
			AssetID: assetID, In: in, Out: out, Source: models.TakeRecorded}, ops.Caller{})
		if err != nil {
			return failErr(c, err)
		}
		ref := editspec.TakeRef{TakeID: tv.ID, AssetID: assetID, In: tv.In, Out: tv.Out, Source: models.TakeRecorded}
		b, _ := json.Marshal(map[string]any{"take": voiceover.TakeSet(&ref), "state": "recorded"})
		eops = append(eops, editspec.Op{Op: "updateBeat", ID: t.beat.ID, Set: b})
		placed = append(placed, map[string]any{"line": t.beat.Line, "takeId": tv.ID, "assetId": assetID, "seconds": tv.Duration})
	}
	plan := editspec.Plan{BaseRevisionID: r.ID, Branch: r.Branch, Instruction: fmt.Sprintf("Recorded narration for %d line(s)", len(takes)), Ops: eops}
	return planFlow(c, a, plan, "", map[string]any{"recorded": placed}, func() { fmt.Printf("Recorded %d line(s).\n", len(takes)) })
}

// cue is when a line is due on the live prompter (seconds into the take).
type cue struct {
	at   float64
	line voiceover.BeatView
}

// liveCues schedules the prompter for a take starting at from (timeline
// seconds), like the dashboard's (web/src/screens/studio/voiceover/prompter.ts):
// a line with a start point of its own is due at its moment; one that follows
// the line before is due when that line has been said, at the narrator's pace.
// Lines already over at from are left out.
func liveCues(show *voiceover.ShowOut, from float64) []cue {
	var out []cue
	prevEnd := -1.0
	for _, b := range show.Beats {
		if b.Problem != "" {
			continue
		}
		dur := b.End - b.Start
		var t float64
		switch {
		case b.Pinned:
			t = b.Start
		case prevEnd < 0:
			t = math.Max(b.Start, from+1)
		default:
			t = prevEnd + 0.3
		}
		if !b.Pinned && t < from+1 && prevEnd < from {
			t = from + 1
		}
		prevEnd = t + dur
		if t+dur <= from {
			continue
		}
		out = append(out, cue{at: math.Max(0, t-from), line: b})
	}
	return out
}

// liveSession records one live take from a point of the video: a prompter in
// the terminal follows the clock (each line about two seconds before its
// moment), Enter stops, and the take is placed where it was spoken.
func liveSession(c *cobra.Command, a *app.App, show *voiceover.ShowOut, from float64, until *float64, rec *recorder, countIn int, keys *bufio.Reader) error {
	say := func(f string, x ...any) { fmt.Fprintf(c.OutOrStdout(), f, x...) }
	cues := liveCues(show, from)
	dir := filepath.Join(a.Cfg.Scratch.Dir, "recordings", ids.New("rec"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "live-take.wav")
	say("\nA live take from %s: play the video from there when the count-in ends and read along.\n", editspec.Clock(from))
	say("Each line shows about two seconds before its moment. Enter to start, Enter again to stop: ")
	if l, _ := keys.ReadString('\n'); strings.TrimSpace(strings.ToLower(l)) == "q" {
		return emit(c, map[string]any{"recorded": 0}, func() { fmt.Println("Nothing recorded.") })
	}
	for i := countIn; i > 0; i-- {
		say("%d… ", i)
		time.Sleep(time.Second)
	}
	if err := rec.Start(c.Context(), path); err != nil {
		return failErr(c, err)
	}
	start := time.Now()
	say("\n● Recording (the video is at %s) — Enter to stop\n", editspec.Clock(from))
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i, q := range cues {
			for _, step := range []struct {
				at   float64
				text string
			}{{q.at - 2, fmt.Sprintf("   next (%d): %s\n", q.line.Line, q.line.Text)}, {q.at, fmt.Sprintf("▶ %d. %s\n", q.line.Line, q.line.Text)}} {
				if i == 0 && step.at <= 0 {
					continue
				}
				wait := time.Until(start.Add(time.Duration(math.Max(0, step.at) * float64(time.Second))))
				select {
				case <-stop:
					return
				case <-time.After(wait):
					say("%s", step.text)
				}
			}
		}
	}()
	if len(cues) > 0 && cues[0].at <= 2 {
		say("▶ %d. %s\n", cues[0].line.Line, cues[0].line.Text)
	}
	_, _ = keys.ReadString('\n')
	close(stop)
	wg.Wait()
	if err := rec.Stop(); err != nil {
		return failErr(c, apperr.Wrap(apperr.ToolFailed, err, "the microphone recording failed (check the device and microphone permission)"))
	}
	if mi, err := probe.Probe(c.Context(), a.Tools, path); err != nil || mi.Duration < 0.3 {
		return fail(c, apperr.Validation, "the take is empty or too short", "try again")
	}
	say("Saving the take and listening to it on this machine…\n")
	r, err := getRev(c, a, show.RevisionID)
	if err != nil {
		return err
	}
	assetID, err := importRecording(c, a, r.ProjectID, path, true)
	if err != nil {
		return err
	}
	out, err := ops.CallTyped[voiceover.PlaceOut](c.Context(), a, "voiceover.place", voiceover.PlaceIn{RevisionID: r.ID, AssetID: assetID, From: &from, Until: until, Source: models.TakeRecorded}, ops.Caller{})
	if err != nil {
		return failErr(c, err)
	}
	if out.Result == nil {
		return emit(c, out, func() { printPlaced(out) })
	}
	return planFlow(c, a, out.Result.Plan, "", map[string]any{"placed": out}, func() { printPlaced(out) })
}

// trimToWords trims a recorded take to its spoken words (with a little air),
// using the transcript; the whole file when the words cannot be found.
func trimToWords(ctx context.Context, a *app.App, t *recordedTake) (float64, float64) {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), t.assetID, &as); err != nil {
		return 0, 0
	}
	dur := as.Media.Duration
	var segs []models.TranscriptSegment
	_ = a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"assetId": t.assetID}, &segs, options.Find().SetSort(bson.D{{Key: "idx", Value: 1}}))
	var words []editspec.TranscriptWord
	for _, s := range segs {
		for _, w := range s.Words {
			words = append(words, editspec.TranscriptWord{Text: w.Text, From: w.From, To: w.To})
		}
	}
	al := editspec.AlignScript([]editspec.Beat{{ID: t.beat.ID, Text: t.beat.Text}}, words, dur)
	if len(al.Beats) == 1 && al.Beats[0].Found {
		return al.Beats[0].From, al.Beats[0].To
	}
	return 0, dur
}

func wrapText(s string, width int) []string {
	var out []string
	line := ""
	for _, w := range strings.Fields(s) {
		if len(line)+len(w)+1 > width && line != "" {
			out = append(out, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}
