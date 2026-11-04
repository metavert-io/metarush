package logbook

import (
	"strings"
	"testing"
)

// Organization sign-ins (D428): a begin address's ticket, the provider's
// answer at the callback and the PKCE and nonce values never reach the log,
// even when MetaRush didn't see the exact value first.
// madeUpToken is a token's shape, put together at run time (nothing here signs anything).
var madeUpToken = strings.Join([]string{"eyJhbGciOiJSUzI1NiJ9", "eyJzdWIiOiJ4In0", "c2lnbmF0dXJlLXNpZw"}, ".")

func TestOrganizationSignInsAreRedacted(t *testing.T) {
	r := NewRedactor()
	for _, tc := range []struct{ in, secret string }{
		{"opened http://localhost:4845/auth/sso/begin?t=AbCdEf0123456789xyzAbCdEf0123456789xyz", "AbCdEf0123456789xyz"},
		{"GET https://studio.example.ts.net/auth/sso/callback?code=4/0AbCdEfGhIj&state=ZyXwVuTs0123456789abc&scope=openid", "4/0AbCdEfGhIj"},
		{"GET https://studio.example.ts.net/auth/sso/callback?code=4/0AbCdEfGhIj&state=ZyXwVuTs0123456789abc", "ZyXwVuTs0123456789abc"},
		{"authorize?response_type=code&nonce=n0nc3V4lu3abcdefgh&code_challenge=Ch4ll3ng3abcdefgh", "n0nc3V4lu3abcdefgh"},
		{"token form: code_verifier=V3r1f13rabcdefghijklmnop", "V3r1f13rabcdefghijklmnop"},
		{"id_token=" + madeUpToken, strings.Split(madeUpToken, ".")[1]},
	} {
		got := r.String(tc.in)
		if strings.Contains(got, tc.secret) {
			t.Errorf("%q → %q", tc.in, got)
		}
	}
	// An ordinary link keeps its words.
	if got := r.String("see https://github.com/x/y/issues?state=open"); !strings.Contains(got, "state=open") {
		t.Errorf("an ordinary link lost its words: %q", got)
	}
}
