package api

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"testing"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/recorder"
)

// A recording through the dashboard's endpoints: started, its voice sent in
// pieces at the host's offset (HEAD says where to carry on; a piece for
// another place is refused with the offset), its clock set, and finished
// into footage through the op — with a session and the CSRF header only.
func TestRecordingPiecesOverHTTP(t *testing.T) {
	d, projectID, _ := newDash(t, func(string, string) *fakeclaude.Script { return nil })
	start := recorder.RecordingCreateIn{ProjectID: projectID, Name: "Demo", Tracks: []recorder.RecordingTrackIn{{ID: "voice", Format: recorder.FormatPCM, Rate: 16000}}}
	if code, _ := d.do("POST", "/api/recordings", start, true); code != http.StatusUnauthorized {
		t.Fatalf("started without a session: %d", code)
	}
	d.signIn()
	if code, _ := d.do("POST", "/api/recordings", start, false); code != http.StatusUnauthorized {
		t.Fatalf("started without the CSRF header: %d", code)
	}
	code, env := d.do("POST", "/api/recordings", start, true)
	var rec models.Recording
	if code != 200 || json.Unmarshal(env.Data, &rec) != nil || rec.State != models.RecordingRecording || rec.MachineName != "api-test-machine" {
		t.Fatalf("start: %d %+v %+v", code, env.Error, rec)
	}
	track := "/api/recordings/" + rec.ID + "/tracks/voice"
	offset := func() int64 {
		req, _ := http.NewRequest("HEAD", d.base+track, nil)
		res, err := d.client.Do(req)
		if err != nil || res.StatusCode != http.StatusNoContent {
			t.Fatalf("HEAD: %v %v", err, res)
		}
		res.Body.Close()
		n, _ := strconv.ParseInt(res.Header.Get("Upload-Offset"), 10, 64)
		return n
	}
	piece := func(at int64, b []byte, csrf bool) (int, int64) {
		req, _ := http.NewRequest("PATCH", d.base+track, bytes.NewReader(b))
		req.Header.Set("Upload-Offset", strconv.FormatInt(at, 10))
		req.Header.Set("Content-Type", "application/octet-stream")
		if csrf {
			req.Header.Set(clientHeader, "dashboard")
		}
		res, err := d.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		n, _ := strconv.ParseInt(res.Header.Get("Upload-Offset"), 10, 64)
		return res.StatusCode, n
	}
	voice := make([]byte, 2*16000*2) // two seconds
	for i := 0; i < len(voice)/2; i++ {
		binary.LittleEndian.PutUint16(voice[2*i:], uint16(int16(6000*math.Sin(2*math.Pi*220*float64(i)/16000))))
	}
	if offset() != 0 {
		t.Fatal("a new track has bytes")
	}
	if c, _ := piece(0, voice[:1000], false); c != http.StatusUnauthorized {
		t.Fatalf("a piece without the CSRF header: %d", c)
	}
	if c, n := piece(0, voice[:30000], true); c != 200 || n != 30000 {
		t.Fatalf("first piece: %d %d", c, n)
	}
	if c, n := piece(10, voice[10:20], true); c != http.StatusConflict || n != 30000 {
		t.Fatalf("a piece for another place: %d %d", c, n)
	}
	if offset() != 30000 {
		t.Fatal("HEAD doesn't say where to carry on")
	}
	if c, n := piece(30000, voice[30000:], true); c != 200 || n != int64(len(voice)) {
		t.Fatalf("second piece: %d %d", c, n)
	}
	clock := recorder.RecordingClockIn{Tracks: []recorder.RecordingTrackClock{{ID: "voice", Start: 0.031, Drift: 12, Bytes: int64(len(voice))}}, Length: 2, Spread: 3}
	if code, env := d.do("POST", "/api/recordings/"+rec.ID+"/clock", clock, true); code != 200 {
		t.Fatalf("clock: %d %+v", code, env.Error)
	}
	clock.Ended = "stopped"
	var info recorder.RecordingInfo
	d.op("recording.finish", recorder.RecordingFinishIn{Recording: rec.ID, Clock: &clock}, &info)
	r := info.Recording
	if r.State != models.RecordingReady || r.Track("voice").AssetID == "" || r.Track("voice").Start != 0.031 || len(r.Links) != 1 || r.Remote {
		t.Fatalf("finished: %s %q %+v", r.State, r.Error, r)
	}
	if c, _ := piece(int64(len(voice)), voice[:10], true); c != http.StatusConflict {
		t.Fatalf("a piece after it finished: %d", c)
	}
}
