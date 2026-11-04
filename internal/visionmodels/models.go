// Package visionmodels looks at frames with open models on this computer
// (D405): YuNet finds faces and their five points, PaddleOCR's PP-OCR models
// read the words on screen, U²-Net-p says what stands out where no face is
// seen, and BiRefNet cuts a picture's subject out. They run in-process through
// onnxruntime (internal/onnx), the library that already fingerprints faces and
// scores voices, and answer in the on-screen helper's own terms (vision.Frame),
// so the index, face recognition, framing that follows movement and the
// quality checks read what they saw exactly as they read Apple Vision's.
//
// The model files are pinned downloads (D406): the publisher's address and its
// SHA-256, set up only when a person adds the component (components), into the
// models folder every home on the computer shares; each file is checked
// against its SHA-256 before it is used.
package visionmodels

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// File is one model file MetaRush may download for this package.
type File struct {
	// ID names its pinned download (components/pins.go).
	ID string
	// Title is what people call it; What says what it does.
	Title, What string
	// Name is the file's name in the models folder.
	Name string
	// URL and SHA256 are the publisher's file and its checksum (Hugging
	// Face's stated LFS digest, else the one measured when it was pinned);
	// Bytes is its size.
	URL, SHA256 string
	Bytes       int64
	// Licence names the licence; LicenceURL is where its repository says so.
	Licence, LicenceURL string
	// Hosts are the hosts the address may redirect to.
	Hosts []string
	// Part is what it is for: the on-screen index, or cutting subjects out.
	Part string
}

// Parts.
const (
	PartIndex  = "index"
	PartCutOut = "cut-out"
	// PartMatte tells people from their background in footage (D506).
	PartMatte = "matte"
)

// Where publishers send their downloads.
var (
	githubMedia  = []string{"media.githubusercontent.com"}
	githubAssets = []string{"release-assets.githubusercontent.com", "objects.githubusercontent.com"}
	hfCDN        = []string{"us.aws.cdn.hf.co", "eu.aws.cdn.hf.co", "cdn-lfs.huggingface.co", "cdn-lfs-us-1.hf.co", "cdn-lfs-eu-1.hf.co", "cas-bridge.xethub.hf.co"}
)

// Files are the pinned model files (checked 3 Nov 2026, each from its
// publisher's own repository; re-read the repository and its licence before
// changing one):
//   - YuNet, the 2026may export (OpenCV Zoo's models/face_detection_yunet: "All
//     files in this directory are licensed under MIT License"; the 2023mar
//     weights with the picture's size left open), at commit 26cc381e.
//   - PP-OCRv5 mobile text detection and PP-OCRv6 small text recognition,
//     PaddlePaddle's own ONNX exports on Hugging Face (model cards: license
//     apache-2.0; PaddleOCR's LICENSE is the Apache License 2.0), at pinned
//     revisions, with the recognizer's settings file (its characters).
//   - U²-Net-p (the U-2-Net repository's LICENSE is the Apache License 2.0;
//     the ONNX export is rembg's, MIT, from its release v0.0.0, whose assets
//     state no digest: the SHA-256 is the one measured when it was pinned).
//   - BiRefNet's general model on the Swin-T backbone (the BiRefNet
//     repository's LICENSE is MIT), from its own release v1 (no digest
//     stated: measured).
//   - MODNet's portrait matting model (its repository's README: "The code,
//     models, and demos in this repository ... are released under the Apache
//     License 2.0"; read at commit 28165a45), as exported to ONNX for
//     Transformers.js on Hugging Face (Xenova/modnet, model card: license
//     apache-2.0), at a pinned revision, by Hugging Face's stated LFS digest
//     (D506).
//
// None is GPL or for non-commercial use only. What the models were trained on
// is their publishers' (WIDER Face; PaddleOCR's text sets; DUTS; DIS5K and
// others): noted, not resolved, as for the face fingerprint (D114).
var Files = []File{
	{ID: "screen-faces", Title: "YuNet", What: "finds faces and their eyes, nose and mouth corners",
		Name:   "face_detection_yunet_2026may.onnx",
		URL:    "https://github.com/opencv/opencv_zoo/raw/26cc381e4d2594bb9f47a26eb8fd96c94a13660d/models/face_detection_yunet/face_detection_yunet_2026may.onnx",
		SHA256: "ebafce4e3c118d6554634be5c27ab333b4c047a9a8c3faf1d7cf93101c22f0f0", Bytes: 229738,
		Licence: "MIT", LicenceURL: "https://github.com/opencv/opencv_zoo/blob/26cc381e4d2594bb9f47a26eb8fd96c94a13660d/models/face_detection_yunet/LICENSE",
		Hosts: githubMedia, Part: PartIndex},
	{ID: "screen-text-find", Title: "PP-OCRv5 mobile text detection", What: "finds the lines of text on screen",
		Name:   "PP-OCRv5_mobile_det.onnx",
		URL:    "https://huggingface.co/PaddlePaddle/PP-OCRv5_mobile_det_onnx/resolve/e6f4fa85f00e168c862bc462aebca69eef9b3d3d/inference.onnx",
		SHA256: "a431985659dc921974177a95adcfbb90fd9e51989a5e04d70d0b75f597b6e61d", Bytes: 4826518,
		Licence: "Apache-2.0", LicenceURL: "https://github.com/PaddlePaddle/PaddleOCR/blob/main/LICENSE", Hosts: hfCDN, Part: PartIndex},
	{ID: "screen-text-read", Title: "PP-OCRv6 small text recognition", What: "reads each line of text",
		Name:   "PP-OCRv6_small_rec.onnx",
		URL:    "https://huggingface.co/PaddlePaddle/PP-OCRv6_small_rec_onnx/resolve/b8f84f0b80c529de40b4fbb3544b84fa7233a513/inference.onnx",
		SHA256: "5435fd747c9e0efe15a96d0b378d5bd157e9492ed8fd80edf08f30d02fa24634", Bytes: 21159378,
		Licence: "Apache-2.0", LicenceURL: "https://github.com/PaddlePaddle/PaddleOCR/blob/main/LICENSE", Hosts: hfCDN, Part: PartIndex},
	{ID: "screen-text-chars", Title: "PP-OCRv6 small text recognition's characters", What: "the characters the text recognizer reads",
		Name:   "PP-OCRv6_small_rec.yml",
		URL:    "https://huggingface.co/PaddlePaddle/PP-OCRv6_small_rec_onnx/resolve/b8f84f0b80c529de40b4fbb3544b84fa7233a513/inference.yml",
		SHA256: "ab078671bb49f06228eadccd34f1bb501e157f7a047095ffb943ba81512c77d1", Bytes: 150579,
		Licence: "Apache-2.0", LicenceURL: "https://github.com/PaddlePaddle/PaddleOCR/blob/main/LICENSE", Part: PartIndex},
	{ID: "screen-attention", Title: "U²-Net-p", What: "finds what stands out where no face is seen",
		Name:   "u2netp.onnx",
		URL:    "https://github.com/danielgatis/rembg/releases/download/v0.0.0/u2netp.onnx",
		SHA256: "309c8469258dda742793dce0ebea8e6dd393174f89934733ecc8b14c76f4ddd8", Bytes: 4574861,
		Licence: "Apache-2.0 (U²-Net; the ONNX export is rembg's, MIT)", LicenceURL: "https://github.com/xuebinqin/U-2-Net/blob/master/LICENSE",
		Hosts: githubAssets, Part: PartIndex},
	{ID: "cut-outs", Title: "BiRefNet (general, Swin-T)", What: "cuts a picture's subject out",
		Name:   "BiRefNet-general-bb_swin_v1_tiny-epoch_232.onnx",
		URL:    "https://github.com/ZhengPeng7/BiRefNet/releases/download/v1/BiRefNet-general-bb_swin_v1_tiny-epoch_232.onnx",
		SHA256: "5600024376f572a557870a5eb0afb1e5961636bef4e1e22132025467d0f03333", Bytes: 224005088,
		Licence: "MIT", LicenceURL: "https://github.com/ZhengPeng7/BiRefNet/blob/main/LICENSE",
		Hosts: githubAssets, Part: PartCutOut},
	{ID: "video-cut-outs", Title: "MODNet", What: "tells the people in footage from their background",
		Name:   "modnet-portrait-matting.onnx",
		URL:    "https://huggingface.co/Xenova/modnet/resolve/fa2fa546052fba4c08921230a26cc69a333fca12/onnx/model.onnx",
		SHA256: "07c308cf0fc7e6e8b2065a12ed7fc07e1de8febb7dc7839d7b7f15dd66584df9", Bytes: 25888640,
		Licence: "Apache-2.0 (MODNet's code and models; the ONNX export is Xenova's for Transformers.js, Apache-2.0)", LicenceURL: "https://github.com/ZHKKKe/MODNet/blob/28165a451e4610c9d77cfdf925a94610bb2810fb/README.md#license",
		Hosts: hfCDN, Part: PartMatte},
}

// Folder is the models folder's subfolder these files go in.
const Folder = "screen-index"

// Dir is where the files live in a models folder.
func Dir(modelsDir string) string { return filepath.Join(modelsDir, Folder) }

// ModelName says which models made an index (the insight's model): the face
// finder, the text reader and what finds what stands out.
const ModelName = "yunet-2026may+ppocr-v5det-v6rec+u2netp"

// Store is a models folder with the pinned files it should hold.
type Store struct {
	Dir string
	// Files override the pinned files (tests).
	Files []File
}

// StoreIn is the store of a models folder (the computer's tools.modelsDir).
func StoreIn(modelsDir string) Store { return Store{Dir: Dir(modelsDir)} }

func (s Store) files() []File {
	if s.Files != nil {
		return s.Files
	}
	return Files
}

// Part lists a part's files.
func (s Store) Part(part string) []File {
	var out []File
	for _, f := range s.files() {
		if f.Part == part {
			out = append(out, f)
		}
	}
	return out
}

// Path is where a file is kept.
func (s Store) Path(f File) string { return filepath.Join(s.Dir, f.Name) }

// ByID is a file by its ID.
func (s Store) ByID(id string) (File, bool) {
	for _, f := range s.files() {
		if f.ID == id {
			return f, true
		}
	}
	return File{}, false
}

// checked remembers files whose SHA-256 was verified in this process (by path,
// size and time).
var checked sync.Map

// Verified is nil when a file is there and is the pinned file.
func (s Store) Verified(f File) error {
	p := s.Path(f)
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return fmt.Errorf("%s isn't here", f.Name)
	}
	key := fmt.Sprintf("%s|%d|%s|%s", p, st.Size(), st.ModTime(), f.SHA256)
	if v, ok := checked.Load(key); ok {
		if v.(bool) {
			return nil
		}
		return fmt.Errorf("%s isn't the file MetaRush pinned (its checksum differs)", f.Name)
	}
	got, err := fileSHA(p)
	ok := err == nil && got == f.SHA256
	checked.Store(key, ok)
	if !ok {
		return fmt.Errorf("%s isn't the file MetaRush pinned (its checksum differs)", f.Name)
	}
	return nil
}

// Ready is nil when every file of a part is here and as pinned; otherwise it
// says which isn't.
func (s Store) Ready(part string) error {
	files := s.Part(part)
	if len(files) == 0 {
		return fmt.Errorf("no files for %s", part)
	}
	var missing []string
	for _, f := range files {
		if err := s.Verified(f); err != nil {
			missing = append(missing, err.Error())
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s", strings.Join(missing, "; "))
	}
	return nil
}

// Present reports whether any file of a part is here (set up in part).
func (s Store) Present(part string) bool {
	for _, f := range s.Part(part) {
		if _, err := os.Stat(s.Path(f)); err == nil {
			return true
		}
	}
	return false
}

// IndexPaths are the index's files in this store.
func (s Store) IndexPaths() Paths {
	get := func(id string) string {
		if f, ok := s.ByID(id); ok {
			return s.Path(f)
		}
		return ""
	}
	return Paths{Faces: get("screen-faces"), TextDet: get("screen-text-find"), TextRec: get("screen-text-read"),
		TextDict: get("screen-text-chars"), Attention: get("screen-attention")}
}

// CutOutPath is the subject model's file in this store.
func (s Store) CutOutPath() string {
	if f, ok := s.ByID("cut-outs"); ok {
		return s.Path(f)
	}
	return ""
}

// MattePath is the matting model's file in this store.
func (s Store) MattePath() string {
	if f, ok := s.ByID("video-cut-outs"); ok {
		return s.Path(f)
	}
	return ""
}

// Bytes is how much a part downloads.
func (s Store) Bytes(part string) int64 {
	var n int64
	for _, f := range s.Part(part) {
		n += f.Bytes
	}
	return n
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
