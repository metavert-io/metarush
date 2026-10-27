package fakeeleven

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The rest of the ElevenLabs API MetaRush's AI services use (D224): sound
// effects, music, speech without timestamps, the voice changer and its
// history, speech to text, dubbing (a project that finishes after a few
// checks) and deleting voices. Results are small WAV files. A marker in the
// words or the file name makes it misbehave: [fake:quota] (402, no credits),
// [fake:tos] (400, against the terms), [fake:rate] (429), [fake:server] (500).

// HistoryItem is something ElevenLabs keeps in the history.
type HistoryItem struct {
	ID, RequestID, VoiceID, Source string
	Deleted                        bool
}

// Dub is a dubbing project.
type Dub struct {
	ID, Target, Source string
	OwnVoices          bool
	Bytes              int
	Polls              int
	Deleted            bool
}

// Transcript is a speech-to-text result ElevenLabs keeps.
type Transcript struct {
	ID      string
	Deleted bool
}

type mediaState struct {
	once        sync.Once
	history     []*HistoryItem
	dubs        map[string]*Dub
	transcripts map[string]*Transcript
	songs       int
	effects     int
	requests    []string // what each media request asked, for test messages
	// DubPolls is how many checks a dub needs before it is finished.
	dubPolls int
}

func (s *Server) ms() *mediaState {
	s.media.once.Do(func() {
		s.media.dubs, s.media.transcripts, s.media.dubPolls = map[string]*Dub{}, map[string]*Transcript{}, 2
	})
	return &s.media
}

// SetDubPolls sets how many checks a dub needs before it is finished.
func (s *Server) SetDubPolls(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ms().dubPolls = n
}

func (s *Server) mediaRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/sound-generation", s.auth("sound-effect", s.soundEffect))
	mux.HandleFunc("POST /v1/music", s.auth("music", s.music))
	mux.HandleFunc("POST /v1/text-to-speech/{voice}", s.auth("speech", s.speech))
	mux.HandleFunc("POST /v1/voice-changer/{voice}", s.auth("voice-change", s.voiceChange))
	mux.HandleFunc("GET /v1/history", s.auth("history", s.historyList))
	mux.HandleFunc("DELETE /v1/history/{id}", s.auth("history-delete", s.historyDelete))
	mux.HandleFunc("POST /v1/speech-to-text", s.auth("stt", s.speechToText))
	mux.HandleFunc("DELETE /v1/speech-to-text/transcripts/{id}", s.auth("stt-delete", s.transcriptDelete))
	mux.HandleFunc("POST /v1/dubbing", s.auth("dub", s.dubCreate))
	mux.HandleFunc("GET /v1/dubbing/{id}", s.auth("dub-get", s.dubGet))
	mux.HandleFunc("GET /v1/dubbing/{id}/audio/{lang}", s.auth("dub-audio", s.dubAudio))
	mux.HandleFunc("DELETE /v1/dubbing/{id}", s.auth("dub-delete", s.dubDelete))
	mux.HandleFunc("DELETE /v1/voices/{id}", s.auth("voice-delete", s.voiceDelete))
}

// Kept counts what the fake still keeps that MetaRush could delete: history
// items, transcripts and dubs.
func (s *Server) Kept() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.ms()
	n := 0
	for _, h := range m.history {
		if !h.Deleted {
			n++
		}
	}
	for _, t := range m.transcripts {
		if !t.Deleted {
			n++
		}
	}
	for _, d := range m.dubs {
		if !d.Deleted {
			n++
		}
	}
	return n
}

// Dubs lists the dubbing projects.
func (s *Server) Dubs() []Dub {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Dub
	for _, d := range s.ms().dubs {
		out = append(out, *d)
	}
	return out
}

// MediaRequests lists what each media request asked, oldest first.
func (s *Server) MediaRequests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.ms().requests...)
}

var fakeMarker = regexp.MustCompile(`\[fake:([a-z-]+)\]`)

// misbehave answers a marker's error; false when there is none.
func misbehave(w http.ResponseWriter, text string) bool {
	m := fakeMarker.FindStringSubmatch(text)
	if m == nil {
		return false
	}
	switch m[1] {
	case "quota":
		writeJSON(w, http.StatusPaymentRequired, map[string]any{"detail": map[string]any{"type": "payment_required", "code": "insufficient_credits", "message": "You have insufficient credits."}})
	case "tos":
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": map[string]any{"type": "invalid_request", "code": "invalid_request", "message": "This request violates our terms of service."}})
	case "rate":
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"detail": map[string]any{"type": "rate_limit_error", "code": "rate_limit_exceeded", "message": "Too many requests."}})
	case "server":
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": map[string]any{"type": "internal_error", "code": "internal_error", "message": "Internal error."}})
	default:
		return false
	}
	return true
}

func (s *Server) newID(prefix string) string {
	s.nextID++
	return fmt.Sprintf("%s%d", prefix, s.nextID)
}

func writeAudio(w http.ResponseWriter, b []byte, requestID string) {
	if requestID != "" {
		w.Header().Set("request-id", requestID)
	}
	w.Header().Set("Content-Type", "audio/wav") // the fake makes WAV; ElevenLabs sends MP3 when asked
	_, _ = w.Write(b)
}

func tone(seconds float64, freq float64) []byte {
	return SpeechWAV(math.Max(0.3, seconds), freq, 16000, func(t float64) bool { return true })
}

func (s *Server) soundEffect(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Text            string   `json:"text"`
		ModelID         string   `json:"model_id"`
		DurationSeconds *float64 `json:"duration_seconds"`
		Loop            bool     `json:"loop"`
	}
	if json.NewDecoder(r.Body).Decode(&b) != nil || b.Text == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": []any{map[string]any{"loc": []string{"body", "text"}, "msg": "field required", "type": "missing"}}})
		return
	}
	if misbehave(w, b.Text) {
		return
	}
	secs := 2.0
	if b.DurationSeconds != nil {
		if *b.DurationSeconds < 0.5 || *b.DurationSeconds > 30 {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "duration_seconds must be between 0.5 and 30"})
			return
		}
		secs = *b.DurationSeconds
	}
	s.mu.Lock()
	m := s.ms()
	m.effects++
	m.requests = append(m.requests, fmt.Sprintf("sound-effect %s %.1fs %q", b.ModelID, secs, b.Text))
	s.mu.Unlock()
	writeAudio(w, tone(secs, 300+float64(len(b.Text))), "")
}

func (s *Server) music(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Prompt            string `json:"prompt"`
		ModelID           string `json:"model_id"`
		MusicLengthMS     *int   `json:"music_length_ms"`
		ForceInstrumental bool   `json:"force_instrumental"`
	}
	if json.NewDecoder(r.Body).Decode(&b) != nil || b.Prompt == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "prompt or composition_plan is required"})
		return
	}
	if misbehave(w, b.Prompt) {
		return
	}
	secs := 30.0
	if b.MusicLengthMS != nil {
		if *b.MusicLengthMS < 3000 || *b.MusicLengthMS > 600000 {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "music_length_ms must be between 3000 and 600000"})
			return
		}
		secs = float64(*b.MusicLengthMS) / 1000
	}
	s.mu.Lock()
	m := s.ms()
	m.songs++
	id := s.newID("song_")
	m.requests = append(m.requests, fmt.Sprintf("music %s %.1fs instrumental=%v %q", b.ModelID, secs, b.ForceInstrumental, b.Prompt))
	s.mu.Unlock()
	w.Header().Set("song-id", id)
	writeAudio(w, tone(math.Min(secs, 20), 260+float64(len(b.Prompt))), "")
}

func (s *Server) remember(source, voice string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	rid := s.newID("req_")
	s.ms().history = append(s.ms().history, &HistoryItem{ID: s.newID("hist_"), RequestID: rid, VoiceID: voice, Source: source})
	return rid
}

func (s *Server) knownVoice(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.Account {
		if v.ID == id {
			return true
		}
	}
	return false
}

func (s *Server) speech(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Text    string `json:"text"`
		ModelID string `json:"model_id"`
	}
	if json.NewDecoder(r.Body).Decode(&b) != nil || b.Text == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "text is required"})
		return
	}
	if misbehave(w, b.Text) {
		return
	}
	voice := r.PathValue("voice")
	if !s.knownVoice(voice) {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": map[string]any{"status": "voice_not_found", "message": "A voice with the voice_id " + voice + " was not found."}})
		return
	}
	s.mu.Lock()
	s.CharacterCount += int64(len([]rune(b.Text)))
	s.ms().requests = append(s.ms().requests, fmt.Sprintf("speech %s %s %q", voice, b.ModelID, b.Text))
	dir := s.SpeechDir
	s.mu.Unlock()
	if dir != "" {
		if made, err := os.ReadFile(filepath.Join(dir, SpeechName(b.Text)+".wav")); err == nil {
			writeAudio(w, made, s.remember("TTS", voice))
			return
		}
	}
	writeAudio(w, tone(float64(len([]rune(b.Text)))/14, 220), s.remember("TTS", voice))
}

func (s *Server) voiceChange(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "bad form"})
		return
	}
	files := r.MultipartForm.File["audio"]
	if len(files) != 1 || r.FormValue("model_id") == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": []any{map[string]any{"loc": []string{"body", "audio"}, "msg": "field required", "type": "missing"}}})
		return
	}
	if misbehave(w, files[0].Filename) {
		return
	}
	voice := r.PathValue("voice")
	if !s.knownVoice(voice) {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": map[string]any{"status": "voice_not_found", "message": "voice not found"}})
		return
	}
	f, _ := files[0].Open()
	data, _ := io.ReadAll(f)
	_ = f.Close()
	secs := wavLength(data)
	s.mu.Lock()
	s.ms().requests = append(s.ms().requests, fmt.Sprintf("voice-change %s %s %d bytes", voice, r.FormValue("model_id"), len(data)))
	s.mu.Unlock()
	writeAudio(w, tone(secs, 180), s.remember("STS", voice))
}

// wavLength is how long a 16-bit WAV plays (2 seconds when it can't tell).
func wavLength(b []byte) float64 {
	rate, ch, bits, pcm, ok := parseWAV(b)
	if !ok || rate == 0 || ch == 0 || bits == 0 {
		return 2
	}
	return float64(len(pcm)) / float64(rate*ch*bits/8)
}

func (s *Server) historyList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	defer s.mu.Unlock()
	items := []any{}
	for i := len(s.ms().history) - 1; i >= 0; i-- {
		h := s.ms().history[i]
		if h.Deleted || (q.Get("voice_id") != "" && q.Get("voice_id") != h.VoiceID) || (q.Get("source") != "" && q.Get("source") != h.Source) {
			continue
		}
		items = append(items, map[string]any{"history_item_id": h.ID, "request_id": h.RequestID, "voice_id": h.VoiceID, "source": h.Source, "date_unix": time.Now().Unix()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": items, "has_more": false})
}

func (s *Server) historyDelete(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range s.ms().history {
		if h.ID == r.PathValue("id") && !h.Deleted {
			h.Deleted = true
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"detail": map[string]any{"code": "history_item_not_found", "message": "History item not found."}})
}

func (s *Server) speechToText(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(256 << 20); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "bad form"})
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) != 1 || r.FormValue("model_id") == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": []any{map[string]any{"loc": []string{"body", "model_id"}, "msg": "field required", "type": "missing"}}})
		return
	}
	if misbehave(w, files[0].Filename) {
		return
	}
	if strings.Contains(files[0].Filename, "[fake:silent]") { // a 200 that heard nothing
		s.mu.Lock()
		id := s.newID("tr_")
		s.ms().transcripts[id] = &Transcript{ID: id}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"language_code": "en", "text": "", "words": []any{}, "transcription_id": id})
		return
	}
	words := strings.Fields("thanks for watching the fake transcript")
	var out []any
	for i, word := range words {
		if i > 0 {
			out = append(out, map[string]any{"text": " ", "type": "spacing", "start": float64(i)*0.5 - 0.1, "end": float64(i) * 0.5})
		}
		sp := "speaker_0"
		if i >= 3 && r.FormValue("diarize") == "true" {
			sp = "speaker_1"
		}
		out = append(out, map[string]any{"text": word, "type": "word", "start": float64(i) * 0.5, "end": float64(i)*0.5 + 0.4, "speaker_id": sp, "logprob": -0.1})
	}
	out = append(out, map[string]any{"text": "(laughs)", "type": "audio_event", "start": 3.1, "end": 3.4})
	s.mu.Lock()
	id := s.newID("tr_")
	s.ms().transcripts[id] = &Transcript{ID: id}
	s.ms().requests = append(s.ms().requests, fmt.Sprintf("stt %s diarize=%s granularity=%s lang=%s", r.FormValue("model_id"), r.FormValue("diarize"), r.FormValue("timestamps_granularity"), r.FormValue("language_code")))
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"language_code": firstOr(r.FormValue("language_code"), "en"), "language_probability": 0.98, "text": strings.Join(words, " "),
		"words": out, "transcription_id": id})
}

func firstOr(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func (s *Server) transcriptDelete(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.ms().transcripts[r.PathValue("id")]
	if t == nil || t.Deleted {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": map[string]any{"code": "transcript_not_found", "message": "Transcript not found."}})
		return
	}
	t.Deleted = true
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) dubCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(256 << 20); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "bad form"})
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) != 1 || r.FormValue("target_lang") == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": []any{map[string]any{"loc": []string{"body", "target_lang"}, "msg": "field required", "type": "missing"}}})
		return
	}
	if misbehave(w, files[0].Filename) {
		return
	}
	f, _ := files[0].Open()
	data, _ := io.ReadAll(f)
	_ = f.Close()
	s.mu.Lock()
	id := s.newID("dub_")
	s.ms().dubs[id] = &Dub{ID: id, Target: r.FormValue("target_lang"), Source: r.FormValue("source_lang"), OwnVoices: r.FormValue("disable_voice_cloning") != "true", Bytes: len(data)}
	s.ms().requests = append(s.ms().requests, fmt.Sprintf("dub %s→%s cloning=%v watermark=%s", r.FormValue("source_lang"), r.FormValue("target_lang"), r.FormValue("disable_voice_cloning") != "true", r.FormValue("watermark")))
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"dubbing_id": id, "expected_duration_sec": 4.5})
}

func (s *Server) dubGet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.ms().dubs[r.PathValue("id")]
	if d == nil || d.Deleted {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": map[string]any{"code": "dubbing_not_found", "message": "Dubbing not found."}})
		return
	}
	d.Polls++
	status := "dubbing"
	if d.Polls >= s.ms().dubPolls {
		status = "dubbed"
	}
	writeJSON(w, http.StatusOK, map[string]any{"dubbing_id": d.ID, "name": "MetaRush dub", "status": status, "target_languages": []string{d.Target}, "created_at": time.Now().UTC().Format(time.RFC3339)})
}

func (s *Server) dubAudio(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	d := s.ms().dubs[r.PathValue("id")]
	ok := d != nil && !d.Deleted && d.Polls >= s.ms().dubPolls && r.PathValue("lang") == d.Target
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": map[string]any{"code": "dubbing_not_found", "message": "Dub not found or not ready for " + r.PathValue("lang") + "."}})
		return
	}
	writeAudio(w, tone(3, 330), "")
}

func (s *Server) dubDelete(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.ms().dubs[r.PathValue("id")]
	if d == nil || d.Deleted {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": map[string]any{"code": "dubbing_not_found", "message": "Dubbing not found."}})
		return
	}
	d.Deleted = true
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) voiceDelete(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	for i, v := range s.Account {
		if v.ID == id {
			s.Account = append(s.Account[:i], s.Account[i+1:]...)
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"detail": map[string]any{"code": "voice_not_found", "message": "Voice not found."}})
}

// SpeechName is the file name (without .wav) the fake looks for in SpeechDir
// for a text: the first 16 hex digits of its SHA-256.
func SpeechName(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:16]
}
