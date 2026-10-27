package ops

import (
	"strings"
	"testing"
)

// A translated line is matched to the version's line with exactly its
// words first, and only then to one that differs in case (D238).
func TestTranslatedLinesMatchExactlyFirst(t *testing.T) {
	lines := []captionLine{{}, {}}
	lines[0].Text, lines[1].Text = "We met in May.", "We met in may."
	exact, folded := map[string]int{}, map[string]int{}
	for i, l := range lines {
		if _, ok := exact[l.Text]; !ok {
			exact[l.Text] = i
		}
		if _, ok := folded[normFold(l.Text)]; !ok {
			folded[normFold(l.Text)] = i
		}
	}
	if i := matchLine(lines, exact, folded, TranslatedLine{Source: "We met in may.", Text: "x"}); i != 1 {
		t.Fatalf("matched line %d, want the exact one (1)", i)
	}
	if i := matchLine(lines, exact, folded, TranslatedLine{Source: "WE MET IN MAY.", Text: "x"}); i != 0 {
		t.Fatalf("case-folded: line %d", i)
	}
	if i := matchLine(lines, exact, folded, TranslatedLine{N: 2, Text: "x"}); i != 1 {
		t.Fatalf("by number: line %d", i)
	}
	got := MatchTranslation([]CaptionLineOut{{N: 1, Source: "We met in May."}, {N: 2, Source: "We met in may."}}, []TranslatedLine{{Source: "We met in may."}})
	if got[0] == nil || got[0].N != 2 {
		t.Fatalf("the card matches exactly first too: %+v", got[0])
	}
}

func normFold(s string) string { return strings.ToLower(normLine(s)) }

// A transcript fix is carried into a translation only when that is certain:
// a name, a number or a spelling-list word that appears in the translation
// exactly once as whole words; common words and punctuation need
// translating again (D238).
func TestFixesFollowIntoTranslationsOnlyWhenCertain(t *testing.T) {
	glossary := map[string]bool{"elmwood": true}
	cases := []struct {
		was, now, text, want string
		ok                   bool
	}{
		{"We met Fern Brook at Elmwood.", "We met FernBrook at Elmwood.", "Conocimos a Fern Brook en Elmwood.", "Conocimos a FernBrook en Elmwood.", true},
		{"We saw Ann", "We saw Anne", "Annabel vio a Ann", "Annabel vio a Anne", true},
		{"It rained. We saw Ann.", "It rained. We saw Anne.", "Llovió. Vimos a Ann.", "Llovió. Vimos a Anne.", true},
		{"3 dogs ran", "4 dogs ran", "3 perros corrieron", "4 perros corrieron", true},
		{"Ask McDonald", "Ask MacDonald", "Pregunta a McDonald", "Pregunta a MacDonald", true},
		{"the elmwood garden", "the Elmwood garden", "el jardín elmwood", "el jardín Elmwood", true},
		{"version 3 is out", "version 4 is out", "la versión 3 salió", "la versión 4 salió", true},
		{"I saw a dame.", "I saw the dame.", "Vi a la Dame.", "", false},                       // a common word, and "a" inside "Dame"
		{"Meet in Berlin", "Meet on Berlin", "Treffen in Berlin", "", false},                   // German "in" isn't the English one
		{"Hello there", "Hello there.", "Hola", "", false},                                     // punctuation only
		{"Ask Fern Brook", "Ask FernBrook", "Pregunta a Fern Brook y a Fern Brook", "", false}, // twice: not certain which
		{"Ask Brook", "Ask Brooke", "Pregunta a Brooker", "", false},                           // only inside another word
		{"We met Fern Brook", "We met Fern Brook today", "Conocimos a Fern Brook", "", false},  // words added: translate again
		// The first word of a line or a sentence has a capital anyway: a common word, not a name.
		{"In the box.", "On the box.", "In der Box.", "", false},
		{"A dog barked", "The dog barked", "A mi perro", "", false},
		{"Was it good?", "Is it good?", "Was es gut?", "", false},
		{"It rained. In the box.", "It rained. On the box.", "Es regnete. In der Box.", "", false},
		{"He asked “why?” A dog came", "He asked “why?” The dog came", "Preguntó «¿por qué?» A mi perro", "", false},
		{"so I went", "so we went", "also I went", "", false},                 // the pronoun "I"
		{"Ann said hello", "Anne said hello", "Annabel oyó a Ann", "", false}, // a line's first word: not certain it is a name
	}
	for _, c := range cases {
		got, ok := followFix(c.was, c.now, c.text, glossary)
		if ok != c.ok || got != c.want {
			t.Errorf("%q → %q in %q: got %q %v, want %q %v", c.was, c.now, c.text, got, ok, c.want, c.ok)
		}
	}
}
