package evidence

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestMomentaryIntegratedGatesSilenceAndRoundTrips(t *testing.T) {
	// 2 s of speech at -20 LUFS, 1 s of near-silence, 2 s at -30 LUFS.
	s := &Series{Step: MomentaryStep}
	for i := 0; i < 50; i++ {
		v := -20.0
		switch {
		case i >= 20 && i < 30:
			v = -95
		case i >= 30:
			v = -30
		}
		s.LUFS = append(s.LUFS, v)
	}
	if l, ok := s.Integrated(0, 2); !ok || math.Abs(l+20) > 0.01 {
		t.Fatalf("first part: %v %v", l, ok)
	}
	if l, ok := s.Integrated(3, 5); !ok || math.Abs(l+30) > 0.01 {
		t.Fatalf("second part: %v %v", l, ok)
	}
	// The silence is gated out; the quieter speech is more than 10 LU under the
	// mix's ungated mean only partly, so the result sits near the loud part.
	if l, ok := s.Integrated(0, 5); !ok || l < -23 || l > -20 {
		t.Fatalf("whole: %v", l)
	}
	if _, ok := s.Integrated(0, 0.3); ok {
		t.Fatal("too short to measure")
	}
	var nilSeries *Series
	if _, ok := nilSeries.Integrated(0, 5); ok {
		t.Fatal("nil series measured")
	}
	back, err := DecodeSeries(s.Step, s.Encode())
	if err != nil || len(back.LUFS) != len(s.LUFS) || back.LUFS[0] != -20 || back.LUFS[25] != -95 || back.LUFS[49] != -30 {
		t.Fatalf("round trip: %v %+v", err, back)
	}
}

func TestReadMomentaryParsesFfmpegOutput(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.txt")
	if err := os.WriteFile(p, []byte("frame:0    pts:0       pts_time:0\nlavfi.r128.M=-120.691\nframe:1    pts:4800    pts_time:0.1\nlavfi.r128.M=-23.44\nframe:2    pts:9600    pts_time:0.2\nlavfi.r128.M=-inf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := ReadMomentary(p)
	if err != nil || len(s.LUFS) != 3 || s.LUFS[0] != -120 || s.LUFS[1] != -23.4 || s.LUFS[2] != -120 {
		t.Fatalf("parse: %v %+v", err, s)
	}
}
