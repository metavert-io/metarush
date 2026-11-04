package emailkey

import (
	"regexp"
	"testing"
)

// Only ASCII letters fold: a character that Unicode's lower case maps onto an
// ASCII letter (the Kelvin sign U+212A, the long s U+017F, the capital I with
// a dot U+0130) keeps its own key, so an address written with it never
// matches someone else's.
func TestOnlyASCIILettersFold(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"  Kate@Client.Example ", "kate@client.example"},
		{"\u212Aate@client.example", "\u212Aate@client.example"}, // the Kelvin sign stays itself
		{"Émile@Studio.example", "Émile@studio.example"},         // other letters as written
		{"\u017Fam@client.example", "\u017Fam@client.example"},   // the long s
		{"\u0130van@client.example", "\u0130van@client.example"}, // capital I with a dot
	} {
		if got := Of(c.in); got != c.want {
			t.Errorf("Of(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if Of("\u212Aate@client.example") == Of("kate@client.example") {
		t.Fatal("the Kelvin sign matched k")
	}
}

// Pattern matches the emails with that key and nothing else: either case of
// an ASCII letter, every other character as written, specials literal.
func TestPatternMatchesExactlyTheKey(t *testing.T) {
	re := regexp.MustCompile(Pattern(Of("Kate.O+tag@Client.example")))
	for _, s := range []string{"kate.o+tag@client.example", "KATE.O+TAG@CLIENT.EXAMPLE", "Kate.O+tag@Client.example"} {
		if !re.MatchString(s) {
			t.Errorf("%q should match", s)
		}
	}
	for _, s := range []string{"\u212Aate.o+tag@client.example", "katexo+tag@client.example", "kate.oo+tag@client.example", "xkate.o+tag@client.example", "kate.o+tag@client.example!"} {
		if re.MatchString(s) {
			t.Errorf("%q shouldn't match", s)
		}
	}
	if re := regexp.MustCompile(Pattern(Of("Émile@x.example"))); re.MatchString("émile@x.example") || !re.MatchString("Émile@X.example") {
		t.Fatal("non-ASCII letters must match as written")
	}
}
