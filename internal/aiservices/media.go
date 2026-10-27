package aiservices

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// Adapters hand over files people can play or open: a provider that answers
// with raw samples (Google's speech is 16-bit PCM) is wrapped in a WAV
// header here, so every kind's outputs are ordinary files (the conformance
// suite checks it with Sniff).

// PCMToWAV wraps 16-bit little-endian PCM samples in a WAV header.
func PCMToWAV(pcm []byte, rate, channels int) []byte {
	if rate <= 0 {
		rate = 24000
	}
	if channels <= 0 {
		channels = 1
	}
	var b bytes.Buffer
	b.Grow(44 + len(pcm))
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + len(pcm)))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1)) // PCM
	w(uint16(channels))
	w(uint32(rate))
	w(uint32(rate * channels * 2))
	w(uint16(channels * 2))
	w(uint16(16))
	b.WriteString("data")
	w(uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}

// PCMRate reads the sample rate from a raw-audio MIME type such as
// "audio/L16;codec=pcm;rate=24000" (0 when it says none).
func PCMRate(mime string) int {
	for _, p := range strings.Split(mime, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if ok && strings.EqualFold(k, "rate") {
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		}
	}
	return 0
}

// IsRawPCM reports whether a MIME type names raw samples (audio/L16, audio/pcm).
func IsRawPCM(mime string) bool {
	m := strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0]))
	return m == "audio/l16" || m == "audio/pcm" || m == "audio/raw"
}

// ToneWAV is a quiet tone of so many seconds as a 16 kHz mono WAV (fakes
// and tests: a real, playable file).
func ToneWAV(seconds float64, hz float64) []byte {
	const rate = 16000
	n := int(math.Max(0.1, seconds) * rate)
	pcm := make([]byte, n*2)
	for i := 0; i < n; i++ {
		v := int16(1500 * math.Sin(2*math.Pi*hz*float64(i)/rate))
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(v))
	}
	return PCMToWAV(pcm, rate, 1)
}

// WAVSeconds is how long a 16-bit PCM WAV plays (0 when it isn't one).
func WAVSeconds(b []byte) float64 {
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

// Sniff says what a file is by its first bytes: image/png, image/jpeg,
// image/webp, audio/wav, audio/mpeg, audio/ogg, audio/flac, video/mp4 (an
// MPEG-4 container, sound or video), video/webm, application/json, or "" when
// it is none of those.
func Sniff(b []byte) string {
	switch {
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WAVE":
		return "audio/wav"
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	case len(b) >= 3 && string(b[0:3]) == "ID3", len(b) >= 2 && b[0] == 0xFF && b[1]&0xE0 == 0xE0:
		return "audio/mpeg"
	case len(b) >= 4 && string(b[0:4]) == "OggS":
		return "audio/ogg"
	case len(b) >= 4 && string(b[0:4]) == "fLaC":
		return "audio/flac"
	case len(b) >= 8 && string(b[4:8]) == "ftyp":
		return "video/mp4"
	case len(b) >= 4 && b[0] == 0x1A && b[1] == 0x45 && b[2] == 0xDF && b[3] == 0xA3:
		return "video/webm"
	}
	switch ct := http.DetectContentType(b); {
	case strings.HasPrefix(ct, "image/png"), strings.HasPrefix(ct, "image/jpeg"):
		return strings.SplitN(ct, ";", 2)[0]
	}
	t := bytes.TrimSpace(b)
	if len(t) > 0 && (t[0] == '{' || t[0] == '[') {
		return "application/json"
	}
	return ""
}

// ExtFor is a file extension for a MIME type MetaRush keeps (".bin" otherwise).
func ExtFor(mime string) string {
	switch strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0])) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return ".wav"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/ogg", "audio/opus":
		return ".ogg"
	case "audio/flac":
		return ".flac"
	case "audio/mp4", "audio/aac", "audio/m4a":
		return ".m4a"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "application/json":
		return ".json"
	}
	return ".bin"
}

// SHA256 is the hex SHA-256 of some bytes.
func SHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
