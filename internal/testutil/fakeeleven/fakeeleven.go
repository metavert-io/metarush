// Package fakeeleven is an in-process fake of the parts of the ElevenLabs API
// MetaRush uses (subscription, voices, the voice library, models, speech with
// timestamps, instant voice cloning, voice previews, voice isolation). Tests and the dashboard
// screenshot flow point METARUSH_ELEVENLABS_BASE_URL at it; it never talks to
// the network and accepts only the key it was created with.
package fakeeleven

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Voice is a fake voice.
type Voice struct {
	ID, Name, Category string
	Labels             map[string]string
	Owner              string // library voices
	Freq               float64
}

// Server is the fake API.
type Server struct {
	*httptest.Server
	Key string

	mu             sync.Mutex
	calls          map[string]int
	CharacterCount int64
	CharacterLimit int64
	Account        []Voice
	Library        []Voice
	Clones         []CloneCall
	TTS            []TTSCall
	Isolations     []IsolationCall
	NoAlignment    bool // simulate a model that returns no alignment
	// SpeechDir holds speech a browser run made beforehand: AI speech of a
	// text is the file <SpeechDir>/<SpeechName(text)>.wav when it is there
	// (spoken in the footage's own synthetic voice), else the usual tone.
	SpeechDir string
	nextID    int
	media     mediaState // AI services' endpoints (media.go)
}

// CloneCall records an instant-voice-clone request.
type CloneCall struct {
	Name, Description string
	Files             int
	Bytes             int
}

// IsolationCall records a voice isolation request.
type IsolationCall struct {
	Bytes   int
	Seconds float64
	Credits int64
	Format  string
}

// TTSCall records a speech request.
type TTSCall struct {
	VoiceID, ModelID, Text, OutputFormat string
	Settings                             map[string]any
}

// New starts a fake with a few voices and the given key on a random loopback port.
func New(key string) *Server {
	s, err := NewAt(key, "")
	if err != nil {
		panic(err)
	}
	return s
}

// NewAt starts the fake on a fixed loopback address (e.g. 127.0.0.1:4889), or
// a random port when addr is empty.
func NewAt(key, addr string) (*Server, error) {
	s := &Server{Key: key, calls: map[string]int{}, CharacterLimit: 100000,
		Account: []Voice{
			{ID: "voice_ada", Name: "Ada", Category: "premade", Labels: map[string]string{"gender": "female", "accent": "british", "age": "middle aged", "use_case": "narration", "descriptive": "warm"}, Freq: 220},
			{ID: "voice_bram", Name: "Bram", Category: "premade", Labels: map[string]string{"gender": "male", "accent": "american", "age": "young", "use_case": "conversational", "descriptive": "calm"}, Freq: 180},
			{ID: "voice_cleo", Name: "Cleo", Category: "generated", Labels: map[string]string{"gender": "female", "accent": "australian", "age": "young", "use_case": "social media"}, Freq: 260},
		},
		Library: []Voice{
			{ID: "lib_dawn", Name: "Dawn Documentary", Category: "professional", Owner: "owner_1", Labels: map[string]string{"gender": "female", "accent": "american", "age": "old", "use_case": "narrative_story"}, Freq: 200},
		}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/user/subscription", s.auth("subscription", s.subscription))
	mux.HandleFunc("GET /v2/voices", s.auth("voices", s.voices))
	mux.HandleFunc("GET /v1/shared-voices", s.auth("shared", s.shared))
	mux.HandleFunc("POST /v1/voices/add/{owner}/{voice}", s.auth("add-shared", s.addShared))
	mux.HandleFunc("POST /v1/voices/add", s.auth("clone", s.clone))
	mux.HandleFunc("GET /v1/models", s.auth("models", s.models))
	mux.HandleFunc("POST /v1/text-to-speech/{voice}/with-timestamps", s.auth("tts", s.tts))
	mux.HandleFunc("GET /previews/{file}", s.preview)
	mux.HandleFunc("POST /v1/audio-isolation", s.auth("isolation", s.isolate))
	s.mediaRoutes(mux)
	// Not part of the real API: the calls answered so far ({"tts": 2, "isolation": 1}),
	// for browser tests that run the fake as its own process.
	mux.HandleFunc("GET /__calls", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		out := map[string]int{}
		for k, v := range s.calls {
			out[k] = v
		}
		out["isolation"] = len(s.Isolations)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, out)
	})
	if addr == "" {
		s.Server = httptest.NewServer(mux)
		return s, nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("the fake ElevenLabs server only listens on loopback, not %s", host)
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s.Server = httptest.NewUnstartedServer(mux)
	_ = s.Server.Listener.Close()
	s.Server.Listener = l
	s.Server.Start()
	return s, nil
}

// Calls reports how many times an endpoint group was called.
func (s *Server) Calls(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[name]
}

func (s *Server) auth(name string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.calls[name]++
		s.mu.Unlock()
		if r.Header.Get("xi-api-key") != s.Key {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": map[string]any{"status": "invalid_api_key", "message": "Invalid API key"}})
			return
		}
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) subscription(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"tier": "creator", "character_count": s.CharacterCount, "character_limit": s.CharacterLimit,
		"next_character_count_reset_unix": time.Now().Add(20 * 24 * time.Hour).Unix(), "can_use_instant_voice_cloning": true, "status": "active"})
}

func (s *Server) apiVoice(v Voice) map[string]any {
	return map[string]any{"voice_id": v.ID, "name": v.Name, "category": v.Category, "labels": v.Labels, "description": "A " + v.Labels["descriptive"] + " voice.",
		"preview_url": s.URL + "/previews/" + v.ID + ".wav"}
}

func (s *Server) voices(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(r.URL.Query().Get("search"))
	ids := r.URL.Query()["voice_ids"]
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []any{}
	for _, v := range s.Account {
		if len(ids) > 0 {
			match := false
			for _, id := range ids {
				match = match || id == v.ID
			}
			if !match {
				continue
			}
		}
		if q != "" && !strings.Contains(strings.ToLower(v.Name), q) && !strings.Contains(strings.ToLower(v.Labels["accent"]+" "+v.Labels["descriptive"]), q) {
			continue
		}
		out = append(out, s.apiVoice(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"voices": out, "has_more": false, "total_count": len(out), "next_page_token": nil})
}

func (s *Server) shared(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(r.URL.Query().Get("search"))
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []any{}
	for _, v := range s.Library {
		if q != "" && !strings.Contains(strings.ToLower(v.Name), q) {
			continue
		}
		out = append(out, map[string]any{"public_owner_id": v.Owner, "voice_id": v.ID, "name": v.Name, "accent": v.Labels["accent"], "gender": v.Labels["gender"],
			"age": v.Labels["age"], "use_case": v.Labels["use_case"], "category": v.Category, "preview_url": s.URL + "/previews/" + v.ID + ".wav"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"voices": out, "has_more": false})
}

func (s *Server) addShared(w http.ResponseWriter, r *http.Request) {
	owner, id := r.PathValue("owner"), r.PathValue("voice")
	var body struct {
		NewName string `json:"new_name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.Library {
		if v.ID == id && v.Owner == owner {
			s.nextID++
			nv := v
			nv.ID, nv.Name, nv.Category, nv.Owner = fmt.Sprintf("voice_lib%d", s.nextID), body.NewName, "professional", ""
			s.Account = append(s.Account, nv)
			writeJSON(w, http.StatusOK, map[string]any{"voice_id": nv.ID})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"detail": map[string]any{"status": "voice_not_found", "message": "voice not found"}})
}

func (s *Server) clone(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "bad form"})
		return
	}
	files := r.MultipartForm.File["files"]
	total := 0
	for _, f := range files {
		total += int(f.Size)
	}
	if r.FormValue("name") == "" || len(files) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": []any{map[string]any{"loc": []string{"body", "files"}, "msg": "field required", "type": "missing"}}})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := fmt.Sprintf("voice_clone%d", s.nextID)
	s.Clones = append(s.Clones, CloneCall{Name: r.FormValue("name"), Description: r.FormValue("description"), Files: len(files), Bytes: total})
	s.Account = append(s.Account, Voice{ID: id, Name: r.FormValue("name"), Category: "cloned", Labels: map[string]string{}, Freq: 240})
	writeJSON(w, http.StatusOK, map[string]any{"voice_id": id, "requires_verification": false})
}

func (s *Server) models(w http.ResponseWriter, _ *http.Request) {
	langs := func(n int) []any {
		out := []any{}
		codes := []string{"en", "de", "fr", "es", "it", "pt", "pl", "nl", "sv", "ja", "zh", "ko", "hi", "ar", "tr", "cs", "da", "fi", "el", "id", "ms", "ro", "sk", "uk", "bg", "hr", "ta", "fil", "ru", "hu", "no", "vi"}
		for i := 0; i < n && i < len(codes); i++ {
			out = append(out, map[string]any{"language_id": codes[i], "name": strings.ToUpper(codes[i])})
		}
		return out
	}
	writeJSON(w, http.StatusOK, []any{
		map[string]any{"model_id": "eleven_flash_v2_5", "name": "Eleven Flash v2.5", "can_do_text_to_speech": true, "can_use_style": false, "can_use_speaker_boost": true,
			"maximum_text_length_per_request": 40000, "model_rates": map[string]any{"character_cost_multiplier": 0.5}, "languages": langs(32)},
		map[string]any{"model_id": "eleven_multilingual_v2", "name": "Eleven Multilingual v2", "can_do_text_to_speech": true, "can_use_style": true, "can_use_speaker_boost": true,
			"maximum_text_length_per_request": 10000, "model_rates": map[string]any{"character_cost_multiplier": 1.0}, "languages": langs(29)},
		map[string]any{"model_id": "eleven_english_sts_v2", "name": "Eleven English v2 (speech to speech)", "can_do_text_to_speech": false, "can_do_voice_conversion": true,
			"model_rates": map[string]any{"character_cost_multiplier": 1.0}, "languages": langs(1)},
	})
}

func (s *Server) tts(w http.ResponseWriter, r *http.Request) {
	voiceID := r.PathValue("voice")
	var body struct {
		Text          string         `json:"text"`
		ModelID       string         `json:"model_id"`
		VoiceSettings map[string]any `json:"voice_settings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": []any{map[string]any{"loc": []string{"body", "text"}, "msg": "field required", "type": "missing"}}})
		return
	}
	s.mu.Lock()
	var voice *Voice
	for i := range s.Account {
		if s.Account[i].ID == voiceID {
			voice = &s.Account[i]
		}
	}
	chars := int64(len([]rune(body.Text)))
	if voice == nil {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": map[string]any{"status": "voice_not_found", "message": "A voice with that ID does not exist."}})
		return
	}
	if s.CharacterCount+chars > s.CharacterLimit {
		s.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": map[string]any{"status": "quota_exceeded", "message": "This request exceeds your quota."}})
		return
	}
	s.CharacterCount += chars
	s.TTS = append(s.TTS, TTSCall{VoiceID: voiceID, ModelID: body.ModelID, Text: body.Text, OutputFormat: r.URL.Query().Get("output_format"), Settings: body.VoiceSettings})
	freq := voice.Freq
	noAlign := s.NoAlignment
	s.mu.Unlock()
	speed := 1.0
	if v, ok := body.VoiceSettings["speed"].(float64); ok && v > 0 {
		speed = v
	}
	perChar := 0.062 / speed
	runes := []rune(body.Text)
	lead := 0.08
	dur := lead + float64(len(runes))*perChar + 0.12
	audio := SpeechWAV(dur, freq, 22050, func(t float64) bool { // silent between "words"
		i := int((t - lead) / perChar)
		return t >= lead && i >= 0 && i < len(runes) && runes[i] != ' '
	})
	chs, st, en := []string{}, []float64{}, []float64{}
	for i, ch := range runes {
		chs = append(chs, string(ch))
		st = append(st, round3(lead+float64(i)*perChar))
		en = append(en, round3(lead+float64(i+1)*perChar))
	}
	res := map[string]any{"audio_base64": base64.StdEncoding.EncodeToString(audio)}
	if !noAlign {
		al := map[string]any{"characters": chs, "character_start_times_seconds": st, "character_end_times_seconds": en}
		res["alignment"], res["normalized_alignment"] = al, al
	}
	writeJSON(w, http.StatusOK, res)
}

// isolate answers a voice isolation request: it charges 1,000 characters a
// minute (rounded up), refuses past the plan's limit like speech does, and sends
// back the WAV with its quiet parts turned down (a simple gate: the background
// drops, the voice stays), so callers can see an effect.
func (s *Server) isolate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(256 << 20); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "bad form"})
		return
	}
	files := r.MultipartForm.File["audio"]
	if len(files) != 1 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": []any{map[string]any{"loc": []string{"body", "audio"}, "msg": "field required", "type": "missing"}}})
		return
	}
	f, err := files[0].Open()
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "unreadable file"})
		return
	}
	data := make([]byte, files[0].Size)
	_, _ = io.ReadFull(f, data)
	f.Close()
	rate, channels, bits, pcm, ok := parseWAV(data)
	if !ok || bits != 16 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": map[string]any{"status": "invalid_audio", "message": "the fake reads 16-bit WAV only"}})
		return
	}
	frames := len(pcm) / 2 / channels
	secs := float64(frames) / float64(rate)
	credits := int64(math.Ceil(secs / 60 * 1000))
	s.mu.Lock()
	if s.CharacterCount+credits > s.CharacterLimit {
		s.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": map[string]any{"status": "quota_exceeded", "message": "This request exceeds your quota."}})
		return
	}
	s.CharacterCount += credits
	s.Isolations = append(s.Isolations, IsolationCall{Bytes: len(data), Seconds: secs, Credits: credits, Format: r.FormValue("file_format")})
	s.mu.Unlock()
	out := gate(pcm, rate, channels)
	w.Header().Set("Content-Type", "audio/mpeg") // the real API answers MP3; callers decode by content
	_, _ = w.Write(wavBytes(out, rate, channels))
}

// parseWAV finds the format and the PCM data of a WAV file.
func parseWAV(b []byte) (rate, channels, bits int, pcm []byte, ok bool) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return
	}
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4:]))
		body := b[off+8 : min(len(b), off+8+size)]
		switch id {
		case "fmt ":
			if len(body) >= 16 {
				channels = int(binary.LittleEndian.Uint16(body[2:]))
				rate = int(binary.LittleEndian.Uint32(body[4:]))
				bits = int(binary.LittleEndian.Uint16(body[14:]))
			}
		case "data":
			return rate, channels, bits, body, rate > 0 && channels > 0
		}
		off += 8 + size + size%2
	}
	return
}

// gate turns 10 ms frames well below the loud parts down by 20 dB.
func gate(pcm []byte, rate, channels int) []byte {
	out := append([]byte(nil), pcm...)
	n := len(pcm) / 2
	hop := max(1, rate/100) * channels
	var energies []float64
	for i := 0; i+hop <= n; i += hop {
		var e float64
		for k := i; k < i+hop; k++ {
			v := float64(int16(binary.LittleEndian.Uint16(pcm[2*k:])))
			e += v * v
		}
		energies = append(energies, e/float64(hop))
	}
	if len(energies) == 0 {
		return out
	}
	sorted := append([]float64(nil), energies...)
	sort.Float64s(sorted)
	thr := sorted[len(sorted)*3/10] * 4
	for f, e := range energies {
		if e > thr {
			continue
		}
		for k := f * hop; k < f*hop+hop; k++ {
			v := int16(binary.LittleEndian.Uint16(pcm[2*k:]))
			binary.LittleEndian.PutUint16(out[2*k:], uint16(v/10))
		}
	}
	return out
}

func wavBytes(pcm []byte, rate, channels int) []byte {
	data := make([]byte, 44+len(pcm))
	copy(data[0:], "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(36+len(pcm)))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], uint16(channels))
	binary.LittleEndian.PutUint32(data[24:], uint32(rate))
	binary.LittleEndian.PutUint32(data[28:], uint32(rate*channels*2))
	binary.LittleEndian.PutUint16(data[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(len(pcm)))
	copy(data[44:], pcm)
	return data
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.PathValue("file"), ".wav")
	s.mu.Lock()
	s.calls["preview"]++
	freq := 0.0
	for _, v := range append(append([]Voice{}, s.Account...), s.Library...) {
		if v.ID == id {
			freq = v.Freq
		}
	}
	s.mu.Unlock()
	if freq == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	_, _ = w.Write(SpeechWAV(2.5, freq, 22050, func(t float64) bool { return math.Mod(t, 0.5) < 0.38 }))
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// SpeechWAV synthesizes a mono 16-bit WAV: a tone with harmonics where on(t)
// is true, silence elsewhere — speech-like enough for loudness and silence checks.
func SpeechWAV(seconds, freq float64, rate int, on func(t float64) bool) []byte {
	n := int(seconds * float64(rate))
	data := make([]byte, 44+2*n)
	copy(data[0:], "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(36+2*n))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], uint32(rate))
	binary.LittleEndian.PutUint32(data[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(2*n))
	env := 0.0
	for i := 0; i < n; i++ {
		t := float64(i) / float64(rate)
		target := 0.0
		if on(t) {
			target = 1
		}
		env += (target - env) * 0.004 // ~5 ms attack/release: no clicks
		v := env * 0.35 * (math.Sin(2*math.Pi*freq*t) + 0.4*math.Sin(4*math.Pi*freq*t) + 0.2*math.Sin(6*math.Pi*freq*t)) / 1.6
		binary.LittleEndian.PutUint16(data[44+2*i:], uint16(int16(v*32767)))
	}
	return data
}

// Itoa is strconv.Itoa (kept for callers building fake IDs).
func Itoa(i int) string { return strconv.Itoa(i) }
