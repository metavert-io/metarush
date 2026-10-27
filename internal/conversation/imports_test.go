package conversation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

// runJobsFor runs an imported recording's evidence job here.
func (c *call) runJobsFor(t *testing.T, assetID string) {
	t.Helper()
	c.runJobs(t, assetID, "evidence")
}

// transcriptsFromTruth gives Zoom's per-person tracks the truth's words: on
// the conversation's clock for Maya, the call's delay later for Sam (his
// sound as it arrived).
func (c *call) transcriptsFromTruth(t *testing.T, maya, sam string) {
	t.Helper()
	var jt, st []turnAt
	for _, x := range c.tr.Turns {
		switch x.Speaker {
		case "Maya":
			jt = append(jt, turnAt{x.Start, x.End, x.Text, "speaker_0"})
		case "Sam":
			st = append(st, turnAt{x.Start + c.tr.Zoom.SamDelay, x.End + c.tr.Zoom.SamDelay, x.Text, "speaker_0"})
		}
	}
	c.transcript(t, maya, jt)
	c.transcript(t, sam, st)
}

func names(files ...string) []CallDetectFile {
	var out []CallDetectFile
	for _, f := range files {
		out = append(out, CallDetectFile{Name: f})
	}
	return out
}

func byPerson(d *CallDetectOut) map[string]CallDetectMember {
	out := map[string]CallDetectMember{}
	for _, m := range d.Members {
		k := m.Person
		if k == "" {
			k = filepath.Base(m.File)
		}
		out[k] = m
	}
	return out
}

// The folders call apps leave, by their names.
func TestDetectRecordingFolders(t *testing.T) {
	z := detectFiles(names(
		"2026-10-19 10.00.00 Garden Show/video1123456789.mp4",
		"2026-10-19 10.00.00 Garden Show/audio1123456789.m4a",
		"2026-10-19 10.00.00 Garden Show/recording.conf",
		"2026-10-19 10.00.00 Garden Show/chat.txt",
		"2026-10-19 10.00.00 Garden Show/Audio Record/audioMayaLindqvist11123456789.m4a",
		"2026-10-19 10.00.00 Garden Show/Audio Record/audioSam21123456789.m4a",
		"2026-10-19 10.00.00 Garden Show/Audio Record/audioRoom1231123456789.m4a",
	))
	p := byPerson(z)
	if !z.Found || z.Provider != "zoom" || !z.Aligned || z.Name != "Garden Show" || z.Folder != "2026-10-19 10.00.00 Garden Show" {
		t.Fatalf("zoom: %+v", z)
	}
	if p["Maya Lindqvist"].Use != editspec.UseSound || p["Sam"].Use != editspec.UseSound || p["Room12"].Use != editspec.UseSound {
		t.Fatalf("zoom people: %+v", z.Members)
	}
	if v := p["video1123456789.mp4"]; v.Use != editspec.UsePicture || v.Skip {
		t.Fatalf("zoom video: %+v", v)
	}
	if mix := p["audio1123456789.m4a"]; !mix.Skip {
		t.Fatalf("the mix is left out: %+v", mix)
	}
	if !strings.Contains(z.Text, "A Zoom recording with Maya Lindqvist, Room12 and Sam, and the meeting's video; they start together.") {
		t.Fatalf("says: %s", z.Text)
	}
	old := detectFiles(names("Meeting/zoom_0.mp4", "Meeting/audio_only.m4a", "Meeting/audio_only_1_Ana.m4a", "Meeting/audio_only_2_Bo_Li.m4a"))
	if !old.Found || old.Provider != "zoom" || old.Aligned || len(old.Notes) != 1 || byPerson(old)["Bo Li"].Use != editspec.UseSound {
		t.Fatalf("old zoom: %+v", old)
	}
	cloud := detectFiles(names("GMT20260101-120000_Recording_1920x1080.mp4", "GMT20260101-120000_Recording_gallery_2560x1440.mp4", "GMT20260101-120000_Recording.m4a", "Audio only - Ana Silva.m4a", "Audio only - Bo.m4a"))
	cp := byPerson(cloud)
	if !cloud.Found || cp["Ana Silva"].Use != editspec.UseSound || cp["GMT20260101-120000_Recording_1920x1080.mp4"].Use != editspec.UsePicture || !cp["GMT20260101-120000_Recording_gallery_2560x1440.mp4"].Skip {
		t.Fatalf("zoom cloud: %+v", cloud)
	}
	rv := detectFiles(names("riverside_maya_raw-video-cfr_garden_show_0001.mp4", "riverside_sam_raw-video-cfr_garden_show_0002.mp4", "riverside_sam_raw-audio_garden_show_0002.wav"))
	rp := byPerson(rv)
	if !rv.Found || rv.Provider != "riverside" || rv.Aligned || rv.Name != "Garden Show" || rp["Maya"].Use != editspec.UseBoth || rp["Sam"].Use != editspec.UseBoth || !rp["riverside_sam_raw-audio_garden_show_0002.wav"].Skip {
		t.Fatalf("riverside: %+v", rv)
	}
	sq := detectFiles(names("SquadCast/Episode 12/Take 1/ana_cam.mp4", "SquadCast/Episode 12/Take 1/bo-audio.wav"))
	if !sq.Found || sq.Provider != "squadcast" || byPerson(sq)["Ana"].Use != editspec.UseBoth || byPerson(sq)["Bo"].Use != editspec.UseSound {
		t.Fatalf("squadcast: %+v", sq)
	}
	one := detectFiles(names("talk/interview.mp4", "talk/notes.txt"))
	if one.Found {
		t.Fatalf("one recording is not a conversation: %+v", one)
	}
}

// A Zoom folder with separate audio per participant comes in as footage and
// one conversation: each person's own sound, the meeting's video as the
// picture, starting together as Zoom recorded them.
func TestImportZoomFolder(t *testing.T) {
	c := newCall(t)
	gen, tr := fixtures(t)
	dir := filepath.Join(gen, tr.Zoom.Folder)
	if _, err := os.Stat(dir); err != nil {
		t.Skip("no zoom fixture")
	}
	det := scall[CallDetectOut](t, c.a, "conversation.detect", CallDetectIn{Folder: dir})
	if !det.Found || det.Provider != "zoom" {
		t.Fatalf("detect: %+v", det)
	}
	out := scall[CallOut](t, c.a, "conversation.import", CallImportIn{ProjectID: c.project, Folder: dir})
	t.Log(out.Summary)
	v := out.Conversation
	if v.Source != "zoom" || v.Name != "Garden Show" || len(v.Members) != 3 {
		t.Fatalf("imported: %+v", v)
	}
	uses := map[string]string{}
	for _, m := range v.Members {
		uses[m.Name] = m.Use
		if m.Start != 0 {
			t.Fatalf("%s starts at %.3f; Zoom's files start together", m.Name, m.Start)
		}
	}
	if uses["Maya"] != editspec.UseSound || uses["Sam"] != editspec.UseSound {
		t.Fatalf("uses: %+v", uses)
	}
	// Its automatic cut shows the meeting's video and plays each person's own sound.
	for i := range []int{0, 1, 2} {
		c.runJobsFor(t, v.Members[i].AssetID)
	}
	c.transcriptsFromTruth(t, v.Members[0].AssetID, v.Members[1].AssetID)
	po := scall[CallPlanOut](t, c.a, "conversation.cut", CallCutIn{CallRefIn: CallRefIn{Conversation: v.ID}, Apply: true})
	if po.Applied == nil || po.Applied.Problem != "" {
		t.Fatalf("cut: %s", po.Summary)
	}
	s := revisionSpec(t, c, po.Applied.RevisionID)
	pics := map[string]bool{}
	for _, sh := range s.Clips[0].Shots {
		if sh.Member != "" {
			pics[sh.Member] = true
		}
	}
	if len(s.Clips) != 1 || len(pics) > 1 {
		t.Fatalf("shots: %+v", s.Clips)
	}
}
