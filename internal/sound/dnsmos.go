package sound

import (
	"errors"
	"math"
	"sync"

	"github.com/metavert-io/metarush/internal/onnx"
)

// The voice clarity score is DNSMOS P.835 (Reddy, Gopal and Cutler, ICASSP
// 2022), Microsoft's non-intrusive speech quality model: from 16 kHz audio and
// no reference it predicts how listeners would rate the speech itself (SIG),
// the background (BAK) and the whole (OVRL) on the 1–5 opinion scale. The
// code in microsoft/DNS-Challenge is MIT (LICENSE-CODE) and the repository
// is CC BY 4.0 (LICENSE); MetaRush downloads the model file from a pinned
// commit and checks its SHA-256, and implements dnsmos_local.py's scoring here
// (9.01 s windows a second apart, the published polynomial mapping), so no
// Python is needed. People see OVRL as the "voice clarity score".

// DNSMOSRate is the sample rate the model takes.
const DNSMOSRate = 16000

const dnsmosWindow = 9.01 // seconds per window (dnsmos_local.py INPUT_LENGTH)

// dnsmosMaxWindows caps the windows scored in a long stretch (spread evenly).
const dnsmosMaxWindows = 60

// Clarity is a DNSMOS P.835 score.
type Clarity struct {
	Overall    float64 `json:"overall" jsonschema:"description=OVRL: overall quality as people would rate it (1 bad … 5 excellent)."`
	Speech     float64 `json:"speech" jsonschema:"description=SIG: the voice itself (distortion\\, processing artifacts)."`
	Background float64 `json:"background" jsonschema:"description=BAK: how intrusive the background is (5 = not at all)."`
	Seconds    float64 `json:"seconds" jsonschema:"description=How much audio was scored."`
	Windows    int     `json:"windows"`
}

// polyfit maps the model's raw outputs to the opinion scale (np.poly1d,
// highest power first; the regular, not personalized, model).
var (
	polyOVRL = [3]float64{-0.06766283, 1.11546468, 0.04602535}
	polySIG  = [3]float64{-0.08397278, 1.22083953, 0.0052439}
	polyBAK  = [3]float64{-0.13166888, 1.60915514, -0.39604546}
)

func poly2(p [3]float64, x float64) float64 { return p[0]*x*x + p[1]*x + p[2] }

// Scorer runs DNSMOS. It is safe for concurrent use.
type Scorer struct {
	m *onnx.Model
}

var (
	scorerMu    sync.Mutex
	scorerCache = map[string]*Scorer{}
)

// OpenScorer loads the model with onnxruntime (lib) once per process.
func OpenScorer(lib, model string) (*Scorer, error) {
	if lib == "" || model == "" {
		return nil, ErrScoreUnavailable
	}
	scorerMu.Lock()
	defer scorerMu.Unlock()
	key := lib + "\x00" + model
	if s, ok := scorerCache[key]; ok {
		return s, nil
	}
	m, err := onnx.Open(lib, model, 2)
	if err != nil {
		return nil, err
	}
	s := &Scorer{m: m}
	scorerCache[key] = s
	return s, nil
}

// ErrScoreUnavailable means the score's model or onnxruntime is not installed.
var ErrScoreUnavailable = errors.New("the voice clarity score is not set up on this machine (set it up on the Sound tab)")

// Score rates 16 kHz mono speech. Audio shorter than a window is repeated to
// fill it, as dnsmos_local.py does.
func (s *Scorer) Score(x []float32) (Clarity, error) {
	if s == nil || s.m == nil {
		return Clarity{}, ErrScoreUnavailable
	}
	if len(x) < DNSMOSRate/4 {
		return Clarity{}, errors.New("too little audio to score")
	}
	actual := float64(len(x)) / DNSMOSRate
	need := int(dnsmosWindow * DNSMOSRate)
	audio := x
	for len(audio) < need {
		audio = append(append([]float32(nil), audio...), audio...)
	}
	hops := int(math.Floor(float64(len(audio))/DNSMOSRate)-dnsmosWindow) + 1
	idx := make([]int, 0, hops)
	for i := 0; i < hops; i++ {
		idx = append(idx, i)
	}
	if hops > dnsmosMaxWindows { // a long stretch: windows spread evenly over it
		idx = idx[:0]
		for k := 0; k < dnsmosMaxWindows; k++ {
			idx = append(idx, k*(hops-1)/(dnsmosMaxWindows-1))
		}
	}
	var sig, bak, ovr float64
	n := 0
	shape := []int64{1, int64(need)}
	for _, i := range idx {
		from := i * DNSMOSRate
		to := int((float64(i) + dnsmosWindow) * DNSMOSRate)
		if to > len(audio) || to-from < need {
			continue
		}
		seg := make([]float32, need)
		copy(seg, audio[from:from+need])
		out, err := s.m.Run(seg, shape)
		if err != nil {
			return Clarity{}, err
		}
		if len(out) == 0 || len(out[0].Data) < 3 {
			return Clarity{}, errors.New("the voice clarity model gave no answer")
		}
		r := out[0].Data
		sig += poly2(polySIG, float64(r[0]))
		bak += poly2(polyBAK, float64(r[1]))
		ovr += poly2(polyOVRL, float64(r[2]))
		n++
	}
	if n == 0 {
		return Clarity{}, errors.New("too little audio to score")
	}
	round2 := func(v float64) float64 { return math.Round(v*100) / 100 }
	return Clarity{Overall: round2(ovr / float64(n)), Speech: round2(sig / float64(n)), Background: round2(bak / float64(n)),
		Seconds: math.Round(actual*10) / 10, Windows: n}, nil
}
