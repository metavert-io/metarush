package google

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/videogen"
)

// picture makes or changes a picture (Nano Banana): the words, then the
// pictures inline; the size and shape in imageConfig.
func (a *Adapter) picture(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	parts := []part{{Text: req.Prompt}}
	for _, in := range req.InputsOf(aiservices.RolePicture) {
		b, err := inline(in)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part{InlineData: b})
	}
	img := map[string]any{}
	if req.Options.AspectRatio != "" {
		img["aspectRatio"] = req.Options.AspectRatio
	}
	if req.Options.Size != "" {
		img["imageSize"] = strings.ToUpper(req.Options.Size) // Google writes 1K, 2K, 4K
	}
	gc := map[string]any{"responseModalities": []string{"TEXT", "IMAGE"}}
	if len(img) > 0 {
		gc["imageConfig"] = img
	}
	out, usage, err := a.generate(ctx, key, req.Model, "make a picture", map[string]any{"contents": []content{{Role: "user", Parts: parts}}, "generationConfig": gc})
	if err != nil {
		return nil, err
	}
	st := &aiservices.Status{State: aiservices.StateDone, Usage: usage}
	var texts []string
	for _, p := range out {
		if p.InlineData != nil && strings.HasPrefix(p.InlineData.MimeType, "image/") {
			b, err := base64.StdEncoding.DecodeString(p.InlineData.Data)
			if err == nil && len(b) > 0 {
				st.Outputs = append(st.Outputs, aiservices.Output{MIME: p.InlineData.MimeType, Data: b})
			}
		} else if strings.TrimSpace(p.Text) != "" {
			texts = append(texts, strings.TrimSpace(p.Text))
		}
	}
	st.Text = strings.Join(texts, " ")
	if len(st.Outputs) == 0 {
		// Words and no picture: the model declined in its own words.
		return nil, &videogen.Error{Kind: videogen.KindSafety, Provider: Name, Message: "Google answered without a picture: the model declined it.", Hint: "Change the words or the pictures and try again.",
			Detail: firstLine(st.Text, 400), Safety: &videogen.Safety{Blocked: true, Side: "output", Category: "other", Message: firstLine(st.Text, 300)}}
	}
	return ready(st, nil), nil
}

// speech says words in a voice (Gemini TTS): a prebuilt voice by name, or a
// voice copy by its ID; how to say it as the part's style.
func (a *Adapter) speech(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	p := part{Text: req.Prompt}
	if s := strings.TrimSpace(req.Options.Style); s != "" {
		p.SpeechMetadata = &speechMetadata{Style: s}
	}
	sc := map[string]any{}
	if v := strings.TrimSpace(req.Options.Voice); v != "" {
		sc["voiceConfig"] = map[string]any{"voice": v}
	}
	if l := strings.TrimSpace(req.Options.Language); l != "" {
		sc["languageCode"] = l
	}
	gc := map[string]any{"responseModalities": []string{"AUDIO"}}
	if len(sc) > 0 {
		gc["speechConfig"] = sc
	}
	out, usage, err := a.generate(ctx, key, req.Model, "make AI speech", map[string]any{"contents": []content{{Role: "user", Parts: []part{p}}}, "generationConfig": gc})
	if err != nil {
		return nil, err
	}
	o, err := sound(out, "speech")
	if err != nil {
		return nil, err
	}
	usage.Known, usage.Seconds = true, aiservices.WAVSeconds(o.Data)
	usage.Characters = int64(len([]rune(req.Prompt)))
	return ready(&aiservices.Status{State: aiservices.StateDone, Usage: usage, Outputs: []aiservices.Output{*o}}, nil), nil
}

// sound finds the audio part and makes it a playable file (raw samples become WAV).
func sound(parts []part, what string) (*aiservices.Output, error) {
	var texts []string
	for _, p := range parts {
		if p.InlineData == nil || !strings.HasPrefix(strings.ToLower(p.InlineData.MimeType), "audio/") {
			if t := strings.TrimSpace(p.Text); t != "" {
				texts = append(texts, t)
			}
			continue
		}
		b, err := base64.StdEncoding.DecodeString(p.InlineData.Data)
		if err != nil || len(b) == 0 {
			continue
		}
		mime := p.InlineData.MimeType
		if aiservices.IsRawPCM(mime) {
			rate := aiservices.PCMRate(mime)
			if rate == 0 {
				rate = 24000 // Google's speech: 24 kHz, 16-bit, mono
			}
			b, mime = aiservices.PCMToWAV(b, rate, 1), "audio/wav"
		} else if s := aiservices.Sniff(b); s != "" {
			mime = s
		}
		return &aiservices.Output{MIME: mime, Data: b, Text: strings.Join(texts, "\n")}, nil
	}
	return nil, &videogen.Error{Kind: videogen.KindSafety, Provider: Name, Message: "Google answered without " + what + ": the model declined it.", Hint: "Change the words and try again.",
		Detail: firstLine(strings.Join(texts, " "), 400), Safety: &videogen.Safety{Blocked: true, Side: "output", Category: "other", Message: firstLine(strings.Join(texts, " "), 300)}}
}

// music makes a song (Lyria): its length and whether it has singing are said
// in the words, as Google's docs ask.
func (a *Adapter) music(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	prompt := strings.TrimSpace(req.Prompt)
	var asks []string
	if s := req.Options.Seconds; s > 0 {
		asks = append(asks, fmt.Sprintf("Make it about %s long.", lengthWords(s)))
	}
	if req.Options.Instrumental {
		asks = append(asks, "Instrumental only, no vocals.")
	}
	if len(asks) > 0 {
		prompt += "\n\n" + strings.Join(asks, " ")
	}
	parts := []part{{Text: prompt}}
	for _, in := range req.InputsOf(aiservices.RolePicture) {
		b, err := inline(in)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part{InlineData: b})
	}
	gc := map[string]any{"responseModalities": []string{"AUDIO", "TEXT"}}
	out, usage, err := a.generate(ctx, key, req.Model, "make music", map[string]any{"contents": []content{{Role: "user", Parts: parts}}, "generationConfig": gc})
	if err != nil {
		return nil, err
	}
	o, err := sound(out, "music")
	if err != nil {
		return nil, err
	}
	return ready(&aiservices.Status{State: aiservices.StateDone, Usage: usage, Outputs: []aiservices.Output{*o}}, nil), nil
}

func lengthWords(s float64) string {
	switch {
	case s >= 60 && math.Mod(s, 60) == 0:
		if s == 60 {
			return "1 minute"
		}
		return fmt.Sprintf("%.0f minutes", s/60)
	case s > 60:
		return fmt.Sprintf("%d minutes %d seconds", int(s)/60, int(s)%60)
	}
	return fmt.Sprintf("%.0f seconds", s)
}

// transcribe writes down a recording with who speaks and when each word is said.
func (a *Adapter) transcribe(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	in := req.Input(aiservices.RoleAudio)
	if in == nil {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "There's no recording to write down."}
	}
	p, file, err := a.media(ctx, key, *in)
	if err != nil {
		return nil, err
	}
	var files []string
	if file != "" {
		files = []string{file}
	}
	cfg := map[string]any{"diarization": true, "wordTimestamp": true}
	if l := strings.TrimSpace(req.Options.Language); l != "" {
		cfg["languageCodes"] = []string{l}
	}
	out, usage, err := a.generate(ctx, key, req.Model, "write down a recording", map[string]any{"contents": []content{{Role: "user", Parts: []part{p}}},
		"generationConfig": map[string]any{"audioTranscriptionConfig": cfg}})
	if err != nil {
		if len(files) > 0 {
			_ = a.Forget(context.WithoutCancel(ctx), key, &aiservices.Ticket{Files: files}, nil)
		}
		return nil, err
	}
	tr := &aiservices.Transcript{Language: req.Options.Language}
	var texts []string
	for _, p := range out {
		t := p.Transcription
		if t == nil {
			continue
		}
		for _, w := range t.Words {
			text := strings.TrimSpace(w.Word)
			if text == "" {
				continue
			}
			tr.Words = append(tr.Words, aiservices.Word{Text: text, Start: offset(w.StartOffset), End: offset(w.EndOffset), Speaker: t.SpeakerLabel})
		}
		if s := strings.TrimSpace(t.Text); s != "" {
			texts = append(texts, s)
		}
	}
	tr.Text = strings.Join(texts, " ")
	if tr.Text == "" {
		var ws []string
		for _, w := range tr.Words {
			ws = append(ws, w.Text)
		}
		tr.Text = strings.Join(ws, " ")
	}
	if len(tr.Words) == 0 {
		if len(files) > 0 {
			_ = a.Forget(context.WithoutCancel(ctx), key, &aiservices.Ticket{Files: files}, nil)
		}
		// Answered (and charged): never sent again.
		return nil, &videogen.Error{Kind: videogen.KindUnusable, Provider: Name, Message: "Google heard no words in the recording.",
			Hint: "Check that the clip has speech; nothing more is sent for it."}
	}
	usage.Known, usage.Seconds = true, in.Seconds
	return ready(&aiservices.Status{State: aiservices.StateDone, Usage: usage, Outputs: []aiservices.Output{{MIME: "application/json", Transcript: tr, Language: tr.Language}}}, files), nil
}

// voiceIDRe is the form of a voice copy's ID Google gives out.
var voiceIDRe = regexp.MustCompile(`^voice_[A-Za-z0-9_-]{1,200}$`)

// voiceBody is the Voices API's answer.
type voiceBody struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Model       string `json:"model"`
	DisplayName string `json:"display_name"`
	ExpireTime  string `json:"expire_time"`
}

// copyVoice makes a copy of a voice (Google's voice replication): a sample
// of the voice and the same person reading Google's consent script. The
// copy is stored at Google (store true) so speech can use it by its ID.
func (a *Adapter) copyVoice(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	sample, consent := req.Input(aiservices.RoleSample), req.Input(aiservices.RoleConsent)
	if sample == nil || consent == nil {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "Google needs a sample of the voice and a recording of the person reading its consent script."}
	}
	sb, err := inline(*sample)
	if err != nil {
		return nil, err
	}
	cb, err := inline(*consent)
	if err != nil {
		return nil, err
	}
	model := req.Model
	if model == "" {
		model = "gemini-3.8-flash-tts"
	}
	body := map[string]any{"store": true, "voice": map[string]any{"model": model, "type": "replicated", "display_name": aiservices.TaggedName(req.Options.Name, req.Tag),
		"replicated": map[string]any{"source_audio": map[string]any{"mime_type": sb.MimeType, "data": sb.Data}, "consent_audio": map[string]any{"mime_type": cb.MimeType, "data": cb.Data}}}}
	c, err := a.client(key)
	if err != nil {
		return nil, err
	}
	wrote := false
	res, err := c.call(ctx, "make a voice copy", http.MethodPost, "/v1beta/voices", body, 5*time.Minute, &wrote)
	if err != nil {
		if wrote {
			return nil, &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "The request reached Google, but no answer came back, so MetaRush can't tell whether the voice was made.", Detail: err.Error()}
		}
		return nil, err
	}
	if res.status >= 300 {
		return nil, classify(a.kind, model, res, "submit")
	}
	var v voiceBody
	if err := json.Unmarshal(res.body, &v); err != nil || !voiceIDRe.MatchString(v.ID) {
		return nil, &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "Google answered in a way MetaRush couldn't read, so it can't tell whether the voice was made.", Detail: firstLine(string(res.body), 300)}
	}
	return ready(&aiservices.Status{State: aiservices.StateDone, Usage: aiservices.Usage{Known: true}, Outputs: []aiservices.Output{{VoiceID: v.ID, Text: v.ExpireTime}}}, nil), nil
}

// FindVoices lists the voice copies at Google whose name carries MetaRush's
// tag (GET /v1beta/voices?type=replicated, page by page): a copy whose answer
// was lost may have been made all the same.
func (a *Adapter) FindVoices(ctx context.Context, key, tag string, _ aiservices.Options) ([]string, error) {
	if strings.TrimSpace(tag) == "" {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "MetaRush needs the tag it gave the voice to find it."}
	}
	c, err := a.client(key)
	if err != nil {
		return nil, err
	}
	var found []string
	page := ""
	for i := 0; i < 50; i++ {
		q := url.Values{"type": {"replicated"}, "page_size": {"100"}}
		if page != "" {
			q.Set("page_token", page)
		}
		res, err := c.call(ctx, "look for a voice copy", http.MethodGet, "/v1beta/voices?"+q.Encode(), nil, time.Minute, nil)
		if err != nil {
			return nil, err
		}
		if res.status >= 300 {
			return nil, classify(a.kind, "voice replication", res, "list")
		}
		var list struct {
			Voices []struct {
				voiceBody
				DisplayNameCamel string `json:"displayName"`
			} `json:"voices"`
			Next      string `json:"next_page_token"`
			NextCamel string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(res.body, &list); err != nil {
			return nil, &videogen.Error{Kind: videogen.KindServer, Provider: Name, Message: "Google answered in a way MetaRush couldn't read.", Detail: firstLine(string(res.body), 300)}
		}
		for _, v := range list.Voices {
			if voiceIDRe.MatchString(v.ID) && strings.Contains(v.DisplayName+" "+v.DisplayNameCamel, "["+tag+"]") {
				found = append(found, v.ID)
			}
		}
		if page = firstNonEmpty(list.Next, list.NextCamel); page == "" {
			break
		}
	}
	return found, nil
}

// RemoveVoice deletes a voice copy at Google.
func (a *Adapter) RemoveVoice(ctx context.Context, key, voiceID string, _ aiservices.Options) error {
	if !voiceIDRe.MatchString(voiceID) {
		return &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "That isn't a voice Google gave out."}
	}
	c, err := a.client(key)
	if err != nil {
		return err
	}
	res, err := c.call(ctx, "delete a voice copy", http.MethodDelete, "/v1beta/voices/"+url.PathEscape(voiceID), nil, time.Minute, nil)
	if err != nil {
		return err
	}
	if res.status >= 300 && res.status != http.StatusNotFound {
		return classify(a.kind, "voice replication", res, "delete")
	}
	return nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
