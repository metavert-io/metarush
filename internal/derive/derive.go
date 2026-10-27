// Package derive produces the variants that make footage browsable and reviewable:
// proxies, thumbnails, contact sheets, waveform peaks, CFR mezzanines and frame
// grabs. Everything is generated from the immutable original; originals are never
// modified.
package derive

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/math/fixed"

	"github.com/metavert-io/metarush/internal/fontface"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
)

// Deriver runs derive operations with a tool set.
type Deriver struct {
	T *tools.Set
}

func (d *Deriver) ffmpeg(ctx context.Context, args ...string) error {
	_, err := d.T.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: append([]string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error"}, args...), Background: true})
	return err
}

// targetFPS picks a constant frame rate for a source.
func targetFPS(mi *models.MediaInfo, max float64) int {
	f := mi.FPS
	if f <= 0 {
		f = 30
	}
	std := []int{24, 25, 30, 50, 60}
	best := 30
	for _, s := range std {
		if math.Abs(float64(s)-f) < math.Abs(float64(best)-f) {
			best = s
		}
	}
	if float64(best) > max {
		best = int(max)
	}
	return best
}

// encoderArgs prefers the hardware encoder on Apple Silicon/macOS.
func (d *Deriver) encoderArgs(ctx context.Context, quality string) []string {
	if d.T.HasEncoder(ctx, "h264_videotoolbox") {
		br := map[string]string{"proxy": "2500k", "mezzanine": "40M"}[quality]
		return []string{"-c:v", "h264_videotoolbox", "-b:v", br, "-profile:v", "high", "-allow_sw", "1"}
	}
	crf := map[string]string{"proxy": "23", "mezzanine": "16"}[quality]
	preset := map[string]string{"proxy": "veryfast", "mezzanine": "medium"}[quality]
	return []string{"-c:v", "libx264", "-preset", preset, "-crf", crf}
}

// Proxy writes a 720p (short side), constant-frame-rate H.264 proxy with frequent
// keyframes so browser seeking is snappy. Rotation is applied (upright output).
func (d *Deriver) Proxy(ctx context.Context, in, out string, mi *models.MediaInfo) (*models.Variant, error) {
	fps := targetFPS(mi, 30)
	vf := fmt.Sprintf("scale='if(gt(iw,ih),-2,min(720,iw))':'if(gt(iw,ih),min(720,ih),-2)':flags=lanczos,fps=%d,format=yuv420p", fps)
	args := []string{"-i", in, "-map", "0:v:0", "-map", "0:a:0?", "-vf", vf}
	args = append(args, d.encoderArgs(ctx, "proxy")...)
	args = append(args, "-g", strconv.Itoa(fps), "-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709",
		"-c:a", "aac", "-b:a", "128k", "-ac", "2", "-ar", "48000", "-movflags", "+faststart", out)
	if err := d.ffmpeg(ctx, args...); err != nil {
		return nil, err
	}
	return d.describe(ctx, out, models.VariantProxy, "video/mp4", map[string]string{"fps": strconv.Itoa(fps), "short": "720"})
}

// Mezzanine writes a high-quality constant-frame-rate copy used instead of a
// variable-frame-rate original for frame-accurate rendering.
func (d *Deriver) Mezzanine(ctx context.Context, in, out string, mi *models.MediaInfo) (*models.Variant, error) {
	fps := targetFPS(mi, 60)
	args := []string{"-i", in, "-map", "0:v:0", "-map", "0:a:0?", "-vf", fmt.Sprintf("fps=%d,format=yuv420p", fps)}
	args = append(args, d.encoderArgs(ctx, "mezzanine")...)
	args = append(args, "-g", strconv.Itoa(fps), "-c:a", "aac", "-b:a", "256k", "-ar", "48000", "-movflags", "+faststart", out)
	if err := d.ffmpeg(ctx, args...); err != nil {
		return nil, err
	}
	return d.describe(ctx, out, models.VariantMezzanine, "video/mp4", map[string]string{"fps": strconv.Itoa(fps)})
}

// Thumbnail grabs a poster frame at ~10% of the duration.
func (d *Deriver) Thumbnail(ctx context.Context, in, out string, mi *models.MediaInfo) (*models.Variant, error) {
	at := math.Min(mi.Duration*0.1, 5)
	if err := d.ffmpeg(ctx, "-ss", fmt.Sprintf("%.3f", at), "-i", in, "-frames:v", "1", "-vf", "scale='if(gt(iw,ih),640,-2)':'if(gt(iw,ih),-2,640)'", "-q:v", "3", out); err != nil {
		return nil, err
	}
	return d.describe(ctx, out, models.VariantThumbnail, "image/jpeg", map[string]string{"at": fmt.Sprintf("%.3f", at)})
}

// Frame extracts one frame at t seconds to a JPEG (for Claude to look at).
func (d *Deriver) Frame(ctx context.Context, in, out string, t float64, width int) error {
	if width <= 0 {
		width = 960
	}
	return d.ffmpeg(ctx, "-ss", fmt.Sprintf("%.3f", t), "-i", in, "-frames:v", "1", "-vf", fmt.Sprintf("scale='if(gt(iw,ih),%d,-2)':'if(gt(iw,ih),-2,%d)'", width, width), "-q:v", "3", out)
}

// ContactSheet extracts n evenly spaced frames and lays them out in a labeled grid.
func (d *Deriver) ContactSheet(ctx context.Context, in, out, scratch string, mi *models.MediaInfo, n int) (*models.Variant, []float64, error) {
	return d.ContactSheetRange(ctx, in, out, scratch, mi, 0, mi.Duration, n)
}

// ContactSheetRange builds a sheet of n frames evenly spaced within [from, to).
func (d *Deriver) ContactSheetRange(ctx context.Context, in, out, scratch string, mi *models.MediaInfo, from, to float64, n int) (*models.Variant, []float64, error) {
	if n <= 0 {
		n = 12
	}
	span := to - from
	if span < 2 && n > 4 {
		n = 4
	}
	times := make([]float64, n)
	for i := range times {
		times[i] = math.Round((from+span*(float64(i)+0.5)/float64(n))*1000) / 1000
	}
	dir, err := os.MkdirTemp(scratch, "sheet-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	tileW := 400
	if mi.Height > mi.Width {
		tileW = 240
	}
	var frames []image.Image
	for i, t := range times {
		p := filepath.Join(dir, fmt.Sprintf("f%03d.jpg", i))
		if err := d.Frame(ctx, in, p, t, tileW); err != nil {
			return nil, nil, err
		}
		img, err := loadJPEG(p)
		if err != nil {
			return nil, nil, err
		}
		frames = append(frames, img)
	}
	cols := 4
	if mi.Height > mi.Width {
		cols = 6
	}
	if n < cols {
		cols = n
	}
	sheet := composeSheet(frames, times, cols)
	f, err := os.Create(out)
	if err != nil {
		return nil, nil, err
	}
	if err := jpeg.Encode(f, sheet, &jpeg.Options{Quality: 82}); err != nil {
		f.Close()
		return nil, nil, err
	}
	f.Close()
	v, err := d.describe(ctx, out, models.VariantContactSheet, "image/jpeg", map[string]string{"frames": strconv.Itoa(n), "cols": strconv.Itoa(cols)})
	if v != nil {
		v.Width, v.Height = sheet.Bounds().Dx(), sheet.Bounds().Dy()
	}
	return v, times, err
}

func loadJPEG(p string) (image.Image, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return jpeg.Decode(f)
}

// face makes the label face for one sheet (fontface: a face is never shared
// between sheets drawn at the same time).
func face() font.Face { return fontface.New("gomonobold", gomonobold.TTF, 15) }

func composeSheet(frames []image.Image, times []float64, cols int) *image.RGBA {
	tw, th := frames[0].Bounds().Dx(), frames[0].Bounds().Dy()
	gap, label := 6, 22
	rows := (len(frames) + cols - 1) / cols
	W := cols*tw + (cols+1)*gap
	H := rows*(th+label) + (rows+1)*gap
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{14, 14, 18, 255}}, image.Point{}, draw.Src)
	fc := face()
	if fc != nil {
		defer fc.Close()
	}
	for i, fr := range frames {
		x := gap + (i%cols)*(tw+gap)
		y := gap + (i/cols)*(th+label+gap)
		draw.Draw(img, image.Rect(x, y, x+tw, y+th), fr, fr.Bounds().Min, draw.Src)
		if fc != nil {
			dr := &font.Drawer{Dst: img, Src: &image.Uniform{color.RGBA{232, 228, 220, 255}}, Face: fc,
				Dot: fixed.P(x+2, y+th+16)}
			dr.DrawString(fmt.Sprintf("#%d  %s", i+1, Timecode(times[i])))
		}
	}
	return img
}

// Timecode formats seconds as M:SS.mmm.
func Timecode(t float64) string {
	m := int(t) / 60
	s := t - float64(m*60)
	return fmt.Sprintf("%d:%06.3f", m, s)
}

// PeaksFile is the peaks variant (served at /media/assets/{id}/peaks).
type PeaksFile struct {
	Version       int       `json:"version"`
	Duration      float64   `json:"duration"`
	BucketSeconds float64   `json:"bucketSeconds"`
	Peaks         []float64 `json:"peaks" jsonschema:"description=Max absolute amplitude per bucket (0 to 1)."`
}

// Peaks computes a waveform envelope (max |amplitude| per bucket, 0..1) streamed
// from ffmpeg so multi-hour files never load into memory.
func (d *Deriver) Peaks(ctx context.Context, in, out string, mi *models.MediaInfo) (*models.Variant, error) {
	buckets := int(math.Min(4000, math.Max(200, mi.Duration*25)))
	rate := 4000
	total := int(mi.Duration * float64(rate))
	per := total / buckets
	if per < 1 {
		per = 1
	}
	peaks := make([]float64, 0, buckets+1)
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		_, err := d.T.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-i", in, "-map", "0:a:0", "-ac", "1", "-ar", strconv.Itoa(rate), "-f", "s16le", "-"}, Stdout: pw, Background: true})
		pw.CloseWithError(err)
		errc <- err
	}()
	buf := make([]byte, 8192)
	var cur float64
	var count int
	var carry []byte
	for {
		n, err := pr.Read(buf)
		data := append(carry, buf[:n]...)
		usable := len(data) - len(data)%2
		for i := 0; i < usable; i += 2 {
			v := math.Abs(float64(int16(binary.LittleEndian.Uint16(data[i:]))) / 32768)
			if v > cur {
				cur = v
			}
			count++
			if count == per {
				peaks = append(peaks, math.Round(cur*1000)/1000)
				cur, count = 0, 0
			}
		}
		carry = append([]byte(nil), data[usable:]...)
		if err != nil {
			break
		}
	}
	if count > 0 {
		peaks = append(peaks, math.Round(cur*1000)/1000)
	}
	if err := <-errc; err != nil {
		return nil, err
	}
	b, _ := json.Marshal(PeaksFile{Version: 1, Duration: mi.Duration, BucketSeconds: float64(per) / float64(rate), Peaks: peaks})
	if err := os.WriteFile(out, b, 0o644); err != nil {
		return nil, err
	}
	return d.describe(ctx, out, models.VariantPeaks, "application/json", map[string]string{"buckets": strconv.Itoa(len(peaks))})
}

// ExtractAudioWAV writes 16 kHz mono PCM for transcription.
func (d *Deriver) ExtractAudioWAV(ctx context.Context, in, out string) error {
	return d.ffmpeg(ctx, "-i", in, "-map", "0:a:0", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", out)
}

func (d *Deriver) describe(ctx context.Context, path, kind, mime string, params map[string]string) (*models.Variant, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	v := &models.Variant{Kind: kind, Size: st.Size(), MIME: mime, Generator: "ffmpeg " + d.T.Version(ctx, tools.FFmpeg), Params: params}
	if bytes.HasPrefix([]byte(mime), []byte("video/")) || mime == "image/jpeg" {
		if res, err := d.T.Run(ctx, tools.Spec{Tool: tools.FFprobe, Args: []string{"-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height:format=duration", "-of", "json", path}}); err == nil {
			var p struct {
				Streams []struct{ Width, Height int } `json:"streams"`
				Format  struct {
					Duration string `json:"duration"`
				} `json:"format"`
			}
			if json.Unmarshal(res.Stdout, &p) == nil && len(p.Streams) > 0 {
				v.Width, v.Height = p.Streams[0].Width, p.Streams[0].Height
				v.Duration, _ = strconv.ParseFloat(p.Format.Duration, 64)
			}
		}
	}
	return v, nil
}
