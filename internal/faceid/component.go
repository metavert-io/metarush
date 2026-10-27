package faceid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/components/choice"
)

// The face model is OpenCV Zoo's SFace (face_recognition_sface_2021dec.onnx,
// "All files in this directory are licensed under Apache 2.0 License"),
// downloaded the first time faces are fingerprinted from a pinned revision of
// OpenCV's own Hugging Face mirror and checked against its SHA-256 before it
// is used (docs/installation.md).
const (
	ModelFile   = "face_recognition_sface_2021dec.onnx"
	ModelURL    = "https://huggingface.co/opencv/face_recognition_sface/resolve/3d7082438a6e4551e840c9b2bb60b71e8da4b524/" + ModelFile
	ModelSHA256 = "0ba9fbfa01b5270c96627c4ef784da859931e02f04419c829e83484087c34e79"
	ModelBytes  = 38696353
)

// Setup finds and downloads the face model for one installation home
// (<home>/components/faces).
type Setup struct {
	Home string
	// HTTP, URL and SHA256 override the download (tests).
	HTTP   *http.Client
	URL    string
	SHA256 string
}

// Path is where the model lives in this home.
func (s Setup) Path() string { return filepath.Join(s.Home, "components", "faces", ModelFile) }

func (s Setup) sha() string {
	if s.SHA256 != "" {
		return s.SHA256
	}
	return ModelSHA256
}

func (s Setup) url() string {
	if s.URL != "" {
		return s.URL
	}
	return ModelURL
}

// checked remembers model files whose SHA-256 was verified in this process.
var checked sync.Map

// Model is the model's path when it is here and verified ("" otherwise).
func (s Setup) Model() string {
	p := s.Path()
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return ""
	}
	key := fmt.Sprintf("%s|%d|%s", p, st.Size(), st.ModTime())
	if v, ok := checked.Load(key); ok {
		if v.(bool) {
			return p
		}
		return ""
	}
	got, err := fileSHA(p)
	ok := err == nil && got == s.sha()
	checked.Store(key, ok)
	if !ok {
		return ""
	}
	return p
}

var installMu sync.Mutex

// Ensure downloads the model when it is missing (once per home at a time)
// and returns its path.
func (s Setup) Ensure(ctx context.Context) (string, error) {
	if p := s.Model(); p != "" {
		return p, nil
	}
	if choice.Declined(s.Home, "faces") {
		// A person left it out or removed it (D325): never downloaded by itself.
		return "", choice.LeftOut("faces", "Recognizing faces across clips needs the face model (about 40 MB). Add it in Settings → Components.")
	}
	installMu.Lock()
	defer installMu.Unlock()
	if p := s.Model(); p != "" {
		return p, nil
	}
	dst := s.Path()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url(), nil)
	if err != nil {
		return "", err
	}
	hc := s.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Minute}
	}
	res, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not download the face model: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("could not download the face model: the download answered HTTP %d", res.StatusCode)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(res.Body, 4*ModelBytes)); err != nil {
		tmp.Close()
		return "", fmt.Errorf("could not download the face model: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != s.sha() {
		return "", errors.New("the face model did not match its published checksum, so it was not used")
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	return s.Model(), nil
}

func fileSHA(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
