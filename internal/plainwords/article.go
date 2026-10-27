package plainwords

// AOrAn is the article before a number as it is said aloud: "an" before
// eight, eleven, eighteen and the eighties ("an 8-second shot", "an 11-…",
// "an 18-…", "an 80-…"), also when they start a larger number ("an 800-…",
// "an 8000-…", "an 11000-…"), and "a" before every other number ("a 6-second
// shot", "a 100-…", "a 1000-…"). Every place that writes "a N-…" uses it, and
// the dashboard has the same rule (aOrAn in web/src/lib/format.ts); both are
// tested against testdata/articles.json.
func AOrAn(n int) string {
	for n >= 1000 {
		n /= 1000
	}
	switch {
	case n == 8, n == 11, n == 18, n >= 80 && n <= 89, n >= 800 && n <= 899:
		return "an"
	}
	return "a"
}
