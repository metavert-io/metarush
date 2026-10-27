package fakevideogen

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The rest of the Gemini API MetaRush uses (D224): generateContent for
// pictures (Nano Banana), speech (Gemini TTS), music (Lyria) and
// transcription, and the Voices API for voice copies. It answers like
// Google's docs of 25 Oct 2026 show, with small real files (PNG, WAV), and
// misbehaves on a marker in the words: [fake:safety] (the prompt blocked),
// [fake:image-safety] (no picture, a safety finish), [fake:refuse] (words
// and no result), [fake:rate] (a 429), [fake:server] (a 500), [fake:pcm]
// (speech as raw samples, as older models sent it), [fake:silent] (a 200 with no words or result), [fake:empty] (a 200
// without candidates). Behaviors queued with Script apply too.

// GenRequest is one generateContent request the fake received.
type GenRequest struct {
	Model      string
	Prompt     string
	Inline     int // inline files
	Files      []string
	Store      *bool
	Config     map[string]any
	Behavior   Behavior
	ReceivedAt time.Time
}

// Voice is a voice copy the fake made.
type Voice struct {
	ID       string
	Model    string
	Name     string
	Sample   int // bytes
	Consent  int
	Deleted  bool
	Stored   bool
	MadeFrom string
}

type mediaState struct {
	gens   []GenRequest
	voices map[string]*Voice
}

func (s *Server) mediaRoutes(mux *http.ServeMux) {
	s.media.voices = map[string]*Voice{}
	mux.HandleFunc("POST /v1beta/models/{name}", s.auth("generate", s.generateContent))
	mux.HandleFunc("POST /v1beta/voices", s.auth("voice-create", s.voiceCreate))
	mux.HandleFunc("GET /v1beta/voices", s.auth("voice-list", s.voiceList))
	mux.HandleFunc("GET /v1beta/voices/{id}", s.auth("voice-get", s.voiceGet))
	mux.HandleFunc("DELETE /v1beta/voices/{id}", s.auth("voice-delete", s.voiceDelete))
}

// GenRequests lists the generateContent requests received, oldest first.
func (s *Server) GenRequests() []GenRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]GenRequest{}, s.media.gens...)
}

// Voices lists the voice copies the fake made.
func (s *Server) Voices() []Voice {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Voice
	for _, v := range s.media.voices {
		out = append(out, *v)
	}
	return out
}

// FilesKept counts uploaded files not deleted.
func (s *Server) FilesKept() int {
	_, f := s.Kept()
	return f
}

type genPart struct {
	Text       string `json:"text"`
	InlineData *struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
	} `json:"inlineData"`
	FileData *struct {
		MimeType string `json:"mimeType"`
		FileURI  string `json:"fileUri"`
	} `json:"fileData"`
}

var lengthRe = regexp.MustCompile(`about (\d+) (seconds|minutes?)`)

func (s *Server) generateContent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	model, method, ok := strings.Cut(name, ":")
	if !ok || method != "generateContent" {
		writeJSON(w, http.StatusNotFound, gerr(404, "NOT_FOUND", "Method not found."))
		return
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	var body struct {
		Contents []struct {
			Parts []genPart `json:"parts"`
		} `json:"contents"`
		GenerationConfig map[string]any `json:"generationConfig"`
		Store            *bool          `json:"store"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Contents) == 0 {
		writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "Invalid JSON payload received."))
		return
	}
	g := GenRequest{Model: model, Store: body.Store, Config: body.GenerationConfig, ReceivedAt: time.Now()}
	h := sha256.New()
	var audio []byte
	for _, p := range body.Contents[0].Parts {
		g.Prompt += p.Text
		h.Write([]byte(p.Text))
		if p.InlineData != nil {
			b, err := base64.StdEncoding.DecodeString(p.InlineData.Data)
			if err != nil || len(b) == 0 {
				writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "Invalid inline data."))
				return
			}
			g.Inline++
			h.Write(b)
			if strings.HasPrefix(p.InlineData.MimeType, "audio/") {
				audio = b
			}
		}
		if p.FileData != nil {
			fname := "files/" + strings.TrimPrefix(fileRe.FindString(p.FileData.FileURI), "/files/")
			s.mu.Lock()
			f := s.files[fname]
			s.mu.Unlock()
			if f == nil || f.Deleted {
				writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "File "+fname+" is not found or not ACTIVE."))
				return
			}
			g.Files = append(g.Files, fname)
			audio = f.Data
			h.Write(f.Data)
		}
	}
	s.mu.Lock()
	g.Behavior = s.next(g.Prompt)
	s.media.gens = append(s.media.gens, g)
	if body.Store == nil || *body.Store {
		s.problems = append(s.problems, "a generateContent request Google would keep a log of (store not false)")
	}
	s.mu.Unlock()
	sum := h.Sum(nil)
	usage := map[string]any{"promptTokenCount": 40, "candidatesTokenCount": 1290, "totalTokenCount": 1330}
	switch g.Behavior.Kind {
	case Rate:
		writeJSON(w, http.StatusTooManyRequests, gerr(429, "RESOURCE_EXHAUSTED", "Resource has been exhausted (e.g. check quota)."))
		return
	case ServerError:
		writeJSON(w, http.StatusInternalServerError, gerr(500, "INTERNAL", "An internal error has occurred."))
		return
	case "safety":
		writeJSON(w, http.StatusOK, map[string]any{"promptFeedback": map[string]any{"blockReason": "SAFETY"}, "usageMetadata": usage})
		return
	case "image-safety":
		writeJSON(w, http.StatusOK, map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{}}, "finishReason": "IMAGE_SAFETY"}}, "usageMetadata": usage})
		return
	case "refuse":
		writeJSON(w, http.StatusOK, map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": "I can't make that."}}}, "finishReason": "STOP"}}, "usageMetadata": usage})
		return
	case "silent": // a 200 with nothing usable: no words heard, no result
		writeJSON(w, http.StatusOK, map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": "(silence)"}}}, "finishReason": "STOP"}}, "usageMetadata": usage})
		return
	case "empty": // a 200 without candidates
		writeJSON(w, http.StatusOK, map[string]any{"usageMetadata": usage})
		return
	}
	var parts []any
	switch {
	case strings.Contains(model, "image") || strings.Contains(model, "banana"):
		ic, _ := body.GenerationConfig["imageConfig"].(map[string]any)
		ar, _ := ic["aspectRatio"].(string)
		size, _ := ic["imageSize"].(string)
		if size != "" && size != "1K" && size != "2K" && size != "4K" && size != "512" {
			writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "Invalid image size "+size+"."))
			return
		}
		parts = []any{map[string]any{"text": "Here is the picture."}, map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(picturePNG(sum, ar))}}}
	case strings.Contains(model, "tts"):
		secs := float64(len([]rune(g.Prompt)))/14 + 0.3
		wav := toneWAV(secs, 200+float64(sum[0]))
		mime, data := "audio/wav", wav
		if g.Behavior.Kind == "pcm" {
			mime, data = "audio/L16;codec=pcm;rate=16000", wav[44:]
		}
		parts = []any{map[string]any{"inlineData": map[string]any{"mimeType": mime, "data": base64.StdEncoding.EncodeToString(data)}}}
	case strings.Contains(model, "lyria"):
		secs := 30.0
		if m := lengthRe.FindStringSubmatch(g.Prompt); m != nil {
			n, _ := strconv.ParseFloat(m[1], 64)
			secs = n
			if strings.HasPrefix(m[2], "minute") {
				secs = n * 60
			}
		}
		secs = min(secs, 20) // small files for tests
		parts = []any{map[string]any{"text": "[Verse] a calm melody"}, map[string]any{"inlineData": map[string]any{"mimeType": "audio/wav", "data": base64.StdEncoding.EncodeToString(toneWAV(secs, 300+float64(sum[1])))}}}
	case strings.Contains(model, "transcribe"):
		if len(audio) == 0 {
			writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "No audio to transcribe."))
			return
		}
		cfg, _ := body.GenerationConfig["audioTranscriptionConfig"].(map[string]any)
		if ts, _ := cfg["wordTimestamp"].(bool); !ts {
			writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "Expected word timestamps."))
			return
		}
		words := strings.Fields("hello from the fake google transcriber and goodbye")
		var a, b []any
		for i, word := range words {
			wd := map[string]any{"word": word, "startOffset": fmt.Sprintf("%.3fs", float64(i)*0.4), "endOffset": fmt.Sprintf("%.3fs", float64(i)*0.4+0.3)}
			if i < 4 {
				a = append(a, wd)
			} else {
				b = append(b, wd)
			}
		}
		parts = []any{map[string]any{"audioTranscription": map[string]any{"speakerLabel": "spk_1", "text": strings.Join(words[:4], " "), "words": a}},
			map[string]any{"audioTranscription": map[string]any{"speakerLabel": "spk_2", "text": strings.Join(words[4:], " "), "words": b}}}
	default:
		writeJSON(w, http.StatusNotFound, gerr(404, "NOT_FOUND", "models/"+model+" is not found for API version v1beta, or is not supported for generateContent."))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "finishReason": "STOP"}}, "usageMetadata": usage,
		"modelVersion": model})
}

func picturePNG(seed []byte, aspect string) []byte {
	w, h := 64, 64
	switch aspect {
	case "16:9":
		w, h = 96, 54
	case "9:16":
		w, h = 54, 96
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{seed[0], uint8(x * 2), uint8(y * 2), 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func (s *Server) voiceCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Store *bool `json:"store"`
		Voice struct {
			Model       string `json:"model"`
			Type        string `json:"type"`
			DisplayName string `json:"display_name"`
			Replicated  struct {
				Source struct {
					MimeType string `json:"mime_type"`
					Data     string `json:"data"`
				} `json:"source_audio"`
				Consent struct {
					MimeType string `json:"mime_type"`
					Data     string `json:"data"`
				} `json:"consent_audio"`
			} `json:"replicated"`
		} `json:"voice"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 32<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "Invalid JSON payload received."))
		return
	}
	src, err1 := base64.StdEncoding.DecodeString(body.Voice.Replicated.Source.Data)
	con, err2 := base64.StdEncoding.DecodeString(body.Voice.Replicated.Consent.Data)
	if body.Voice.Type != "replicated" || err1 != nil || err2 != nil || len(src) == 0 || len(con) == 0 {
		writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "A replicated voice needs source_audio and consent_audio."))
		return
	}
	if secs := wavSeconds(src); secs > 0 && (secs < 10 || secs > 30.5) {
		writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "source_audio must be 10 to 30 seconds."))
		return
	}
	sum := sha256.Sum256(append(src, con...))
	v := &Voice{ID: "voice_" + hex.EncodeToString(sum[:8]), Model: body.Voice.Model, Name: body.Voice.DisplayName, Sample: len(src), Consent: len(con), Stored: body.Store != nil && *body.Store}
	s.mu.Lock()
	s.media.voices[v.ID] = v
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"id": v.ID, "type": "replicated", "model": v.Model, "display_name": v.Name, "expire_time": time.Now().AddDate(1, 0, 0).UTC().Format(time.RFC3339)})
}

// voiceList lists the voice copies (type=replicated), one page.
func (s *Server) voiceList(w http.ResponseWriter, r *http.Request) {
	if t := r.URL.Query().Get("type"); t != "" && t != "replicated" {
		writeJSON(w, http.StatusOK, map[string]any{"voices": []any{}})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []any{}
	for _, v := range s.media.voices {
		if !v.Deleted {
			out = append(out, map[string]any{"id": v.ID, "type": "replicated", "model": v.Model, "display_name": v.Name})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"voices": out})
}

func (s *Server) voiceGet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	v := s.media.voices[r.PathValue("id")]
	s.mu.Unlock()
	if v == nil || v.Deleted {
		writeJSON(w, http.StatusNotFound, gerr(404, "NOT_FOUND", "Voice not found."))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": v.ID, "type": "replicated", "model": v.Model, "display_name": v.Name})
}

func (s *Server) voiceDelete(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.media.voices[r.PathValue("id")]
	if v == nil || v.Deleted {
		writeJSON(w, http.StatusNotFound, gerr(404, "NOT_FOUND", "Voice not found."))
		return
	}
	v.Deleted = true
	writeJSON(w, http.StatusOK, map[string]any{})
}

// toneWAV is a quiet tone as a 16 kHz mono WAV (the fake can't use
// internal/aiservices: videogen's own tests use this fake).
func toneWAV(seconds, hz float64) []byte {
	const rate = 16000
	n := int(math.Max(0.1, seconds) * rate)
	var b bytes.Buffer
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + n*2))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1))
	w(uint16(1))
	w(uint32(rate))
	w(uint32(rate * 2))
	w(uint16(2))
	w(uint16(16))
	b.WriteString("data")
	w(uint32(n * 2))
	for i := 0; i < n; i++ {
		w(int16(1500 * math.Sin(2*math.Pi*hz*float64(i)/rate)))
	}
	return b.Bytes()
}

// wavSeconds is how long a 16-bit PCM WAV plays (0 when it isn't one).
func wavSeconds(b []byte) float64 {
	if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return 0
	}
	rate := binary.LittleEndian.Uint32(b[24:28])
	block := binary.LittleEndian.Uint16(b[32:34])
	if rate == 0 || block == 0 {
		return 0
	}
	return float64(len(b)-44) / float64(uint32(block)*rate)
}
