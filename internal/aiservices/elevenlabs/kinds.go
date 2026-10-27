package elevenlabs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/videogen"
)

// Submit sends the request: most kinds answer with the result; a dub returns its ID.
func (a *Adapter) Submit(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	var t *aiservices.Ticket
	var err error
	switch a.kind {
	case aiservices.KindSoundEffect:
		t, err = a.soundEffect(ctx, key, req)
	case aiservices.KindMusic:
		t, err = a.music(ctx, key, req)
	case aiservices.KindVoice:
		t, err = a.speech(ctx, key, req)
	case aiservices.KindVoiceChange:
		t, err = a.voiceChange(ctx, key, req)
	case aiservices.KindTranscription:
		t, err = a.transcribe(ctx, key, req)
	case aiservices.KindDubbing:
		t, err = a.dub(ctx, key, req)
	case aiservices.KindVoiceCopy:
		t, err = a.copyVoice(ctx, key, req)
	default:
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "MetaRush can't do that with ElevenLabs."}
	}
	if t != nil && req.Options.Region != "" {
		if t.Extra == nil {
			t.Extra = map[string]string{}
		}
		t.Extra["region"] = req.Options.Region
	}
	return t, err
}

func query(format string) string { return "?output_format=" + url.QueryEscape(format) }

// soundEffect: POST /v1/sound-generation (JSON); the length when asked, else ElevenLabs chooses.
func (a *Adapter) soundEffect(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	body := map[string]any{"text": req.Prompt, "model_id": req.Model}
	if s := req.Options.Seconds; s > 0 {
		body["duration_seconds"] = s
	}
	if req.Options.Loop {
		body["loop"] = true
	}
	rd, ct := jsonBody(body)
	res, err := a.post(ctx, key, req.Options.Region, "make a sound effect", "/v1/sound-generation"+query(OutputFormat), rd, ct, req.Model, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	u := aiservices.Usage{}
	if s := req.Options.Seconds; s > 0 {
		u = aiservices.Usage{Known: true, Seconds: s}
	}
	return audio(res, u, nil)
}

// music: POST /v1/music (JSON) with the length in milliseconds and no singing when asked.
func (a *Adapter) music(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	body := map[string]any{"prompt": req.Prompt, "model_id": req.Model}
	if s := req.Options.Seconds; s > 0 {
		body["music_length_ms"] = int(math.Round(s * 1000))
	}
	if req.Options.Instrumental {
		body["force_instrumental"] = true
	}
	rd, ct := jsonBody(body)
	res, err := a.post(ctx, key, req.Options.Region, "make music", "/v1/music"+query(OutputFormat), rd, ct, req.Model, 15*time.Minute)
	if err != nil {
		return nil, err
	}
	u := aiservices.Usage{}
	if s := req.Options.Seconds; s > 0 {
		u = aiservices.Usage{Known: true, Seconds: s}
	}
	var extra map[string]string
	if id := res.header.Get("song-id"); id != "" {
		extra = map[string]string{"song": id}
	}
	return audio(res, u, extra)
}

// speech: POST /v1/text-to-speech/{voice} (JSON).
func (a *Adapter) speech(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	voice := strings.TrimSpace(req.Options.Voice)
	if voice == "" {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "Choose the ElevenLabs voice to say it."}
	}
	body := map[string]any{"text": req.Prompt, "model_id": req.Model}
	if l := strings.TrimSpace(req.Options.Language); l != "" {
		body["language_code"] = strings.SplitN(l, "-", 2)[0]
	}
	rd, ct := jsonBody(body)
	res, err := a.post(ctx, key, req.Options.Region, "make speech", "/v1/text-to-speech/"+url.PathEscape(voice)+query(OutputFormat), rd, ct, req.Model, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	return audio(res, aiservices.Usage{Known: true, Characters: int64(len([]rune(req.Prompt)))}, map[string]string{"voice": voice, "source": "TTS"})
}

// voiceChange: POST /v1/voice-changer/{voice} (multipart: the recording and the model).
func (a *Adapter) voiceChange(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	voice := strings.TrimSpace(req.Options.Voice)
	in := req.Input(aiservices.RoleAudio)
	if voice == "" || in == nil {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "Choose the recording and the voice to say it in."}
	}
	rd, ct, err := form(map[string]string{"model_id": req.Model, "file_format": "other"}, map[string][]aiservices.Media{"audio": {*in}})
	if err != nil {
		return nil, err
	}
	res, err := a.post(ctx, key, req.Options.Region, "change a voice", "/v1/voice-changer/"+url.PathEscape(voice)+query(OutputFormat), rd, ct, req.Model, 10*time.Minute)
	if err != nil {
		return nil, err
	}
	return audio(res, aiservices.Usage{Known: true, Seconds: in.Seconds}, map[string]string{"voice": voice, "source": "STS"})
}

// sttWord is a word speech to text heard.
type sttWord struct {
	Text      string   `json:"text"`
	Type      string   `json:"type"`
	Start     *float64 `json:"start"`
	End       *float64 `json:"end"`
	SpeakerID string   `json:"speaker_id"`
}

// transcribe: POST /v1/speech-to-text (multipart) with who speaks and word times.
func (a *Adapter) transcribe(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	in := req.Input(aiservices.RoleAudio)
	if in == nil {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "There's no recording to write down."}
	}
	fields := map[string]string{"model_id": req.Model, "diarize": "true", "timestamps_granularity": "word", "tag_audio_events": "false", "file_format": "other"}
	if l := strings.TrimSpace(req.Options.Language); l != "" {
		fields["language_code"] = strings.SplitN(l, "-", 2)[0]
	}
	if n := req.Options.Speakers; n > 0 {
		fields["num_speakers"] = strconv.Itoa(n)
	}
	rd, ct, err := form(fields, map[string][]aiservices.Media{"file": {*in}})
	if err != nil {
		return nil, err
	}
	res, err := a.post(ctx, key, req.Options.Region, "write down a recording", "/v1/speech-to-text", rd, ct, req.Model, 30*time.Minute)
	if err != nil {
		return nil, err
	}
	var out struct {
		LanguageCode    string    `json:"language_code"`
		Text            string    `json:"text"`
		Words           []sttWord `json:"words"`
		TranscriptionID string    `json:"transcription_id"`
	}
	if err := json.Unmarshal(res.body, &out); err != nil {
		return nil, &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "ElevenLabs answered in a way MetaRush couldn't read.", Detail: firstLine(string(res.body), 300)}
	}
	tr := &aiservices.Transcript{Language: out.LanguageCode, Text: strings.TrimSpace(out.Text)}
	for _, w := range out.Words {
		if w.Type != "" && w.Type != "word" || w.Start == nil || w.End == nil || strings.TrimSpace(w.Text) == "" {
			continue // spaces and sound events
		}
		tr.Words = append(tr.Words, aiservices.Word{Text: strings.TrimSpace(w.Text), Start: *w.Start, End: *w.End, Speaker: w.SpeakerID})
	}
	if len(tr.Words) == 0 {
		if out.TranscriptionID != "" {
			_ = a.deleteTranscript(context.WithoutCancel(ctx), key, &aiservices.Ticket{Extra: map[string]string{"region": req.Options.Region}}, out.TranscriptionID)
		}
		// Answered (and charged): never sent again.
		return nil, &videogen.Error{Kind: videogen.KindUnusable, Provider: Name, Message: "ElevenLabs heard no words in the recording.",
			Hint: "Check that the clip has speech; nothing more is sent for it."}
	}
	t := &aiservices.Ticket{SubmittedAt: time.Now().UTC(), Ready: &aiservices.Status{State: aiservices.StateDone, Usage: aiservices.Usage{Known: true, Seconds: in.Seconds},
		Outputs: []aiservices.Output{{MIME: "application/json", Transcript: tr, Language: tr.Language}}}}
	if out.TranscriptionID != "" {
		t.Extra = map[string]string{"transcript": out.TranscriptionID}
	}
	return t, nil
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// dub: POST /v1/dubbing (multipart). Without the speakers' consent to copy
// their voices, voice copies are turned off: ElevenLabs uses similar voices
// from its library. No watermark (the price MetaRush shows).
func (a *Adapter) dub(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	in := req.Input(aiservices.RoleMedia)
	target := strings.TrimSpace(req.Options.TargetLanguage)
	if in == nil || target == "" {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "Choose the recording and the language to dub it into."}
	}
	fields := map[string]string{"target_lang": target, "watermark": "false", "name": "MetaRush dub", "num_speakers": strconv.Itoa(max(0, req.Options.Speakers)),
		"disable_voice_cloning": strconv.FormatBool(!req.Options.OwnVoices)}
	if l := strings.TrimSpace(req.Options.Language); l != "" {
		fields["source_lang"] = l
	}
	rd, ct, err := form(fields, map[string][]aiservices.Media{"file": {*in}})
	if err != nil {
		return nil, err
	}
	res, err := a.post(ctx, key, req.Options.Region, "start a dub", "/v1/dubbing", rd, ct, req.Model, 30*time.Minute)
	if err != nil {
		return nil, err
	}
	var out struct {
		DubbingID           string  `json:"dubbing_id"`
		ExpectedDurationSec float64 `json:"expected_duration_sec"`
	}
	if err := json.Unmarshal(res.body, &out); err != nil || !idRe.MatchString(out.DubbingID) {
		return nil, &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "ElevenLabs answered in a way MetaRush couldn't read, so it can't tell whether the dub was started.", Detail: firstLine(string(res.body), 300)}
	}
	extra := map[string]string{"language": target}
	if out.ExpectedDurationSec > 0 {
		extra["expected"] = strconv.FormatFloat(out.ExpectedDurationSec, 'f', 1, 64)
	}
	return &aiservices.Ticket{ID: out.DubbingID, SubmittedAt: time.Now().UTC(), Extra: extra}, nil
}

func (a *Adapter) pollDub(ctx context.Context, key string, t *aiservices.Ticket) (*aiservices.Status, error) {
	if !idRe.MatchString(t.ID) {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "That isn't a dub ElevenLabs started."}
	}
	c, err := a.client(key, region(t))
	if err != nil {
		return nil, err
	}
	res, err := c.call(ctx, "check on a dub", http.MethodGet, "/v1/dubbing/"+url.PathEscape(t.ID), nil, "", time.Minute, nil)
	if err != nil {
		return nil, err
	}
	if res.status == http.StatusNotFound {
		return nil, &videogen.Error{Kind: videogen.KindExpired, Provider: Name, Message: "ElevenLabs no longer has this dub."}
	}
	if res.status >= 300 {
		return nil, classify(res, "")
	}
	var d struct {
		Status          string   `json:"status"`
		TargetLanguages []string `json:"target_languages"`
		Error           string   `json:"error"`
	}
	if err := json.Unmarshal(res.body, &d); err != nil {
		return nil, &videogen.Error{Kind: videogen.KindServer, Provider: Name, Message: "ElevenLabs answered in a way MetaRush couldn't read. It asks again in a moment."}
	}
	lang := t.Extra["language"]
	switch strings.ToLower(d.Status) {
	case "dubbed":
		return &aiservices.Status{State: aiservices.StateDone, Usage: aiservices.Usage{},
			Outputs: []aiservices.Output{{MIME: "audio/mpeg", URI: "/v1/dubbing/" + t.ID + "/audio/" + lang, Language: lang}}}, nil
	case "failed":
		return &aiservices.Status{State: aiservices.StateFailed, Err: &videogen.Error{Kind: videogen.KindServer, Provider: Name, Message: "ElevenLabs couldn't dub it.", Detail: firstLine(d.Error, 400)}}, nil
	}
	st := &aiservices.Status{State: aiservices.StateRunning}
	if exp, err := strconv.ParseFloat(t.Extra["expected"], 64); err == nil && exp > 0 {
		st.Progress = math.Min(0.95, time.Since(t.SubmittedAt).Seconds()/exp)
	}
	return st, nil
}

func (a *Adapter) fetchDub(ctx context.Context, key string, t *aiservices.Ticket, out *aiservices.Output, dst io.Writer) (int64, error) {
	lang := firstNonEmpty(out.Language, t.Extra["language"])
	if !idRe.MatchString(t.ID) || !idRe.MatchString(lang) {
		return 0, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "That isn't a dub ElevenLabs made."}
	}
	c, err := a.client(key, region(t))
	if err != nil {
		return 0, err
	}
	res, err := c.call(ctx, "download a dub", http.MethodGet, "/v1/dubbing/"+url.PathEscape(t.ID)+"/audio/"+url.PathEscape(lang), nil, "", 30*time.Minute, nil)
	if err != nil {
		return 0, err
	}
	switch {
	case res.status == http.StatusNotFound:
		return 0, &videogen.Error{Kind: videogen.KindExpired, Provider: Name, Message: "ElevenLabs no longer has the finished dub.", Hint: "Dub it again."}
	case res.status >= 300:
		return 0, classify(res, "")
	case len(res.body) == 0:
		return 0, &videogen.Error{Kind: videogen.KindServer, Provider: Name, Message: "ElevenLabs sent an empty dub. MetaRush tries again by itself."}
	}
	n, err := dst.Write(res.body)
	return int64(n), err
}

func (a *Adapter) deleteDub(ctx context.Context, key string, t *aiservices.Ticket) error {
	if !idRe.MatchString(t.ID) {
		return nil
	}
	c, err := a.client(key, region(t))
	if err != nil {
		return err
	}
	res, err := c.call(ctx, "delete a dub", http.MethodDelete, "/v1/dubbing/"+url.PathEscape(t.ID), nil, "", time.Minute, nil)
	if err != nil {
		return err
	}
	if res.status >= 300 && res.status != http.StatusNotFound {
		return classify(res, "")
	}
	return nil
}

func (a *Adapter) deleteTranscript(ctx context.Context, key string, t *aiservices.Ticket, id string) error {
	if !idRe.MatchString(id) {
		return nil
	}
	c, err := a.client(key, region(t))
	if err != nil {
		return err
	}
	res, err := c.call(ctx, "delete a transcript", http.MethodDelete, "/v1/speech-to-text/transcripts/"+url.PathEscape(id), nil, "", time.Minute, nil)
	if err != nil {
		return err
	}
	if res.status >= 300 && res.status != http.StatusNotFound {
		return classify(res, "")
	}
	return nil
}

// deleteHistory deletes the history item speech or a voice change left,
// found by the request's ID among the newest items of that voice.
func (a *Adapter) deleteHistory(ctx context.Context, key string, t *aiservices.Ticket) error {
	rid := t.Extra["request"]
	if rid == "" {
		return fmt.Errorf("ElevenLabs didn't say which request it was, so its history item couldn't be found to delete")
	}
	c, err := a.client(key, region(t))
	if err != nil {
		return err
	}
	q := url.Values{"page_size": {"100"}}
	if v := t.Extra["voice"]; v != "" {
		q.Set("voice_id", v)
	}
	if s := t.Extra["source"]; s != "" {
		q.Set("source", s)
	}
	res, err := c.call(ctx, "find a history item", http.MethodGet, "/v1/history?"+q.Encode(), nil, "", time.Minute, nil)
	if err != nil {
		return err
	}
	if res.status >= 300 {
		return classify(res, "")
	}
	var h struct {
		History []struct {
			HistoryItemID string `json:"history_item_id"`
			RequestID     string `json:"request_id"`
		} `json:"history"`
	}
	if err := json.Unmarshal(res.body, &h); err != nil {
		return fmt.Errorf("ElevenLabs' history couldn't be read")
	}
	for _, it := range h.History {
		if it.RequestID != rid || !idRe.MatchString(it.HistoryItemID) {
			continue
		}
		res, err := c.call(ctx, "delete a history item", http.MethodDelete, "/v1/history/"+url.PathEscape(it.HistoryItemID), nil, "", time.Minute, nil)
		if err != nil {
			return err
		}
		if res.status >= 300 && res.status != http.StatusNotFound {
			return classify(res, "")
		}
		return nil
	}
	return fmt.Errorf("the request wasn't among ElevenLabs' newest history items, so it couldn't be deleted there")
}

// copyVoice: POST /v1/voices/add (multipart): the sample as files. ElevenLabs
// takes no consent recording; MetaRush's consent record stands for it.
func (a *Adapter) copyVoice(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	samples := req.InputsOf(aiservices.RoleSample)
	if len(samples) == 0 {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "Choose a recording of the voice to copy."}
	}
	rd, ct, err := form(map[string]string{"name": aiservices.TaggedName(req.Options.Name, req.Tag), "description": "Made with MetaRush, with the speaker's recorded consent."},
		map[string][]aiservices.Media{"files": samples})
	if err != nil {
		return nil, err
	}
	res, err := a.post(ctx, key, req.Options.Region, "copy a voice", "/v1/voices/add", rd, ct, req.Model, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	var out struct {
		VoiceID              string `json:"voice_id"`
		RequiresVerification bool   `json:"requires_verification"`
	}
	if err := json.Unmarshal(res.body, &out); err != nil || !idRe.MatchString(out.VoiceID) {
		return nil, &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "ElevenLabs answered in a way MetaRush couldn't read, so it can't tell whether the voice was made.", Detail: firstLine(string(res.body), 300)}
	}
	o := aiservices.Output{VoiceID: out.VoiceID}
	if out.RequiresVerification {
		o.Text = "ElevenLabs asks you to confirm the voice in your ElevenLabs account before it is used."
	}
	return &aiservices.Ticket{SubmittedAt: time.Now().UTC(), Ready: &aiservices.Status{State: aiservices.StateDone, Usage: aiservices.Usage{Known: true}, Outputs: []aiservices.Output{o}}}, nil
}

// FindVoices lists the voices in the account whose name carries MetaRush's
// tag (GET /v2/voices?search=…, page by page): a copy whose answer was lost
// may have been made all the same.
func (a *Adapter) FindVoices(ctx context.Context, key, tag string, o aiservices.Options) ([]string, error) {
	if strings.TrimSpace(tag) == "" {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "MetaRush needs the tag it gave the voice to find it."}
	}
	c, err := a.client(key, o.Region)
	if err != nil {
		return nil, err
	}
	var found []string
	page := ""
	for i := 0; i < 50; i++ {
		q := url.Values{"search": {tag}, "page_size": {"100"}}
		if page != "" {
			q.Set("next_page_token", page)
		}
		res, err := c.call(ctx, "look for a voice copy", http.MethodGet, "/v2/voices?"+q.Encode(), nil, "", time.Minute, nil)
		if err != nil {
			return nil, err
		}
		if res.status >= 300 {
			return nil, classify(res, "")
		}
		var list struct {
			Voices []struct {
				VoiceID     string `json:"voice_id"`
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"voices"`
			HasMore bool    `json:"has_more"`
			Next    *string `json:"next_page_token"`
		}
		if err := json.Unmarshal(res.body, &list); err != nil {
			return nil, &videogen.Error{Kind: videogen.KindServer, Provider: Name, Message: "ElevenLabs answered in a way MetaRush couldn't read.", Detail: firstLine(string(res.body), 300)}
		}
		for _, v := range list.Voices {
			if idRe.MatchString(v.VoiceID) && strings.Contains(v.Name+" "+v.Description, "["+tag+"]") {
				found = append(found, v.VoiceID)
			}
		}
		if !list.HasMore || list.Next == nil || *list.Next == "" {
			break
		}
		page = *list.Next
	}
	return found, nil
}

// RemoveVoice deletes a voice copy at ElevenLabs.
func (a *Adapter) RemoveVoice(ctx context.Context, key, voiceID string, o aiservices.Options) error {
	if !idRe.MatchString(voiceID) {
		return &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "That isn't a voice ElevenLabs gave out."}
	}
	c, err := a.client(key, o.Region)
	if err != nil {
		return err
	}
	res, err := c.call(ctx, "delete a voice copy", http.MethodDelete, "/v1/voices/"+url.PathEscape(voiceID), nil, "", time.Minute, nil)
	if err != nil {
		return err
	}
	if res.status >= 300 && res.status != http.StatusNotFound {
		return classify(res, "")
	}
	return nil
}
