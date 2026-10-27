package faces

import (
	"regexp"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/models"
)

// Names people say (D156): "Hi, I'm Iris" names the voice that says it;
// "Thanks, Lena" names the voice spoken to. They are evidence for a
// question, never a name by themselves.

// SaidName is a name said in a clip, and whose voice it is about.
type SaidName struct {
	Name  string  // as said ("Iris")
	Label string  // the voice it names (speaker_01)
	Self  bool    // the voice said it about itself ("I'm Iris")
	Quote string  // the words ("Hi, I'm Iris")
	At    float64 // when (source seconds)
}

const nameRE = `(\p{Lu}[\p{L}\p{M}'’\-]+(?:\s+\p{Lu}[\p{L}\p{M}'’\-]+)?)`

var (
	// "I'm Ada", "I am Ada Park", "my name is Ada", "my name's Ada" — anywhere in a turn.
	selfAnywhere = regexp.MustCompile(`(?:\b(?:I'm|I’m|I am)|\b[Mm]y name is|\b[Mm]y name's|\b[Mm]y name’s)\s+` + nameRE)
	// "This is Ada", "It's Ada", "Ada here" — only where a turn starts (after a greeting at most).
	selfOpening = regexp.MustCompile(`^(?:(?:[Hh]i|[Hh]ello|[Hh]ey|[Gg]ood (?:morning|afternoon|evening))(?:\s+(?:everyone|everybody|all|there|guys|folks))?[,.!]?\s+)?(?:[Tt]his is|[Ii]t's|[Ii]t’s)\s+` + nameRE)
	selfHere    = regexp.MustCompile(`^(?:(?:[Hh]i|[Hh]ello|[Hh]ey)[,.!]?\s+)?` + nameRE + `\s+here\b`)
	// "Thanks, Lena", "Thank you Lena", "Hi Lena", "Welcome, Lena" — the voice spoken to.
	addressed = regexp.MustCompile(`(?:^|[.!?]\s+|,\s+)(?:[Tt]hanks(?: so much)?|[Tt]hank you(?: so much)?|[Hh]i|[Hh]ey|[Hh]ello|[Ww]elcome(?: back)?|[Gg]ood to see you|[Gg]reat to see you|[Nn]ice to meet you|[Bb]ye|[Gg]oodbye|[Ss]ee you)[,!]?\s+` + nameRE + `\b`)
)

// notSaidNames are capitalized words after "I'm" or "thanks" that are not
// names. Pronouns, contractions and fillers are never names (D189): a
// transcript's "I'm I'm going to share" read "I'm" as a name, and "You're
// here" read "You're" as one.
var notSaidNames = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`everyone everybody all guys folks there team you sir madam ma'am god jesus lord mom dad mum
		american canadian british english irish scottish welsh australian french german italian spanish mexican chinese japanese korean indian
		african european asian jewish catholic christian muslim hindu buddhist protestant okay ok sorry here good fine back so not just really
		monday tuesday wednesday thursday friday saturday sunday january february march april may june july august september october november december
		zoom teams google apple microsoft
		i i'm im i've i'll i'd you you're youre you've you'll you'd we we're we've we'll we'd they they're they've they'll they'd
		he he's he'll he'd she she's she'll she'd it it's its me my mine us our them their this that that's these those
		here's there's where's what's who's let's who what when where why how which yes yeah yep yup no nope um uh uhm umm hmm mm oh ah
		like well hey hi hello right sure also still very pretty kind kinda sort totally actually basically literally going gonna trying
		glad happy excited thrilled honored honoured delighted great ready done new one`) {
		notSaidNames[w] = true
	}
}

// notName says whether a word can't be a name (case and curly apostrophes ignored).
func notName(w string) bool {
	return notSaidNames[strings.ToLower(strings.ReplaceAll(w, "’", "'"))]
}

// cleanSaid trims a said name to the words that are a name ("Ada Park" stays,
// "Ada So" becomes "Ada"); "" when it isn't one.
func cleanSaid(s string) string {
	words := strings.Fields(s)
	var keep []string
	for _, w := range words {
		w = strings.Trim(w, "'’-")
		if w == "" || notName(w) {
			break
		}
		keep = append(keep, w)
	}
	if len(keep) == 0 || len([]rune(keep[0])) < 2 {
		return ""
	}
	return strings.Join(keep, " ")
}

// SaidNames finds the names said in a clip's transcript and the voice each one
// names. voices are the clip's voices (labels of brief voices are left out of
// "the other voice").
func SaidNames(segs []models.TranscriptSegment, voices []models.AssetSpeaker) []SaidName {
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].From < segs[j].From })
	var labels []string
	seen := map[string]bool{}
	for _, v := range voices {
		if v.MergedInto != "" || v.Minor || seen[v.Label] {
			continue
		}
		seen[v.Label] = true
		labels = append(labels, v.Label)
	}
	other := func(label string) string {
		if len(labels) != 2 {
			return ""
		}
		if labels[0] == label {
			return labels[1]
		}
		if labels[1] == label {
			return labels[0]
		}
		return ""
	}
	previous := func(i int) string {
		for k := i - 1; k >= 0; k-- {
			if segs[k].Speaker != "" && segs[k].Speaker != segs[i].Speaker {
				if segs[i].From-segs[k].To > 5 {
					return ""
				}
				return segs[k].Speaker
			}
		}
		return ""
	}
	var out []SaidName
	add := func(n SaidName) {
		if n.Name == "" || n.Label == "" {
			return
		}
		for _, o := range out {
			if o.Label == n.Label && strings.EqualFold(o.Name, n.Name) && o.Self == n.Self {
				return
			}
		}
		out = append(out, n)
	}
	for i, s := range segs {
		if s.Speaker == "" {
			continue
		}
		text := strings.TrimSpace(s.Text)
		quote := func(lo, hi int) string {
			q := strings.TrimLeft(strings.TrimSpace(text[lo:hi]), ".,!? ")
			if len([]rune(q)) > 80 {
				q = string([]rune(q)[:80]) + "…"
			}
			return q
		}
		for _, re := range []*regexp.Regexp{selfAnywhere, selfOpening, selfHere} {
			for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
				add(SaidName{Name: cleanSaid(text[m[2]:m[3]]), Label: s.Speaker, Self: true, Quote: quote(m[0], m[1]), At: s.From})
			}
		}
		for _, m := range addressed.FindAllStringSubmatchIndex(text, -1) {
			name := cleanSaid(text[m[2]:m[3]])
			if name == "" {
				continue
			}
			to := other(s.Speaker)
			if to == "" && strings.TrimSpace(text[:m[0]]) == "" {
				// With three or more voices, only "Thanks, Lena" that opens a turn is
				// clear enough: it answers whoever just spoke.
				to = previous(i)
			}
			add(SaidName{Name: name, Label: to, Quote: quote(m[0], m[1]), At: s.From})
		}
	}
	return out
}
