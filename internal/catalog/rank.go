package catalog

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Ranking says how find scores entries: BM25F over weighted fields, with
// optional stemming, synonyms and phrase matches.
type Ranking struct {
	// Field weights; a zero weight leaves the field out.
	Name, Command, Words, About, CommandAbout float64
	// Stem: plural and -ing/-ed forms count as one word.
	Stem bool
	// Synonyms: a request's words also match the catalogue's words for the
	// same thing ("subtitles" finds captions), at SynonymWeight.
	Synonyms      bool
	SynonymWeight float64
	// Phrases: two words of the request next to each other in an entry's
	// name, command or words ("review copy") add PhraseWeight.
	Phrases      bool
	PhraseWeight float64
	// Intent: a request that asks to look ("show", "which", "is …?") favours
	// entries that change nothing, and one that asks to change ("add",
	// "remove", "make") those that change, by IntentWeight (a fraction of
	// the score).
	Intent       bool
	IntentWeight float64
}

// DefaultRanking is find's.
var DefaultRanking = Ranking{Name: 3, Command: 2, Words: 2.5, About: 1, CommandAbout: 1,
	Stem: true, Synonyms: true, SynonymWeight: 0.5, Phrases: true, PhraseWeight: 1.5, Intent: true, IntentWeight: 0.15}

// BaselineRanking is plain BM25 over names and summaries, words as written:
// about what a client's own tool search does with one tool per operation. The
// eval reports it next to find's, so what the rest adds stays measured.
var BaselineRanking = Ranking{Name: 1, About: 1}

const (
	k1 = 1.2
	// Short fields (names, commands, words) hardly depend on length.
	bShort = 0.3
	bLong  = 0.75
)

type field struct {
	weight, b float64
	terms     [][]string // per entry: its words in order
	avgLen    float64
}

// Rank scores entries for a request and returns the best limit (limit <= 0:
// all that match), best first.
func Rank(es []Entry, request string, r Ranking, limit int) []Hit {
	fields := []*field{
		{weight: r.Name, b: bShort}, {weight: r.Command, b: bShort}, {weight: r.Words, b: bShort},
		{weight: r.About, b: bLong}, {weight: r.CommandAbout, b: bLong},
	}
	text := func(e Entry, i int) string {
		switch i {
		case 0:
			return e.Name
		case 1:
			return strings.TrimPrefix(e.Command, "metarush ")
		case 2:
			return strings.Join(e.Words, " · ")
		case 3:
			return e.About
		}
		return e.CommandAbout
	}
	for i, f := range fields {
		if f.weight == 0 {
			continue
		}
		f.terms = make([][]string, len(es))
		total := 0
		for j, e := range es {
			f.terms[j] = Terms(text(e, i), r.Stem)
			total += len(f.terms[j])
		}
		if len(es) > 0 {
			f.avgLen = math.Max(1, float64(total)/float64(len(es)))
		}
	}
	// Document frequency over the union of an entry's fields.
	df := map[string]int{}
	for j := range es {
		seen := map[string]bool{}
		for _, f := range fields {
			if f.terms == nil {
				continue
			}
			for _, t := range f.terms[j] {
				if !seen[t] {
					seen[t] = true
					df[t]++
				}
			}
		}
	}
	n := float64(len(es))
	idf := func(t string) float64 {
		d := float64(df[t])
		return math.Log(1 + (n-d+0.5)/(d+0.5))
	}

	words := Terms(request, r.Stem)
	query := map[string]float64{} // term → weight
	for _, w := range words {
		query[w] = 1
	}
	if r.Synonyms {
		for _, w := range words {
			for _, s := range synonymsOf(w, r.Stem) {
				if _, ok := query[s]; !ok {
					query[s] = r.SynonymWeight
				}
			}
		}
		for _, s := range phraseTerms(request, r.Stem) {
			if _, ok := query[s]; !ok {
				query[s] = r.SynonymWeight
			}
		}
	}
	var pairs []string
	if r.Phrases {
		for i := 0; i+1 < len(words); i++ {
			pairs = append(pairs, words[i]+" "+words[i+1])
		}
	}
	intent := 0
	if r.Intent {
		intent = intentOf(request)
		if intent < 0 && r.Synonyms {
			// A question about what there is lists or shows it.
			for _, s := range Terms("list show", r.Stem) {
				if _, ok := query[s]; !ok {
					query[s] = r.SynonymWeight / 2
				}
			}
		}
	}

	// Terms in a fixed order: the same request always sums the same way.
	terms := make([]string, 0, len(query))
	for t := range query {
		terms = append(terms, t)
	}
	sort.Strings(terms)
	var hits []Hit
	for j, e := range es {
		score := 0.0
		for _, t := range terms {
			qw := query[t]
			tf := 0.0
			for _, f := range fields {
				if f.terms == nil {
					continue
				}
				c := 0
				for _, x := range f.terms[j] {
					if x == t {
						c++
					}
				}
				if c > 0 {
					tf += f.weight * float64(c) / (1 - f.b + f.b*float64(len(f.terms[j]))/f.avgLen)
				}
			}
			if tf > 0 {
				score += qw * idf(t) * tf * (k1 + 1) / (tf + k1)
			}
		}
		if score == 0 {
			continue
		}
		for _, p := range pairs {
			// The best field the two words stand together in, by its weight.
			best := 0.0
			for _, f := range fields {
				if f.terms != nil && f.weight > best && hasPair(f.terms[j], p) {
					best = f.weight
				}
			}
			if best > 0 {
				a, b, _ := strings.Cut(p, " ")
				score += r.PhraseWeight * best / 3 * (idf(a) + idf(b)) / 2
			}
		}
		if intent != 0 {
			if (intent > 0) == e.Changes {
				score *= 1 + r.IntentWeight
			}
		}
		hits = append(hits, Hit{Entry: e, Score: math.Round(score*1000) / 1000})
	}
	byScore(hits)
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func hasPair(ts []string, p string) bool {
	for i := 0; i+1 < len(ts); i++ {
		if ts[i]+" "+ts[i+1] == p {
			return true
		}
	}
	return false
}

// Terms splits text into search words: lower case, letters and digits,
// camelCase and dotted names split ("removeWords" → remove words), stop words
// dropped, and with stem each word in its stem form.
func Terms(text string, stem bool) []string {
	var out []string
	for _, w := range splitWords(text) {
		w = strings.ToLower(w)
		if canonical, ok := spelling[w]; ok {
			w = canonical
		}
		if stopWords[w] || len(w) < 2 || allDigits(w) {
			continue // times and counts (0:12, 30 seconds) say nothing about what to use
		}
		if stem {
			w = Stem(w)
		}
		out = append(out, w)
	}
	return out
}

func allDigits(w string) bool {
	for _, r := range w {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// splitWords cuts at anything but letters and digits, and inside a word where
// a lower-case letter meets an upper-case one ("deleteRange").
func splitWords(text string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = cur[:0]
		}
	}
	rs := []rune(text)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\'' || c == '’':
			// "what's", "don't": the part after the apostrophe goes.
			flush()
			for i+1 < len(rs) && unicode.IsLetter(rs[i+1]) {
				i++
			}
		case unicode.IsLetter(c) || unicode.IsDigit(c):
			if unicode.IsUpper(c) && len(cur) > 0 && unicode.IsLower(cur[len(cur)-1]) {
				flush()
			}
			cur = append(cur, c)
		default:
			flush()
		}
	}
	flush()
	return out
}

// Stem is a light English stemmer: plurals, -ing, -ed and -ly, then a final
// e, so "renders", "rendering" and "rendered" are one word and "titles" and
// "title" another.
func Stem(w string) string {
	if s, ok := irregular[w]; ok {
		return s
	}
	if len(w) <= 3 {
		return w
	}
	switch {
	case strings.HasSuffix(w, "ies") && len(w) > 4:
		w = w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "sses"):
		w = w[:len(w)-2]
	case strings.HasSuffix(w, "xes") || strings.HasSuffix(w, "ches") || strings.HasSuffix(w, "shes") || strings.HasSuffix(w, "zes"):
		w = w[:len(w)-2]
	case strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is"):
		w = w[:len(w)-1]
	}
	switch {
	case strings.HasSuffix(w, "ing") && len(w) >= 6:
		w = undouble(w[:len(w)-3])
	case strings.HasSuffix(w, "ed") && len(w) >= 5 && !strings.HasSuffix(w, "eed"):
		w = undouble(w[:len(w)-2])
	case strings.HasSuffix(w, "ly") && len(w) >= 6:
		w = w[:len(w)-2]
	case strings.HasSuffix(w, "able") && len(w) >= 7:
		w = w[:len(w)-4] // editable → edit
	}
	if strings.HasSuffix(w, "e") && len(w) > 3 {
		w = w[:len(w)-1]
	}
	return w
}

// undouble: "trimm" → "trim", "stopp" → "stop" (but "fill", "pass" stay).
func undouble(w string) string {
	n := len(w)
	if n >= 3 && w[n-1] == w[n-2] && !strings.ContainsRune("aeioulsz", rune(w[n-1])) {
		return w[:n-1]
	}
	return w
}

// irregular forms a suffix rule gets wrong.
var irregular = map[string]string{
	"made": "mak", "said": "say", "says": "say", "saying": "say", "spoken": "speak", "spoke": "speak",
	"took": "tak", "taken": "tak", "done": "do", "doing": "do", "does": "do", "did": "do",
	"bought": "buy", "brought": "bring", "chosen": "choos", "chose": "choos", "seen": "see", "saw": "see",
	"written": "writ", "wrote": "writ", "writing": "writ", "using": "us", "used": "us", "uses": "us", "use": "us",
	"news": "new", "media": "media", "data": "data", "status": "status", "analysis": "analys", "analyses": "analys",
	"people": "people", "person": "person", "children": "child", "series": "series", "this": "this",
}

// spelling folds British and American spellings and a few written-together
// forms into one.
var spelling = map[string]string{
	"colour": "color", "colours": "colors", "analyse": "analyze", "analysed": "analyzed", "organise": "organize",
	"favourite": "favorite", "licence": "license", "centre": "center", "grey": "gray", "dialogue": "dialog",
	"voiceover": "voiceover", "voiceovers": "voiceover", "vo": "voiceover", "subtitles": "subtitle",
	"youtube": "youtube", "tiktok": "tiktok", "github": "github", "mp4": "mp4", "srt": "srt", "vtt": "vtt",
	"okay": "ok", "todo": "todos", "todos": "todos", "ums": "um", "uhs": "uh",
}

// stopWords carry no meaning for choosing. Words that do ("who", "where",
// "why", "again", "now", "not") stay.
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "of": true, "to": true, "in": true, "on": true, "at": true, "for": true, "from": true,
	"by": true, "with": true, "and": true, "or": true, "but": true, "is": true, "are": true, "was": true, "were": true,
	"be": true, "been": true, "being": true, "it": true, "its": true, "this": true, "that": true, "these": true, "those": true,
	"my": true, "me": true, "we": true, "our": true, "us": true, "you": true, "your": true, "i": true, "can": true, "could": true,
	"would": true, "should": true, "will": true, "please": true, "what": true, "which": true, "how": true, "do": true,
	"does": true, "did": true, "have": true, "has": true, "had": true, "get": true, "got": true, "let": true, "just": true,
	"so": true, "some": true, "any": true, "there": true, "here": true, "then": true, "than": true, "into": true,
	"about": true, "as": true, "if": true, "else": true, "also": true, "very": true, "too": true, "one": true,
	"want": true, "need": true, "like": true, "im": true, "ive": true, "id": true, "s": true, "t": true, "ll": true,
	"ve": true, "re": true, "d": true, "m": true, "eg": true, "etc": true, "they": true, "them": true,
	"their": true, "he": true, "she": true, "his": true, "her": true, "him": true, "all": true, "each": true,
	"every": true, "only": true, "own": true, "same": true, "such": true, "other": true, "another": true, "more": true,
	"most": true, "much": true, "many": true, "metarush": true, "x": true,
	// Which one of several ("the second clip") says nothing about what to use.
	"first": true, "second": true, "third": true, "fourth": true, "fifth": true, "seconds": true,
}

// read and change verbs a request starts with (or has after "can you").
var lookWords = map[string]bool{"show": true, "list": true, "which": true, "what": true, "whats": true, "who": true, "where": true,
	"is": true, "are": true, "does": true, "how": true, "read": true, "tell": true, "find": true, "search": true, "check": true,
	"compare": true, "look": true, "see": true, "when": true, "why": true, "give": true, "print": true, "get": true, "preview": true}
var changeWords = map[string]bool{"add": true, "remove": true, "delete": true, "make": true, "create": true, "change": true,
	"set": true, "turn": true, "fix": true, "rename": true, "put": true, "use": true, "move": true, "cut": true, "trim": true,
	"split": true, "save": true, "apply": true, "start": true, "stop": true, "cancel": true, "retry": true, "approve": true,
	"generate": true, "render": true, "import": true, "bring": true, "upload": true, "record": true, "export": true,
	"empty": true, "archive": true, "restore": true, "merge": true, "combine": true, "name": true, "answer": true,
	"skip": true, "update": true, "invite": true, "extend": true, "replace": true, "retype": true, "undo": true,
	"take": true, "drop": true, "clean": true, "free": true, "download": true, "pair": true, "line": true, "always": true,
	"leave": true, "queue": true, "ask": true, "dry": true, "grab": true, "open": true, "send": true, "deliver": true}

// intentOf: +1 when the request asks for a change, -1 when it asks to look,
// 0 when it can't tell. "Can you add …" asks for a change; "Can people …?",
// like any other question that doesn't start with a change, asks to look.
func intentOf(request string) int {
	ws := splitWords(strings.ToLower(request))
	polite := map[string]bool{"please": true, "you": true, "i": true, "want": true, "need": true, "to": true, "we": true,
		"lets": true, "let": true, "us": true, "now": true, "also": true, "just": true, "then": true, "ok": true, "okay": true}
	for len(ws) > 1 && (ws[0] == "can" || ws[0] == "could" || ws[0] == "would") && ws[1] == "you" {
		ws = ws[2:]
	}
	for len(ws) > 0 && polite[ws[0]] {
		ws = ws[1:]
	}
	if len(ws) == 0 {
		return 0
	}
	switch {
	case changeWords[ws[0]]:
		return 1
	case lookWords[ws[0]] || strings.HasSuffix(strings.TrimSpace(request), "?"):
		return -1
	}
	return 0
}
