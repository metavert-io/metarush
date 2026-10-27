// Package sound makes voices clearer and times music (D88–D91). It holds the
// audio work behind `metarush sound …` and the dashboard's Sound tab:
//
//   - enhancement pipelines that turn a stretch of a recording into a processed
//     ("wet") voice: clean (DeepFilterNet for noise and room echo, then
//     de-plosive, a gentle presence EQ, de-ess, smooth compression and
//     leveling, all on this machine), studio (Resemble Enhance, an optional
//     Python component that runs on the Apple GPU when there is one) and cloud
//     (ElevenLabs voice isolation, priced first and budgeted);
//   - the assembly of a processed copy of a recording: each person's stretches
//     mixed from their processed voice at their strength (wet/dry), short
//     crossfades where one person's stretch meets another's, the original
//     everywhere else, the level matched to the original so the mixer's
//     loudness facts stay true;
//   - proof it helped: a voice clarity score before and after (DNSMOS P.835,
//     the ONNX model run in-process through onnxruntime) and a background noise
//     estimate, with plain warnings when processing may have gone too far;
//   - music analysis (beats) for timing music to the video.
//
// Results are content-addressed (input hash + settings + model versions) and
// cached in MongoDB and the media store, so nothing is processed — or paid
// for — twice. Recordings are never changed.
package sound

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// Audio is interleaved float samples.
type Audio struct {
	Rate     int
	Channels int
	Data     []float32 // interleaved
}

// Frames is the number of sample frames.
func (a *Audio) Frames() int {
	if a == nil || a.Channels <= 0 {
		return 0
	}
	return len(a.Data) / a.Channels
}

// Seconds is the length.
func (a *Audio) Seconds() float64 {
	if a == nil || a.Rate <= 0 {
		return 0
	}
	return float64(a.Frames()) / float64(a.Rate)
}

// Mono mixes the channels down (their average).
func (a *Audio) Mono() []float32 {
	n := a.Frames()
	if a.Channels == 1 {
		return append([]float32(nil), a.Data[:n]...)
	}
	out := make([]float32, n)
	c := a.Channels
	inv := 1 / float32(c)
	for i := 0; i < n; i++ {
		var s float32
		for k := 0; k < c; k++ {
			s += a.Data[i*c+k]
		}
		out[i] = s * inv
	}
	return out
}

// ReadWAV reads a PCM (16, 24, 32-bit) or float (32, 64-bit) WAV file.
func ReadWAV(path string) (*Audio, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readWAV(bufio.NewReaderSize(f, 1<<20))
}

func readWAV(r io.Reader) (*Audio, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, fmt.Errorf("not a WAV file: %w", err)
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return nil, errors.New("not a WAV file")
	}
	var format, channels, bits int
	var rate int
	for {
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			return nil, errors.New("WAV file has no audio data")
		}
		id := string(ch[0:4])
		size := int64(binary.LittleEndian.Uint32(ch[4:]))
		switch id {
		case "fmt ":
			body := make([]byte, size)
			if _, err := io.ReadFull(r, body); err != nil {
				return nil, err
			}
			if len(body) < 16 {
				return nil, errors.New("WAV format chunk too short")
			}
			format = int(binary.LittleEndian.Uint16(body[0:]))
			channels = int(binary.LittleEndian.Uint16(body[2:]))
			rate = int(binary.LittleEndian.Uint32(body[4:]))
			bits = int(binary.LittleEndian.Uint16(body[14:]))
			if format == 0xFFFE && len(body) >= 26 { // WAVE_FORMAT_EXTENSIBLE: the subformat's first two bytes
				format = int(binary.LittleEndian.Uint16(body[24:]))
			}
			if size%2 == 1 {
				_, _ = io.CopyN(io.Discard, r, 1)
			}
		case "data":
			if channels <= 0 || rate <= 0 {
				return nil, errors.New("WAV data before its format")
			}
			if size == 0xFFFFFFFF || size == 0 { // streamed WAV (ffmpeg to a pipe): read to the end
				size = -1
			}
			return readSamples(r, size, format, channels, rate, bits)
		default:
			if _, err := io.CopyN(io.Discard, r, size+size%2); err != nil {
				return nil, errors.New("WAV file has no audio data")
			}
		}
	}
}

func readSamples(r io.Reader, size int64, format, channels, rate, bits int) (*Audio, error) {
	var src io.Reader = r
	if size >= 0 {
		src = io.LimitReader(r, size)
	}
	raw, err := io.ReadAll(src)
	if err != nil {
		return nil, err
	}
	a := &Audio{Rate: rate, Channels: channels}
	switch {
	case format == 1 && bits == 16:
		n := len(raw) / 2
		a.Data = make([]float32, n)
		for i := 0; i < n; i++ {
			a.Data[i] = float32(int16(binary.LittleEndian.Uint16(raw[2*i:]))) / 32768
		}
	case format == 1 && bits == 24:
		n := len(raw) / 3
		a.Data = make([]float32, n)
		for i := 0; i < n; i++ {
			v := int32(raw[3*i]) | int32(raw[3*i+1])<<8 | int32(int8(raw[3*i+2]))<<16
			a.Data[i] = float32(v) / 8388608
		}
	case format == 1 && bits == 32:
		n := len(raw) / 4
		a.Data = make([]float32, n)
		for i := 0; i < n; i++ {
			a.Data[i] = float32(float64(int32(binary.LittleEndian.Uint32(raw[4*i:]))) / 2147483648)
		}
	case format == 3 && bits == 32:
		n := len(raw) / 4
		a.Data = make([]float32, n)
		for i := 0; i < n; i++ {
			a.Data[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
		}
	case format == 3 && bits == 64:
		n := len(raw) / 8
		a.Data = make([]float32, n)
		for i := 0; i < n; i++ {
			a.Data[i] = float32(math.Float64frombits(binary.LittleEndian.Uint64(raw[8*i:])))
		}
	default:
		return nil, fmt.Errorf("unsupported WAV encoding (format %d, %d bits)", format, bits)
	}
	if rem := len(a.Data) % channels; rem != 0 {
		a.Data = a.Data[:len(a.Data)-rem]
	}
	return a, nil
}

// WriteWAV writes 16-bit PCM (with TPDF dither) or 32-bit float.
func WriteWAV(path string, a *Audio, float bool) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	bits, format := 16, 1
	if float {
		bits, format = 32, 3
	}
	dataLen := len(a.Data) * bits / 8
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+dataLen))
	copy(hdr[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], uint16(format))
	binary.LittleEndian.PutUint16(hdr[22:], uint16(a.Channels))
	binary.LittleEndian.PutUint32(hdr[24:], uint32(a.Rate))
	binary.LittleEndian.PutUint32(hdr[28:], uint32(a.Rate*a.Channels*bits/8))
	binary.LittleEndian.PutUint16(hdr[32:], uint16(a.Channels*bits/8))
	binary.LittleEndian.PutUint16(hdr[34:], uint16(bits))
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], uint32(dataLen))
	if _, err := w.Write(hdr); err != nil {
		f.Close()
		return err
	}
	buf := make([]byte, 4)
	// A fixed-seed dither: the same input always gives the same bytes (content addressing).
	rng := uint32(0x9E3779B9)
	next := func() float32 {
		rng ^= rng << 13
		rng ^= rng >> 17
		rng ^= rng << 5
		return float32(rng)/4294967296 - 0.5
	}
	for _, v := range a.Data {
		if float {
			binary.LittleEndian.PutUint32(buf, math.Float32bits(v))
			if _, err := w.Write(buf[:4]); err != nil {
				f.Close()
				return err
			}
			continue
		}
		x := float64(v)*32767 + float64(next()+next())
		x = math.Round(math.Max(-32768, math.Min(32767, x)))
		binary.LittleEndian.PutUint16(buf, uint16(int16(x)))
		if _, err := w.Write(buf[:2]); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
