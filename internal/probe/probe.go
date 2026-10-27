// Package probe inspects media with ffprobe and normalizes the result. Content is
// identified by what ffprobe can actually decode, never by file extension.
package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
)

type ffStream struct {
	Index         int               `json:"index"`
	CodecType     string            `json:"codec_type"`
	CodecName     string            `json:"codec_name"`
	Profile       string            `json:"profile"`
	Width         int               `json:"width"`
	Height        int               `json:"height"`
	PixFmt        string            `json:"pix_fmt"`
	RFrameRate    string            `json:"r_frame_rate"`
	AvgFrameRate  string            `json:"avg_frame_rate"`
	TimeBase      string            `json:"time_base"`
	ColorSpace    string            `json:"color_space"`
	ColorTransfer string            `json:"color_transfer"`
	ColorPrim     string            `json:"color_primaries"`
	SampleRate    string            `json:"sample_rate"`
	Channels      int               `json:"channels"`
	Duration      string            `json:"duration"`
	BitRate       string            `json:"bit_rate"`
	NbFrames      string            `json:"nb_frames"`
	Disposition   map[string]int    `json:"disposition"`
	Tags          map[string]string `json:"tags"`
	SideData      []map[string]any  `json:"side_data_list"`
}

type ffFormat struct {
	FormatName string            `json:"format_name"`
	Duration   string            `json:"duration"`
	BitRate    string            `json:"bit_rate"`
	Tags       map[string]string `json:"tags"`
}

type ffOut struct {
	Streams []ffStream `json:"streams"`
	Format  ffFormat   `json:"format"`
}

// ErrNotMedia means ffprobe found no decodable audio/video.
type ErrNotMedia struct{ Reason string }

func (e *ErrNotMedia) Error() string { return "not a supported media file: " + e.Reason }

// Probe runs ffprobe on a local file.
func Probe(ctx context.Context, t *tools.Set, path string) (*models.MediaInfo, error) {
	res, err := t.Run(ctx, tools.Spec{Tool: tools.FFprobe, Args: []string{"-v", "error", "-print_format", "json", "-show_format", "-show_streams", "--", path}})
	if err != nil {
		if fe, ok := err.(*tools.ErrFailed); ok {
			return nil, &ErrNotMedia{Reason: firstLine(fe.Stderr)}
		}
		return nil, err
	}
	var out ffOut
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}
	return normalize(out)
}

func normalize(out ffOut) (*models.MediaInfo, error) {
	mi := &models.MediaInfo{Container: out.Format.FormatName, Duration: atof(out.Format.Duration), BitRate: atoi64(out.Format.BitRate)}
	mi.CreationTime = out.Format.Tags["creation_time"]
	mi.Device, mi.Software, mi.TagTitle, mi.TagArtist = fileTags(out.Format.Tags)
	var videoSeen bool
	for _, s := range out.Streams {
		st := models.Stream{
			Index: s.Index, Type: s.CodecType, Codec: s.CodecName, Profile: s.Profile, Width: s.Width, Height: s.Height,
			PixFmt: s.PixFmt, RFrameRate: s.RFrameRate, AvgRate: s.AvgFrameRate, TimeBase: s.TimeBase,
			ColorSpace: s.ColorSpace, ColorTRC: s.ColorTransfer, ColorPrim: s.ColorPrim,
			SampleRate: int(atoi64(s.SampleRate)), Channels: s.Channels, Duration: atof(s.Duration), BitRate: atoi64(s.BitRate),
		}
		switch s.CodecType {
		case "video":
			if s.Disposition["attached_pic"] == 1 || isStill(s.CodecName) && mi.Duration == 0 {
				st.Type = "image"
				mi.Streams = append(mi.Streams, st)
				continue
			}
			st.Rotation = rotation(s)
			mi.Streams = append(mi.Streams, st)
			if videoSeen {
				continue
			}
			videoSeen = true
			mi.HasVideo = true
			w, h := s.Width, s.Height
			if st.Rotation == 90 || st.Rotation == 270 {
				w, h = h, w
			}
			mi.Width, mi.Height, mi.Rotation = w, h, st.Rotation
			r, avg := rate(s.RFrameRate), rate(s.AvgFrameRate)
			mi.FPS = avg
			if mi.FPS == 0 {
				mi.FPS = r
			}
			// Variable frame rate: the nominal rate and the average disagree.
			if r > 0 && avg > 0 && math.Abs(r-avg)/r > 0.01 {
				mi.VFR = true
			}
			if s.ColorTransfer == "smpte2084" || s.ColorTransfer == "arib-std-b67" || s.ColorPrim == "bt2020" {
				mi.HDR = true
			}
		case "audio":
			mi.HasAudio = true
			mi.Streams = append(mi.Streams, st)
		default:
			mi.Streams = append(mi.Streams, st)
		}
	}
	if mi.Duration == 0 {
		for _, s := range mi.Streams {
			mi.Duration = math.Max(mi.Duration, s.Duration)
		}
	}
	if !mi.HasVideo && !mi.HasAudio {
		if len(mi.Streams) > 0 && mi.Streams[0].Type == "image" {
			return mi, nil
		}
		return nil, &ErrNotMedia{Reason: "no audio or video streams"}
	}
	if mi.Duration <= 0 {
		return nil, &ErrNotMedia{Reason: "zero duration"}
	}
	return mi, nil
}

// Kind classifies probed media.
func Kind(mi *models.MediaInfo) string {
	switch {
	case mi.HasVideo:
		return "video"
	case mi.HasAudio:
		return "audio"
	default:
		return "image"
	}
}

// MIME guesses a content type from the container.
func MIME(mi *models.MediaInfo, name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(mi.Container, "mov") && strings.HasSuffix(lower, ".mov"):
		return "video/quicktime"
	case strings.Contains(mi.Container, "mp4") && !mi.HasVideo:
		return "audio/mp4"
	case strings.Contains(mi.Container, "mp4"), strings.Contains(mi.Container, "mov"):
		return "video/mp4"
	case strings.Contains(mi.Container, "matroska"), strings.Contains(mi.Container, "webm"):
		if strings.HasSuffix(lower, ".webm") {
			return "video/webm"
		}
		return "video/x-matroska"
	case mi.Container == "wav":
		return "audio/wav"
	case mi.Container == "mp3":
		return "audio/mpeg"
	case strings.Contains(mi.Container, "aac"):
		return "audio/aac"
	case mi.Container == "flac":
		return "audio/flac"
	}
	return "application/octet-stream"
}

func isStill(codec string) bool {
	switch codec {
	case "mjpeg", "png", "webp", "bmp", "tiff", "gif":
		return true
	}
	return false
}

func rotation(s ffStream) int {
	r := 0
	if v, ok := s.Tags["rotate"]; ok {
		r, _ = strconv.Atoi(v)
	}
	for _, sd := range s.SideData {
		if v, ok := sd["rotation"]; ok {
			switch x := v.(type) {
			case float64:
				r = int(x)
			case string:
				r, _ = strconv.Atoi(x)
			}
		}
	}
	r = ((r % 360) + 360) % 360
	return r
}

func rate(s string) float64 {
	n, d, ok := strings.Cut(s, "/")
	if !ok {
		return atof(s)
	}
	dn := atof(d)
	if dn == 0 {
		return 0
	}
	return atof(n) / dn
}

func atof(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
func atoi64(s string) int64 { v, _ := strconv.ParseInt(s, 10, 64); return v }

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// fileTags reads what a file says about itself (D151): the camera or phone
// that recorded it, the app or system that wrote it, and its title and
// artist (music, exported videos). Tag names vary by maker; values are
// trimmed and capped, and empty when the file says nothing.
func fileTags(tags map[string]string) (device, software, title, artist string) {
	get := func(keys ...string) string {
		for _, k := range keys {
			for tk, v := range tags {
				if strings.EqualFold(tk, k) {
					if v = strings.Join(strings.Fields(v), " "); v != "" {
						return capRunes(v, 120)
					}
				}
			}
		}
		return ""
	}
	maker := get("com.apple.quicktime.make", "make", "com.android.manufacturer")
	model := get("com.apple.quicktime.model", "model", "com.android.model")
	switch {
	case maker != "" && model != "" && !strings.HasPrefix(strings.ToLower(model), strings.ToLower(maker)):
		device = maker + " " + model
	case model != "":
		device = model
	default:
		device = maker
	}
	software = get("com.apple.quicktime.software", "software", "com.android.version")
	if software == "" {
		// An encoder tag names the app that wrote the file only when it isn't a
		// plain library version ("Lavf60.3.100").
		if enc := get("encoder", "encoded_by"); enc != "" && !strings.HasPrefix(enc, "Lav") {
			software = enc
		}
	}
	title = get("title", "com.apple.quicktime.title")
	artist = get("artist", "album_artist", "com.apple.quicktime.artist")
	return device, software, title, artist
}

func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
