package google_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiservices/aiconformance"
	"github.com/metavert-io/metarush/internal/aiservices/google"
	"github.com/metavert-io/metarush/internal/testutil/fakevideogen"
	"github.com/metavert-io/metarush/internal/videogen"
)

// fakeKey is a key-shaped value made at run time (no literal for the secret scanner).
func fakeKey() string { return "gk-" + strings.Repeat("Hb5", 10) + "-fake" }

func setup(t *testing.T) (*fakevideogen.Server, string) {
	t.Helper()
	key := fakeKey()
	fake := fakevideogen.New(key)
	t.Cleanup(fake.Close)
	t.Setenv(videogen.EnvGeminiBaseURL, fake.URL)
	return fake, key
}

// Every Google kind passes the conformance suite against the fake Gemini API.
func TestGoogleAdaptersConform(t *testing.T) {
	fake, key := setup(t)
	for _, kind := range []string{aiservices.KindPicture, aiservices.KindVoice, aiservices.KindVoiceCopy, aiservices.KindMusic, aiservices.KindTranscription} {
		t.Run(kind, func(t *testing.T) {
			a, err := aiservices.Get(google.Name, kind)
			if err != nil {
				t.Fatal(err)
			}
			aiconformance.Run(t, aiconformance.Harness{Adapter: a, Key: key, Kept: fake.FilesKept, Problems: fake.Problems})
		})
	}
	for _, g := range fake.GenRequests() {
		if g.Store == nil || *g.Store {
			t.Errorf("a request to %s asked Google to keep it", g.Model)
		}
	}
}

func submit(t *testing.T, kind, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	t.Helper()
	a, err := aiservices.Get(google.Name, kind)
	if err != nil {
		t.Fatal(err)
	}
	return a.Submit(context.Background(), key, req)
}

func kindOf(err error) string {
	var ve *videogen.Error
	if errors.As(err, &ve) {
		return ve.Kind
	}
	return ""
}

// A picture's size and shape go in imageConfig; a refusal in words, a
// blocked prompt or a safety finish are each the safety filters' doing.
func TestPictureMapping(t *testing.T) {
	fake, key := setup(t)
	req := &aiservices.Request{Kind: aiservices.KindPicture, Task: aiservices.TaskGenerate, Model: "gemini-nano-banana-2.1", Prompt: "a kite",
		Options: aiservices.Options{Size: "2k", AspectRatio: "16:9"}}
	tk, err := submit(t, aiservices.KindPicture, key, req)
	if err != nil {
		t.Fatal(err)
	}
	if tk.Ready == nil || tk.Ready.Outputs[0].MIME != "image/png" || !tk.Ready.Usage.Known {
		t.Fatalf("ticket %+v", tk.Ready)
	}
	got := fake.GenRequests()[0]
	ic, _ := got.Config["imageConfig"].(map[string]any)
	if ic["imageSize"] != "2K" || ic["aspectRatio"] != "16:9" {
		t.Errorf("imageConfig %v", ic)
	}
	for _, marker := range []string{"[fake:safety]", "[fake:image-safety]", "[fake:refuse]"} {
		req.Prompt = "a kite " + marker
		if _, err := submit(t, aiservices.KindPicture, key, req); kindOf(err) != videogen.KindSafety {
			t.Errorf("%s: %v", marker, err)
		}
	}
	req.Prompt = "a kite [fake:rate]"
	if _, err := submit(t, aiservices.KindPicture, key, req); kindOf(err) != videogen.KindRate {
		t.Errorf("rate: %v", err)
	}
	req.Prompt = "a kite"
	if _, err := submit(t, aiservices.KindPicture, "wrong-key-0000000000000000", req); kindOf(err) != videogen.KindKey || !strings.Contains(err.Error(), "Gemini key") {
		t.Errorf("key: %v", err)
	}
}

// Speech names its voice (a copy by its ID) and its style; raw samples come
// back as a WAV file with its length measured.
func TestSpeechMapping(t *testing.T) {
	fake, key := setup(t)
	req := &aiservices.Request{Kind: aiservices.KindVoice, Model: "gemini-3.8-flash-tts", Prompt: "Welcome to the garden. [fake:pcm]",
		Options: aiservices.Options{Voice: "voice_abc123", Style: "warm and slow"}}
	tk, err := submit(t, aiservices.KindVoice, key, req)
	if err != nil {
		t.Fatal(err)
	}
	o := tk.Ready.Outputs[0]
	if o.MIME != "audio/wav" || aiservices.Sniff(o.Data) != "audio/wav" || tk.Ready.Usage.Seconds < 1 {
		t.Fatalf("speech %s %s %v", o.MIME, aiservices.Sniff(o.Data), tk.Ready.Usage)
	}
	sc, _ := fake.GenRequests()[0].Config["speechConfig"].(map[string]any)
	vc, _ := sc["voiceConfig"].(map[string]any)
	if vc["voice"] != "voice_abc123" {
		t.Errorf("speechConfig %v", sc)
	}
}

// Music asks for its length and no singing in the words, as Google's docs say.
func TestMusicSaysLengthInWords(t *testing.T) {
	fake, key := setup(t)
	req := &aiservices.Request{Kind: aiservices.KindMusic, Model: "lyria-3.5", Prompt: "calm acoustic guitar", Options: aiservices.Options{Seconds: 15, Instrumental: true}}
	tk, err := submit(t, aiservices.KindMusic, key, req)
	if err != nil {
		t.Fatal(err)
	}
	p := fake.GenRequests()[0].Prompt
	if !strings.Contains(p, "about 15 seconds long") || !strings.Contains(p, "Instrumental only") {
		t.Errorf("prompt %q", p)
	}
	if o := tk.Ready.Outputs[0]; o.Text == "" || aiservices.Sniff(o.Data) == "" {
		t.Errorf("music %+v", o.MIME)
	}
}

// A long recording goes through the Files API and is deleted afterwards; the
// words come back with times and who speaks.
func TestTranscriptionUploadsLongRecordings(t *testing.T) {
	fake, key := setup(t)
	old := google.InlineMax
	google.InlineMax = 1000
	t.Cleanup(func() { google.InlineMax = old })
	in := aiconformance.WriteMedia(t, t.TempDir(), "talk.wav", "audio/wav", aiservices.RoleAudio, aiservices.ToneWAV(3, 250), 3)
	req := &aiservices.Request{Kind: aiservices.KindTranscription, Model: "gemini-3.5-transcribe", Inputs: []aiservices.Media{in}, Options: aiservices.Options{WordTimes: true, Language: "en"}}
	a, _ := aiservices.Get(google.Name, aiservices.KindTranscription)
	tk, err := a.Submit(context.Background(), key, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(tk.Files) != 1 || fake.FilesKept() != 1 {
		t.Fatalf("files %v kept %d", tk.Files, fake.FilesKept())
	}
	tr := tk.Ready.Outputs[0].Transcript
	if tr == nil || len(tr.Words) != 8 || tr.Words[4].Speaker != "spk_2" || tr.Words[1].Start != 0.4 {
		t.Fatalf("transcript %+v", tr)
	}
	if err := a.Forget(context.Background(), key, tk, tk.Ready.Outputs); err != nil || fake.FilesKept() != 0 {
		t.Fatalf("forget %v kept %d", err, fake.FilesKept())
	}
}

// A voice copy sends the sample and the consent reading, is stored at Google
// under its ID, and is deleted when withdrawn.
func TestVoiceCopyAndRemove(t *testing.T) {
	fake, key := setup(t)
	dir := t.TempDir()
	req := &aiservices.Request{Kind: aiservices.KindVoiceCopy, Model: "gemini-3.8-flash-tts", Options: aiservices.Options{Name: "Ada"},
		Inputs: []aiservices.Media{aiconformance.WriteMedia(t, dir, "s.wav", "audio/wav", aiservices.RoleSample, aiservices.ToneWAV(12, 200), 12),
			aiconformance.WriteMedia(t, dir, "c.wav", "audio/wav", aiservices.RoleConsent, aiservices.ToneWAV(6, 210), 6)}}
	a, _ := aiservices.Get(google.Name, aiservices.KindVoiceCopy)
	tk, err := a.Submit(context.Background(), key, req)
	if err != nil {
		t.Fatal(err)
	}
	id := tk.Ready.Outputs[0].VoiceID
	vs := fake.Voices()
	if len(vs) != 1 || vs[0].ID != id || !vs[0].Stored || vs[0].Name != "Ada" || vs[0].Consent == 0 {
		t.Fatalf("voices %+v", vs)
	}
	rm := a.(aiservices.VoiceRemover)
	if err := rm.RemoveVoice(context.Background(), key, id, req.Options); err != nil || !fake.Voices()[0].Deleted {
		t.Fatalf("remove %v %+v", err, fake.Voices())
	}
	// Too short a sample is Google's to refuse, in plain words.
	req.Inputs[0] = aiconformance.WriteMedia(t, dir, "short.wav", "audio/wav", aiservices.RoleSample, aiservices.ToneWAV(3, 200), 3)
	if _, err := a.Submit(context.Background(), key, req); kindOf(err) != videogen.KindInvalid {
		t.Errorf("short sample: %v", err)
	}
}

// Google's free tier and missing permissions are said for the kind of work, not for video.
func TestErrorsSpeakOfTheKind(t *testing.T) {
	_, key := setup(t)
	req := &aiservices.Request{Kind: aiservices.KindMusic, Model: "lyria-3.5", Prompt: "calm guitar"}
	a, _ := aiservices.Get(google.Name, aiservices.KindMusic)
	_, err := a.Submit(context.Background(), key, req)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if _, err := a.Fetch(context.Background(), key, &aiservices.Ticket{}, &aiservices.Output{}, &b); kindOf(err) != videogen.KindExpired {
		t.Errorf("fetch of nothing: %v", err)
	}
	req.Model = "melody-9"
	_, err = a.Submit(context.Background(), key, req)
	if kindOf(err) != videogen.KindRetired || strings.Contains(err.Error(), "video") {
		t.Errorf("unknown model: %v", err)
	}
}

// A 200 that can't be used (no words heard, no candidates) is never worth
// sending again: it is its own kind, and an uploaded recording is deleted
// on that path too.
func TestAnsweredButUnusable(t *testing.T) {
	fake, key := setup(t)
	old := google.InlineMax
	google.InlineMax = 1000
	t.Cleanup(func() { google.InlineMax = old })
	in := aiconformance.WriteMedia(t, t.TempDir(), "quiet.wav", "audio/wav", aiservices.RoleAudio, aiservices.ToneWAV(3, 250), 3)
	a, _ := aiservices.Get(google.Name, aiservices.KindTranscription)
	fake.Script(fakevideogen.Behavior{Kind: "silent"})
	_, err := a.Submit(context.Background(), key, &aiservices.Request{Kind: aiservices.KindTranscription, Model: "gemini-3.5-transcribe", Inputs: []aiservices.Media{in}})
	if kindOf(err) != videogen.KindUnusable {
		t.Fatalf("no words: %v", err)
	}
	if fake.FilesKept() != 0 {
		t.Errorf("the uploaded recording is still kept: %d", fake.FilesKept())
	}
	var ve *videogen.Error
	if errors.As(err, &ve) && ve.Retryable() {
		t.Error("an answered request must not be retried")
	}
	_, err = submit(t, aiservices.KindPicture, key, &aiservices.Request{Kind: aiservices.KindPicture, Model: "gemini-nano-banana-2.1", Prompt: "a kite [fake:empty]"})
	if kindOf(err) != videogen.KindUnusable {
		t.Errorf("no candidates: %v", err)
	}
}

// A test that forgets setup() can't reach Google: the real host is refused
// under go test, so nothing is sent and nothing is spent.
func TestForgottenSetupNeverReachesGoogle(t *testing.T) {
	t.Setenv(videogen.EnvGeminiBaseURL, "")
	for _, req := range []*aiservices.Request{
		{Kind: aiservices.KindPicture, Task: aiservices.TaskGenerate, Model: "gemini-nano-banana-2.1", Prompt: "a kite"},
		{Kind: aiservices.KindMusic, Model: "lyria-3.5", Prompt: "calm guitar", Options: aiservices.Options{Seconds: 15}},
		{Kind: aiservices.KindVoice, Model: "gemini-3.8-flash-tts", Prompt: "Hello there."},
	} {
		_, err := submit(t, req.Kind, fakeKey(), req)
		if err == nil || !strings.Contains(err.Error(), "Tests never call Google") {
			t.Errorf("%s without a fake: %v", req.Kind, err)
		}
	}
}
