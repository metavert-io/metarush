package catalog

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

// selectionJSON is the tool-selection eval (D223): plain requests with the
// operation that should be chosen for each.
//
//go:embed selection.json
var selectionJSON []byte

// SelectionCase is one request of the eval.
type SelectionCase struct {
	// Ask is the request ({project} and {clip} are names to fill in).
	Ask string `json:"ask"`
	// Op is the operation that should be chosen; Alt others that are right too.
	Op  string   `json:"op"`
	Alt []string `json:"alt,omitempty"`
	// N is its place in the file (1-based); every third is held out: find is
	// tuned on the others only, so the held-out score says how well it does
	// on requests nobody tuned it for.
	N       int  `json:"n"`
	Holdout bool `json:"holdout"`
}

// RightHit says whether a hit is a right choice: its name, or — for a
// first-party ability that runs an operation with some fields fixed — that
// operation (D271): choosing "steady shaky footage" for a request the case
// answers with asset.fixes.set runs asset.fixes.set.
func (c SelectionCase) RightHit(h Hit) bool {
	return c.Right(h.Name) || h.Kind == KindFirstParty && h.RunOwnInput && c.Right(h.RunWith)
}

// Right says whether an operation is a right choice for the case.
func (c SelectionCase) Right(op string) bool {
	if op == c.Op {
		return true
	}
	for _, a := range c.Alt {
		if a == op {
			return true
		}
	}
	return false
}

// Request is the case's words with the names filled in.
func (c SelectionCase) Request(names map[string]string) string {
	s := c.Ask
	for k, v := range names {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

// Selection is the eval's requests and the names its placeholders stand for.
type Selection struct {
	Cases []SelectionCase
	Names map[string]string
}

// LoadSelection reads the embedded eval.
func LoadSelection() (*Selection, error) {
	var f struct {
		Placeholders map[string]string `json:"placeholders"`
		Cases        []SelectionCase   `json:"cases"`
	}
	if err := json.Unmarshal(selectionJSON, &f); err != nil {
		return nil, fmt.Errorf("selection.json: %w", err)
	}
	for i := range f.Cases {
		f.Cases[i].N = i + 1
		f.Cases[i].Holdout = (i+1)%3 == 0
	}
	return &Selection{Cases: f.Cases, Names: f.Placeholders}, nil
}

// SelectionScore is how often find puts a right operation first, and among
// its first three.
type SelectionScore struct {
	Cases int `json:"cases"`
	Top1  int `json:"top1"`
	Top3  int `json:"top3"`
}

// Top1Rate and Top3Rate are the shares, 0 to 1.
func (s SelectionScore) Top1Rate() float64 { return rate(s.Top1, s.Cases) }
func (s SelectionScore) Top3Rate() float64 { return rate(s.Top3, s.Cases) }

func rate(n, of int) float64 {
	if of == 0 {
		return 0
	}
	return float64(n) / float64(of)
}

// SelectionMiss is a case find didn't put first.
type SelectionMiss struct {
	Case SelectionCase `json:"case"`
	// Got is what find put first, then second and third.
	Got []string `json:"got"`
	// Rank is where the right operation came (0: not in the first ten).
	Rank int `json:"rank"`
}

// SelectionResult is the eval's outcome for one ranking.
type SelectionResult struct {
	All     SelectionScore  `json:"all"`
	Tuned   SelectionScore  `json:"tuned"`
	Holdout SelectionScore  `json:"holdout"`
	Misses  []SelectionMiss `json:"misses"`
}

// ScoreSelection ranks the entries for every case's request.
func ScoreSelection(es []Entry, sel *Selection, r Ranking) SelectionResult {
	var res SelectionResult
	res.Misses = []SelectionMiss{}
	for _, c := range sel.Cases {
		hits := Rank(es, c.Request(sel.Names), r, 10)
		at := 0
		for i, h := range hits {
			if c.RightHit(h) {
				at = i + 1
				break
			}
		}
		for _, s := range []*SelectionScore{&res.All, map[bool]*SelectionScore{true: &res.Holdout, false: &res.Tuned}[c.Holdout]} {
			s.Cases++
			if at == 1 {
				s.Top1++
			}
			if at >= 1 && at <= 3 {
				s.Top3++
			}
		}
		if at != 1 {
			var got []string
			for i := 0; i < len(hits) && i < 3; i++ {
				got = append(got, hits[i].Name)
			}
			res.Misses = append(res.Misses, SelectionMiss{Case: c, Got: got, Rank: at})
		}
	}
	return res
}

// EvalEntries are what the eval ranks: what find searches with no
// workspace open — the operations and the first-party catalogue, each
// operation once (Searchable) — and the sample entries of catalogues that
// live in a workspace (abilities, D271).
func EvalEntries(ctx context.Context) ([]Entry, error) {
	es, err := All(ctx, nil)
	if err != nil {
		return nil, err
	}
	for _, s := range Sources() {
		if sm, ok := s.(Sampler); ok {
			es = append(es, sm.Samples()...)
		}
	}
	return Searchable(es), nil
}

// A Sampler is a catalogue whose entries live in a workspace (abilities):
// it offers typical entries for the eval to rank among the operations.
type Sampler interface {
	Samples() []Entry
}

// SelectionGate is what find must keep scoring on the eval (D223): go test
// fails below it, and `metarush skills eval` says whether it holds. The
// numbers sit just under what was measured when they were set; raise them
// when find gets better, and never lower them to let a change through.
var SelectionGate = struct {
	// Top1 and Top3: the right operation first, and among the first three.
	Top1, Top3 float64
	// HoldoutTop3: among the first three, on the cases find was not tuned on.
	HoldoutTop3 float64
	// PaddedTop3: among the first three with MadeUpEntries made-up entries in
	// the catalogue beside the operations (what abilities will add).
	PaddedTop3 float64
}{Top1: 0.93, Top3: 0.97, HoldoutTop3: 0.91, PaddedTop3: 0.97}

// MadeUpEntries is how many made-up entries the padded score adds.
const MadeUpEntries = 500

// Holds says whether a result meets the gate (padded: the score with
// MadeUpEntries made-up entries).
func (r SelectionResult) Holds(padded SelectionResult) bool {
	return r.All.Top1Rate() >= SelectionGate.Top1 && r.All.Top3Rate() >= SelectionGate.Top3 &&
		r.Holdout.Top3Rate() >= SelectionGate.HoldoutTop3 && padded.All.Top3Rate() >= SelectionGate.PaddedTop3
}

// KindMadeUp is the kind of MadeUp's entries.
const KindMadeUp = "made-up"

// MadeUp makes n made-up catalogue entries, the same every time: what other
// catalogues (abilities) might add — outside services and tools for video,
// pictures, sound and words, some of them changing things — so the eval can
// say how find holds up in a larger catalogue. The services' names are
// invented.
func MadeUp(n int) []Entry {
	verbs := []string{"Upscale", "Denoise", "Colorize", "Translate", "Dub", "Summarize", "Restyle", "Stabilize", "Retouch",
		"Animate", "Lip-sync", "Remix", "Master", "Compress", "Publish", "Schedule", "Tag", "Index", "Detect objects in", "Cut silences from",
		"Make a thumbnail for", "Write chapters for", "Clean the sound of", "Find music for", "Make a trailer from"}
	things := []string{"a clip", "a picture", "the narration", "a song", "a podcast episode", "a logo", "a short video", "a slide deck",
		"a web page", "an interview", "a screen recording", "a product photo", "a voice recording", "a social post", "a livestream"}
	services := []string{"Nimbus Studio", "Orbit Labs", "Pixelforge", "Soundcraft", "Lumen Cloud", "Quillworks", "Tandem AI",
		"Northwind Media", "Brightloop", "Cobalt Engine", "Fernway", "Halcyon Tools"}
	out := make([]Entry, 0, n)
	for i := 0; i < n; i++ {
		v, th, sv := verbs[i%len(verbs)], things[(i/len(verbs))%len(things)], services[(i*7)%len(services)]
		name := fmt.Sprintf("madeup-%03d", i)
		out = append(out, Entry{Name: name, Kind: KindMadeUp, About: fmt.Sprintf("%s %s with %s", v, th, sv), Changes: i%3 != 0,
			RunWith: "made.up", RunInput: map[string]any{"name": name}})
	}
	return out
}
