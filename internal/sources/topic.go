package sources

import (
	"sort"
	"strings"
	"unicode"
)

// What a recording is about, from its transcript (D150): the content words
// said most often, across the recording (not one sentence repeating them).
// No model and no network: a stop-word list and counts, so the same words
// always give the same topic.

// stopWords are words that say nothing about a topic: English function words,
// conversation fillers and very common verbs.
var stopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a about above actually after again against ago ah ahead all almost along already also although always am among an
and another any anybody anyone anything anyway anyways are aren't around as ask asked asking at away back be became because become becomes been before
began begin being believe below best better between big bit both bring but by call called came can can't cannot certain certainly come comes coming
could couldn't course day days did didn't different do does doesn't doing don't done down during each easy either else end enough especially even
ever every everybody everyone everything exactly example far feel feels felt few find first five for found four from front fun gave get gets getting
give given gives go goes going gone gonna good got gotta great guess guy guys had hadn't half happen happened happens hard has hasn't have haven't
having he he'd he'll he's hear heard hello help her here here's hers herself hey hi him himself his hm hmm honestly hope how however huh i i'd i'll
i'm i've if important in inside instead into is isn't it it'd it'll it's its itself just keep kind knew know knows last later least left less let
let's like liked likely little long look looked looking looks lot lots made make makes making man many may maybe me mean means meant might mine minute
minutes mm more most mostly much must my myself need needed needs never new next nice nine no nobody none nope nor not nothing now of off oh ok okay
old on once one ones only onto or other others our ours ourselves out over own part people perhaps person place point pretty probably put quite
rather real really right said same saw say saying says second see seem seemed seems seen several shall she she'd she'll she's should shouldn't show
side since six so some somebody someone something sometimes somewhat soon sorry sort start started stay still stuff such sure take taken takes taking
talk talked talking tell ten than thank thanks that that's the their theirs them themselves then there there's these they they'd they'll they're
they've thing things think thinking thought three through time times to today together told too took toward towards tried true try trying turn two
uh um under understand until up upon us use used using usually very via want wanted wants was wasn't way ways we we'd we'll we're we've week weeks well
went were weren't what what's whatever when where where's whether which while who who's whole whom whose why will wish with within without won't
wonder word words work worked works would wouldn't wow yeah year years yep yes yet you you'd you'll you're you've your yours yourself yourselves
ahh ahm eh er erm uhh umm uh-huh mhm oops alright gosh`) {
		stopWords[w] = true
	}
}

type tokenRun struct {
	words []string // lower case
	raw   []string // as written
}

// tokenize splits text into runs of content words (a stop word, a number or
// a short word breaks a run), keeping how each word was written.
func tokenize(text string) []tokenRun {
	var runs []tokenRun
	cur := tokenRun{}
	flush := func() {
		if len(cur.words) > 0 {
			runs = append(runs, cur)
		}
		cur = tokenRun{}
	}
	for _, f := range strings.FieldsFunc(text, func(r rune) bool { return !(unicode.IsLetter(r) || r == '\'' || r == '’' || unicode.IsDigit(r)) }) {
		raw := strings.Trim(strings.ReplaceAll(f, "’", "'"), "'")
		w := strings.ToLower(raw)
		w = strings.TrimSuffix(w, "'s")
		raw = strings.TrimSuffix(strings.TrimSuffix(raw, "'s"), "'S")
		if len([]rune(w)) < 4 || stopWords[w] || strings.IndexFunc(w, unicode.IsDigit) >= 0 {
			flush()
			continue
		}
		cur.words = append(cur.words, w)
		cur.raw = append(cur.raw, raw)
	}
	flush()
	return runs
}

// Topic is what a set of transcript segments is about, as a few words ready
// for "talks about …" ("the garden", "tomatoes", "Lisbon"), or "" when no
// content word stands out.
func Topic(segments []string) string {
	type stat struct {
		n        int
		segs     map[int]bool
		capitals int
		raw      string
	}
	uni := map[string]*stat{}
	bi := map[string]*stat{}
	add := func(m map[string]*stat, key, raw string, seg int, capital bool) {
		s := m[key]
		if s == nil {
			s = &stat{segs: map[int]bool{}, raw: raw}
			m[key] = s
		}
		s.n++
		s.segs[seg] = true
		if capital {
			s.capitals++
		}
	}
	total := 0
	for i, seg := range segments {
		for _, run := range tokenize(seg) {
			for j, w := range run.words {
				total++
				add(uni, w, run.raw[j], i, startsUpper(run.raw[j]))
				if j > 0 {
					key := run.words[j-1] + " " + w
					add(bi, key, run.raw[j-1]+" "+run.raw[j], i, startsUpper(run.raw[j-1]) && startsUpper(run.raw[j]))
				}
			}
		}
	}
	if total == 0 {
		return ""
	}
	type cand struct {
		key   string
		score float64
		st    *stat
	}
	var cands []cand
	for k, s := range bi {
		if s.n >= 2 && len(s.segs) >= 2 {
			cands = append(cands, cand{k, float64(s.n) * 2.2, s})
		}
	}
	for k, s := range uni {
		// A word said three times in two places, and often enough for the
		// length of the recording.
		if s.n >= 3 && len(s.segs) >= 2 && float64(s.n) >= 0.01*float64(total) {
			cands = append(cands, cand{k, float64(s.n), s})
		}
	}
	if len(cands) == 0 {
		return ""
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		if len(cands[i].key) != len(cands[j].key) {
			return len(cands[i].key) > len(cands[j].key)
		}
		return cands[i].key < cands[j].key
	})
	best := cands[0]
	// A name (written with a capital most of the time) keeps its capitals and
	// takes no article; a plural takes none either.
	if best.st.capitals*2 > best.st.n {
		return best.st.raw
	}
	if strings.HasSuffix(best.key, "s") && !strings.HasSuffix(best.key, "ss") {
		return best.key
	}
	return "the " + best.key
}

func startsUpper(s string) bool {
	for _, r := range s {
		return unicode.IsUpper(r)
	}
	return false
}
