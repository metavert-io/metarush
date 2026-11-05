package emailkey

import (
	"regexp"
	"testing"
	"unicode"
)

// Emails fold to Unicode's lower case, so Émile@… is émile@…, except the two
// characters whose lower case is an ASCII letter: the Kelvin sign U+212A
// (k) and the capital I with a dot U+0130 (i) keep their own key, so an
// address written with one never matches someone else's. The long s U+017F
// is already lower case and stays itself.
func TestEmailsFoldExceptIntoASCII(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"  Kate@Client.Example ", "kate@client.example"},
		{"Émile@Studio.example", "émile@studio.example"},
		{"ÉMILE@STUDIO.EXAMPLE", "émile@studio.example"},
		{"Ännie@Client.example", "ännie@client.example"},
		{"ΣΩΚΡΆΤΗΣ@example.gr", "σωκράτησ@example.gr"},
		{"Kate@client.example", "Kate@client.example"}, // the Kelvin sign stays itself
		{"İvan@client.example", "İvan@client.example"}, // so does the capital I with a dot
		{"ſam@client.example", "ſam@client.example"},   // the long s is lower case already
	} {
		if got := Of(c.in); got != c.want {
			t.Errorf("Of(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, lookAlike := range []string{"Kate@client.example", "İvan@client.example", "ſam@client.example"} {
		plain := map[rune]string{'K': "kate@client.example", 'İ': "ivan@client.example", 'ſ': "sam@client.example"}[[]rune(lookAlike)[0]]
		if Of(lookAlike) == Of(plain) {
			t.Errorf("%q matched %q", lookAlike, plain)
		}
	}
	// No other character's key is an ASCII letter.
	for r := rune(0x80); r <= unicode.MaxRune; r++ {
		if k := []rune(Of(string(r))); len(k) == 1 && k[0] < 0x80 && k[0] != unicode.ReplacementChar && unicode.IsLetter(k[0]) {
			t.Fatalf("U+%04X folds onto %q", r, k[0])
		}
	}
}

// Pattern matches the emails with that key and nothing else: each letter in
// any case that folds to it, everything else literal.
func TestPatternMatchesExactlyTheKey(t *testing.T) {
	re := regexp.MustCompile(Pattern(Of("Kate.O+tag@Client.example")))
	for _, s := range []string{"kate.o+tag@client.example", "KATE.O+TAG@CLIENT.EXAMPLE", "Kate.O+tag@Client.example"} {
		if !re.MatchString(s) {
			t.Errorf("%q should match", s)
		}
	}
	for _, s := range []string{"Kate.o+tag@client.example", "katexo+tag@client.example", "kate.oo+tag@client.example", "xkate.o+tag@client.example", "kate.o+tag@client.example!"} {
		if re.MatchString(s) {
			t.Errorf("%q shouldn't match", s)
		}
	}
	re = regexp.MustCompile(Pattern(Of("Émile@x.example")))
	for _, s := range []string{"émile@x.example", "Émile@X.example", "ÉMILE@X.EXAMPLE"} {
		if !re.MatchString(s) {
			t.Errorf("%q should match", s)
		}
	}
	if re.MatchString("emile@x.example") {
		t.Error("é matched e")
	}
	// The Kelvin sign and the long s match only themselves; s and S never the long s.
	if re := regexp.MustCompile(Pattern(Of("Kate@x.example"))); !re.MatchString("Kate@x.example") || re.MatchString("kate@x.example") || re.MatchString("Kate@x.example") {
		t.Error("the Kelvin sign's key")
	}
	if re := regexp.MustCompile(Pattern(Of("Sam@x.example"))); !re.MatchString("sam@x.example") || !re.MatchString("SAM@x.example") || re.MatchString("ſam@x.example") {
		t.Error("s and the long s")
	}
	if re := regexp.MustCompile(Pattern(Of("ivan@x.example"))); re.MatchString("İvan@x.example") || !re.MatchString("IVAN@x.example") {
		t.Error("i and the capital I with a dot")
	}
}
