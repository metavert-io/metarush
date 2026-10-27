package elevenlabs_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiservices/aiconformance"
	"github.com/metavert-io/metarush/internal/aiservices/elevenlabs"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/testutil/fakeeleven"
	"github.com/metavert-io/metarush/internal/videogen"
)

// fakeKey is a key-shaped value made at run time (no literal for the secret scanner).
func fakeKey() string { return "xk-" + strings.Repeat("Rt8", 10) + "-fake" }

func setup(t *testing.T) (*fakeeleven.Server, string) {
	t.Helper()
	key := fakeKey()
	fake := fakeeleven.New(key)
	t.Cleanup(fake.Close)
	t.Setenv(providers.EnvElevenLabsBaseURL, fake.URL)
	return fake, key
}

// sample is the suite's request with one of the fake account's voices.
func sample(t *testing.T, m *aiservices.Model, dir string) *aiservices.Request {
	req := aiconformance.SampleRequest(t, m, dir)
	if req.Options.Voice != "" {
		req.Options.Voice = "voice_ada"
	}
	return req
}

// Every ElevenLabs kind passes the conformance suite against the fake.
func TestElevenLabsAdaptersConform(t *testing.T) {
	fake, key := setup(t)
	fake.SetDubPolls(2)
	for _, kind := range []string{aiservices.KindSoundEffect, aiservices.KindMusic, aiservices.KindVoice, aiservices.KindVoiceChange, aiservices.KindDubbing,
		aiservices.KindTranscription, aiservices.KindVoiceCopy} {
		t.Run(kind, func(t *testing.T) {
			a, err := aiservices.Get(elevenlabs.Name, kind)
			if err != nil {
				t.Fatal(err)
			}
			aiconformance.Run(t, aiconformance.Harness{Adapter: a, Key: key, Request: sample, Kept: fake.Kept})
		})
	}
}

func kindOf(err error) string {
	var ve *videogen.Error
	if errors.As(err, &ve) {
		return ve.Kind
	}
	return ""
}

func adapter(t *testing.T, kind string) aiservices.Adapter {
	a, err := aiservices.Get(elevenlabs.Name, kind)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Lengths go in the request as ElevenLabs takes them; what can't be deleted
// says so; no credits, the terms and a busy service are each said plainly.
func TestSoundAndMusicMapping(t *testing.T) {
	fake, key := setup(t)
	ctx := context.Background()
	tk, err := adapter(t, aiservices.KindMusic).Submit(ctx, key, &aiservices.Request{Kind: aiservices.KindMusic, Model: "music_v2", Prompt: "calm guitar",
		Options: aiservices.Options{Seconds: 12.5, Instrumental: true}})
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.MediaRequests(); len(got) != 1 || !strings.Contains(got[0], "music music_v2 12.5s instrumental=true") {
		t.Errorf("requests %v", got)
	}
	if tk.Extra["song"] == "" || tk.Ready.Usage.Seconds != 12.5 {
		t.Errorf("ticket %+v %+v", tk.Extra, tk.Ready.Usage)
	}
	if err := adapter(t, aiservices.KindMusic).Forget(ctx, key, tk, nil); !errors.Is(err, aiservices.ErrKept) {
		t.Errorf("forget music: %v", err)
	}
	sfx := adapter(t, aiservices.KindSoundEffect)
	for marker, kind := range map[string]string{"[fake:quota]": videogen.KindPermission, "[fake:tos]": videogen.KindSafety, "[fake:rate]": videogen.KindRate, "[fake:server]": videogen.KindServer} {
		_, err := sfx.Submit(ctx, key, &aiservices.Request{Kind: aiservices.KindSoundEffect, Model: "eleven_text_to_sound_v2", Prompt: "a door " + marker})
		if kindOf(err) != kind {
			t.Errorf("%s: %v", marker, err)
		}
	}
	_, err = sfx.Submit(ctx, "wrong-key-for-this-test", &aiservices.Request{Kind: aiservices.KindSoundEffect, Model: "eleven_text_to_sound_v2", Prompt: "a door"})
	if kindOf(err) != videogen.KindKey {
		t.Errorf("key: %v", err)
	}
}

// Speech and voice changes leave history items: found by the request's ID
// and deleted.
func TestHistoryIsDeleted(t *testing.T) {
	fake, key := setup(t)
	ctx := context.Background()
	a := adapter(t, aiservices.KindVoice)
	tk, err := a.Submit(ctx, key, &aiservices.Request{Kind: aiservices.KindVoice, Model: "eleven_flash_v2_5", Prompt: "Hello there.", Options: aiservices.Options{Voice: "voice_ada"}})
	if err != nil {
		t.Fatal(err)
	}
	if tk.Extra["request"] == "" || fake.Kept() != 1 {
		t.Fatalf("extra %v kept %d", tk.Extra, fake.Kept())
	}
	if err := a.Forget(ctx, key, tk, nil); err != nil || fake.Kept() != 0 {
		t.Fatalf("forget %v kept %d", err, fake.Kept())
	}
}

// A dub without the speakers' consent turns voice copies off; one with it
// keeps them; the dub is downloaded in its language and deleted.
func TestDubbingMapping(t *testing.T) {
	fake, key := setup(t)
	fake.SetDubPolls(1)
	ctx := context.Background()
	a := adapter(t, aiservices.KindDubbing)
	in := aiconformance.WriteMedia(t, t.TempDir(), "talk.flac", "audio/flac", aiservices.RoleMedia, aiservices.ToneWAV(3, 200), 3)
	for _, own := range []bool{false, true} {
		tk, err := a.Submit(ctx, key, &aiservices.Request{Kind: aiservices.KindDubbing, Model: "dubbing_v1", Inputs: []aiservices.Media{in}, Options: aiservices.Options{TargetLanguage: "es", OwnVoices: own}})
		if err != nil {
			t.Fatal(err)
		}
		st, err := a.Poll(ctx, key, tk)
		if err != nil || st.State != aiservices.StateDone || st.Outputs[0].Language != "es" {
			t.Fatalf("poll %+v %v", st, err)
		}
		if err := a.Forget(ctx, key, tk, st.Outputs); err != nil {
			t.Fatal(err)
		}
	}
	reqs := fake.MediaRequests()
	if len(reqs) != 2 || !strings.Contains(reqs[0], "cloning=false watermark=false") || !strings.Contains(reqs[1], "cloning=true") {
		t.Errorf("requests %v", reqs)
	}
	if fake.Kept() != 0 {
		t.Errorf("dubs kept %d", fake.Kept())
	}
}

// Speech to text keeps words (never spaces or sound events) with who speaks.
func TestSpeechToTextWords(t *testing.T) {
	_, key := setup(t)
	in := aiconformance.WriteMedia(t, t.TempDir(), "talk.flac", "audio/flac", aiservices.RoleAudio, aiservices.ToneWAV(4, 200), 4)
	tk, err := adapter(t, aiservices.KindTranscription).Submit(context.Background(), key, &aiservices.Request{Kind: aiservices.KindTranscription, Model: "scribe_v2",
		Inputs: []aiservices.Media{in}, Options: aiservices.Options{WordTimes: true, Language: "en-US"}})
	if err != nil {
		t.Fatal(err)
	}
	tr := tk.Ready.Outputs[0].Transcript
	if len(tr.Words) != 6 || tr.Words[3].Speaker != "speaker_1" || tr.Language != "en" || tk.Extra["transcript"] == "" {
		t.Fatalf("transcript %+v %v", tr, tk.Extra)
	}
}

// Speech to text that heard nothing answered (and charged): its own kind,
// never retried, and the transcript ElevenLabs kept is deleted.
func TestNoWordsIsUnusable(t *testing.T) {
	fake, key := setup(t)
	in := aiconformance.WriteMedia(t, t.TempDir(), "quiet [fake:silent].flac", "audio/flac", aiservices.RoleAudio, aiservices.ToneWAV(2, 200), 2)
	in.Name = "quiet [fake:silent].flac"
	_, err := adapter(t, aiservices.KindTranscription).Submit(context.Background(), key, &aiservices.Request{Kind: aiservices.KindTranscription, Model: "scribe_v2", Inputs: []aiservices.Media{in}})
	if kindOf(err) != videogen.KindUnusable {
		t.Fatalf("no words: %v", err)
	}
	if fake.Kept() != 0 {
		t.Errorf("ElevenLabs still keeps the transcript: %d", fake.Kept())
	}
}

// A test that forgets setup() can't reach ElevenLabs: every real host is
// refused under go test, so nothing is sent and nothing is spent.
func TestForgottenSetupNeverReachesElevenLabs(t *testing.T) {
	t.Setenv(providers.EnvElevenLabsBaseURL, "")
	ctx := context.Background()
	for _, req := range []*aiservices.Request{
		{Kind: aiservices.KindSoundEffect, Model: "eleven_text_to_sound_v2", Prompt: "a door"},
		{Kind: aiservices.KindMusic, Model: "music_v2", Prompt: "calm guitar", Options: aiservices.Options{Seconds: 15}},
		{Kind: aiservices.KindVoice, Model: "eleven_flash_v2_5", Prompt: "Hello there.", Options: aiservices.Options{Voice: "voice_ada"}},
	} {
		_, err := adapter(t, req.Kind).Submit(ctx, fakeKey(), req)
		if err == nil || !strings.Contains(err.Error(), "Tests never call ElevenLabs") {
			t.Errorf("%s without a fake: %v", req.Kind, err)
		}
	}
}
