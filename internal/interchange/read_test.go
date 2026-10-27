package interchange

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

func writeFormat(t *testing.T, tl *Timeline, format string) []byte {
	t.Helper()
	var b []byte
	var err error
	switch format {
	case FormatOTIO:
		b, _, err = WriteOTIO(tl, nil)
	case FormatFCPXML:
		b, _, err = WriteFCPXML(tl, nil)
	case FormatXMEML:
		b, _, err = WriteXMEML(tl, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fixtureUses ties each source of a read timeline to the fixture's footage:
// by MetaRush's own note, else by file name.
func fixtureUses(r *Read) map[string]Use {
	media := fixtureMedia()
	uses := map[string]Use{}
	for _, s := range r.Sources {
		for id, m := range media {
			if s.AssetID == id || strings.EqualFold(s.File, m.Name) || strings.EqualFold(filepath.Base(s.Path), m.Name) {
				uses[s.Key] = Use{AssetID: id, Duration: m.Duration, Name: m.Name}
			}
		}
	}
	return uses
}

type planClip struct {
	asset            string
	from, dur, trim  int
	rate             float64
	trans            string
	transDur         int
	muted            bool
	soundFrom, sound int
}

func clipsOf(p *editspec.RenderPlan) []planClip {
	var out []planClip
	for _, c := range p.Clips {
		pc := planClip{asset: c.AssetID, from: c.From, dur: c.DurationInFrames, trim: c.TrimBefore, rate: c.PlaybackRate, trans: c.TransitionIn.Type, transDur: c.TransitionIn.DurationInFrames, muted: c.Muted}
		if pc.rate == 0 {
			pc.rate = 1
		}
		if !c.Muted && c.HasAudio {
			pc.soundFrom, pc.sound, _ = c.Sound()
		}
		out = append(out, pc)
	}
	return out
}

func within(a, b, tol int) bool { return a-b <= tol && b-a <= tol }

// The round trip (D304): a cut exported in each format and read back plays
// the same clips, from the same source frames, at the same times, with the
// same transitions and split edits, and the same music and narration.
func TestReadRoundTrip(t *testing.T) {
	tl, plan := fixtureTimeline(t)
	want := clipsOf(plan)
	for _, format := range Formats {
		t.Run(format, func(t *testing.T) {
			data := writeFormat(t, tl, format)
			r, err := ReadTimeline(data, ReadOptions{Name: "fixture" + Extension(format)})
			if err != nil {
				t.Fatal(err)
			}
			// Every format carries MetaRush's own note: which clip each file is,
			// so it comes back without opening the files.
			for _, src := range r.Sources {
				if m := tl.mediaByID(src.AssetID); m == nil || (m.SHA256 != "" && src.SHA256 != m.SHA256) {
					t.Errorf("%s: source %q came back as clip %q (%s)", format, src.Name, src.AssetID, src.SHA256)
				}
			}
			f := Flatten(r)
			res := f.Spec(SpecOptions{Uses: fixtureUses(r)})
			got := editspec.Resolve(res.Spec, fixtureFacts(), "rev_back", "prj_test", "hash")
			have := clipsOf(got)
			if len(have) != len(want) {
				t.Fatalf("%d clips back, want %d\n%s", len(have), len(want), dumpFlat(f, res))
			}
			for i := range want {
				w, h := want[i], have[i]
				wantTrans := w.trans
				switch {
				case format != FormatOTIO && (wantTrans == editspec.TransitionWipe):
					wantTrans = editspec.TransitionCrossfade // FCPXML and FCP7 XML write a dissolve of the same length
				}
				if h.asset != w.asset || !within(h.from, w.from, 1) || !within(h.dur, w.dur, 1) || !within(h.trim, w.trim, 1) || math.Abs(h.rate-w.rate) > 1e-6 ||
					h.trans != wantTrans || !within(h.transDur, w.transDur, 1) || h.muted != w.muted {
					t.Errorf("clip %d: got %+v\nwant %+v", i, h, w)
				}
				if !w.muted && (!within(h.soundFrom, w.soundFrom, 1) || !within(h.sound, w.sound, 1)) {
					t.Errorf("clip %d sound: got from %d for %d, want from %d for %d", i, h.soundFrom, h.sound, w.soundFrom, w.sound)
				}
			}
			if got.DurationInFrames != plan.DurationInFrames {
				t.Errorf("duration %d frames, want %d", got.DurationInFrames, plan.DurationInFrames)
			}
			// Music and narration.
			byAsset := map[string]editspec.PlanAudio{}
			for _, a := range got.Audio {
				byAsset[a.AssetID] = a
			}
			for _, a := range plan.Audio {
				if a.AssetID != astM && a.AssetID != astV {
					continue
				}
				b, ok := byAsset[a.AssetID]
				role := a.Role
				if format == FormatXMEML && role == editspec.RoleMusic {
					role = editspec.RoleDialogue // FCP7 XML doesn't say what a sound is (the import says so)
				}
				if !ok || !within(b.From, a.From, 1) || !within(b.DurationInFrames, a.DurationInFrames, 1) || !within(b.TrimBefore, a.TrimBefore, 1) || b.Role != role {
					t.Errorf("sound %s: got %+v, want from %d for %d (trim %d, %s)", a.AssetID, b, a.From, a.DurationInFrames, a.TrimBefore, a.Role)
				}
			}
			if t.Failed() {
				t.Log(dumpFlat(f, res))
			}
		})
	}
}

func dumpFlat(f *Flat, res *SpecResult) string {
	var b strings.Builder
	for _, s := range f.Shots {
		fmt.Fprintf(&b, "shot %-22q at %.3f len %.3f in %.3f speed %g enter %s(%.3f+%.3f) hb %.3f ha %.3f own %v lead %.3f carry %.3f fadeout %.3f\n",
			s.Name, s.At, s.Length, s.In, s.Speed, s.Enter.Kind, s.Enter.Before, s.Enter.After, s.HandleBefore, s.HandleAfter, s.OwnSound, s.Lead, s.Carry, s.FadeOut)
	}
	for _, s := range f.Sounds {
		fmt.Fprintf(&b, "sound %-22q at %.3f len %.3f in %.3f role %s\n", s.Name, s.At, s.Length, s.In, s.Role)
	}
	fmt.Fprintf(&b, "lost: %q\n", f.Lost)
	if res != nil {
		fmt.Fprintf(&b, "spec lost: %q\n", res.Lost)
	}
	return b.String()
}

// TestReadOtherWriters reads the files OpenTimelineIO wrote from the fixture
// (scripts/interchange-check.sh sets INTERCHANGE_OTHER): at every sampled
// frame away from transitions, the cut plays the same file from the same
// moment that OpenTimelineIO reads from that file.
func TestReadOtherWriters(t *testing.T) {
	dir := os.Getenv("INTERCHANGE_OTHER")
	if dir == "" {
		t.Skip("run by scripts/interchange-check.sh")
	}
	var exp struct {
		FPS     float64             `json:"fps"`
		Files   []string            `json:"files"`
		Samples map[string][][3]any `json:"samples"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "other.expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &exp); err != nil {
		t.Fatal(err)
	}
	for _, name := range exp.Files {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			r, err := ReadTimeline(data, ReadOptions{Name: name, FPS: exp.FPS})
			if err != nil {
				t.Fatal(err)
			}
			f := Flatten(r)
			frame := 1 / exp.FPS
			checked := 0
			samples := exp.Samples[name]
			for _, s := range samples {
				at := s[0].(float64) / exp.FPS
				file, _ := s[1].(string)
				src, _ := s[2].(float64)
				var hit *Shot
				for i := range f.Shots {
					sh := &f.Shots[i]
					if at >= sh.oldAt-1e-6 && at < sh.oldAt+sh.Length-1e-6 {
						hit = sh
					}
				}
				if file == "" {
					if hit != nil {
						t.Errorf("frame %v: plays %q, want nothing", s[0], hit.Name)
					}
					continue
				}
				if hit == nil {
					t.Errorf("frame %v: plays nothing, want %s at %.3f", s[0], file, src)
					continue
				}
				srcFile := ""
				if so := r.Source(hit.Source); so != nil {
					srcFile = firstNonEmptyStr(so.File, filepath.Base(so.Path), so.Name)
				}
				got := hit.In + (at-hit.oldAt)*hit.Speed
				if !strings.EqualFold(srcFile, file) && !strings.EqualFold(hit.Name, strings.TrimSuffix(file, filepath.Ext(file))) {
					t.Errorf("frame %v: plays %q (%s), want %s", s[0], srcFile, hit.Name, file)
					continue
				}
				if math.Abs(got-src) > 1.5*frame*math.Max(1, hit.Speed) {
					t.Errorf("frame %v: plays %s from %.3f s, want %.3f s", s[0], file, got, src)
				}
				checked++
			}
			if checked < len(samples)/2 {
				t.Errorf("only %d of %d frames checked", checked, len(samples))
			}
			t.Logf("%s: %d frames match OpenTimelineIO's timeline; read as %s; lost: %q", name, checked, r.FormatName, f.Lost)
		})
	}
}

// mediaByID is a timeline's file by its clip's ID (nil: none).
func (tl *Timeline) mediaByID(id string) *Media {
	var found *Media
	var walk func(ts []*Track)
	walk = func(ts []*Track) {
		for _, t := range ts {
			for _, it := range t.Items {
				if it.Media != nil && it.Media.ID == id && id != "" {
					found = it.Media
				}
			}
		}
	}
	walk(tl.Video)
	walk(tl.Audio)
	return found
}
