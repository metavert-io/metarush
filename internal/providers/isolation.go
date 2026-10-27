package providers

import (
	"bytes"
	"context"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/logbook"
)

// Voice isolation ("cloud" clearer voices, D90): POST /v1/audio-isolation with
// the audio as multipart field "audio"; ElevenLabs answers with the isolated
// voice as audio/mpeg. It costs 1,000 credits a minute of audio and takes files
// up to 500 MB and an hour long (elevenlabs.io/docs, checked 2026-10-19).

// IsolationMaxBytes is the largest file ElevenLabs takes.
const IsolationMaxBytes = 500 << 20

// Isolate sends a recording (a WAV file) for voice isolation and returns the
// audio ElevenLabs sends back. credits is what it is expected to bill (for the
// activity log; never the audio).
func (c *ElevenClient) Isolate(ctx context.Context, path string, credits int) (out []byte, err error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > IsolationMaxBytes {
		return nil, apperr.New(apperr.InvalidInput, "This stretch of sound is too big to send to ElevenLabs (over 500 MB). Shorter stretches are sent one at a time.")
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("audio", filepath.Base(path))
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(part, f)
	f.Close()
	if err != nil {
		return nil, err
	}
	_ = w.WriteField("file_format", "other")
	if err := w.Close(); err != nil {
		return nil, err
	}
	const path0 = "/v1/audio-isolation"
	call, start := elevenCall(withCharacters(ctx, credits), http.MethodPost, path0), time.Now()
	defer func() { call.Duration = time.Since(start); logbook.ProviderDone(ctx, call, err) }()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+path0, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", c.key)
	req.Header.Set("Accept", "audio/mpeg")
	req.Header.Set("Content-Type", w.FormDataContentType())
	hc := *c.HTTP
	hc.Timeout = 30 * time.Minute // long recordings take a while
	res, err := hc.Do(req)
	if err != nil {
		return nil, apperr.Wrap(apperr.Provider, err, "MetaRush couldn't reach ElevenLabs. Check the internet connection and try again.")
	}
	defer res.Body.Close()
	call.Status, call.RequestID = res.StatusCode, res.Header.Get("request-id")
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<30))
	call.Bytes = len(data)
	if err != nil {
		return nil, apperr.Wrap(apperr.Provider, err, "The connection to ElevenLabs dropped. Try again.")
	}
	if res.StatusCode >= 300 {
		return nil, elevenError(res.StatusCode, data)
	}
	if len(data) == 0 {
		return nil, apperr.New(apperr.Provider, "ElevenLabs sent no audio. Try again in a moment.")
	}
	return data, nil
}

// IsolationCredits is what isolating seconds of audio costs.
func IsolationCredits(seconds float64) int {
	return int(math.Ceil(math.Max(0, seconds) / 60 * 1000))
}
