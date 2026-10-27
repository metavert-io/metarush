package evidence

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/store"
)

// Momentary loudness: ebur128 measures the loudness of the last 400 ms every
// 100 ms. Keeping that series with an asset's evidence lets MetaRush know how
// loud any part of a recording is (the overall integrated loudness of a long
// recording says little about one answer in it): speech leveling evens clips out
// from it, and QA measures loudness jumps across cuts in a render the same way.

// MomentaryStep is the spacing of the series (seconds).
const MomentaryStep = 0.1

// InsightKind is the insight that keeps an asset's momentary loudness.
const InsightKind = "loudness"

// Series is a momentary-loudness series: LUFS[k] is the loudness of the 400 ms
// ending at (k+1)·Step seconds (ebur128's M at pts k·Step).
type Series struct {
	Step float64
	LUFS []float64
}

// MomentaryFilter is the ffmpeg filter suffix that writes the series to file
// (append it to an ebur128 filter with metadata=1).
func MomentaryFilter(file string) string {
	return ",ametadata=mode=print:key=lavfi.r128.M:file=" + escapeFilterPath(file)
}

// ReadMomentary parses the file MomentaryFilter wrote.
func ReadMomentary(file string) (*Series, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := &Series{Step: MomentaryStep}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, "lavfi.r128.M="); ok {
			x, err := strconv.ParseFloat(v, 64)
			if err != nil || math.IsNaN(x) || math.IsInf(x, 0) || x < -120 {
				x = -120
			}
			s.LUFS = append(s.LUFS, math.Round(x*10)/10)
		}
	}
	return s, sc.Err()
}

// Integrated is the BS.1770 gated loudness of [from, to) seconds: the 400 ms
// blocks inside the range (75% overlap at a 100 ms step, as the standard has
// it), an absolute gate at -70 LUFS and a relative gate 10 LU under the mean.
// ok is false when fewer than three blocks fit or all are silent.
func (s *Series) Integrated(from, to float64) (float64, bool) {
	if s == nil || s.Step <= 0 || to-from < 0.6 {
		return 0, false
	}
	var es []float64
	for k, l := range s.LUFS {
		end := float64(k+1) * s.Step
		if end-0.4 < from-1e-9 || end > to+1e-9 {
			continue
		}
		if l > -70 {
			es = append(es, math.Pow(10, l/10))
		}
	}
	if len(es) < 3 {
		return 0, false
	}
	mean := func(xs []float64) float64 {
		t := 0.0
		for _, x := range xs {
			t += x
		}
		return t / float64(len(xs))
	}
	abs := 10 * math.Log10(mean(es))
	var rel []float64
	for _, e := range es {
		if 10*math.Log10(e) > abs-10 {
			rel = append(rel, e)
		}
	}
	if len(rel) == 0 {
		return abs, true
	}
	return 10 * math.Log10(mean(rel)), true
}

// Encode packs the series compactly for storage: little-endian int16 tenths of
// an LU, base64.
func (s *Series) Encode() string {
	b := make([]byte, 2*len(s.LUFS))
	for i, l := range s.LUFS {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(int16(math.Round(math.Max(-1200, math.Min(300, l*10))))))
	}
	return base64.StdEncoding.EncodeToString(b)
}

// DecodeSeries unpacks Encode's output.
func DecodeSeries(step float64, data string) (*Series, error) {
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, err
	}
	if len(b)%2 != 0 {
		return nil, fmt.Errorf("momentary loudness: odd length")
	}
	s := &Series{Step: step, LUFS: make([]float64, len(b)/2)}
	for i := range s.LUFS {
		s.LUFS[i] = float64(int16(binary.LittleEndian.Uint16(b[2*i:]))) / 10
	}
	return s, nil
}

// SeriesData is what the loudness insight stores.
func (s *Series) SeriesData() map[string]any {
	return map[string]any{"step": s.Step, "encoding": "int16le-decilu-base64", "points": len(s.LUFS), "values": s.Encode()}
}

// LoadSeries reads the momentary loudness of assets (the latest per asset);
// assets measured before it was kept are absent.
func LoadSeries(ctx context.Context, db *store.DB, workspaceID string, assetIDs []string, sel ...store.InsightMedia) map[string]*Series {
	out := map[string]*Series{}
	if db == nil || len(assetIDs) == 0 {
		return out
	}
	var docs []struct {
		AssetID   string    `bson:"assetId"`
		CreatedAt time.Time `bson:"createdAt"`
		Data      struct {
			Step   float64 `bson:"step"`
			Values string  `bson:"values"`
		} `bson:"data"`
	}
	f := store.MediaFilter(assetIDs, db.MediaFor(ctx, workspaceID, assetIDs, sel))
	f["kind"] = InsightKind
	if err := db.Find(ctx, store.CInsights, workspaceID, f, &docs, options.Find().SetSort(store.MediaSort)); err != nil {
		return out
	}
	for _, d := range docs {
		if s, err := DecodeSeries(d.Data.Step, d.Data.Values); err == nil && len(s.LUFS) > 0 {
			out[d.AssetID] = s
		}
	}
	return out
}
