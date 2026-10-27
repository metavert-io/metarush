package auth

import (
	"strings"
	"testing"
)

func TestPasswordsAreHashedAndChecked(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil || !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("hash %q %v", h, err)
	}
	h2, _ := HashPassword("correct horse battery staple")
	if h == h2 {
		t.Fatal("two hashes of one password share a salt")
	}
	if ok, rehash := VerifyPassword("correct horse battery staple", h); !ok || rehash {
		t.Fatalf("verify: %v %v", ok, rehash)
	}
	if ok, _ := VerifyPassword("correct horse battery stapl", h); ok {
		t.Fatal("a wrong password matched")
	}
	// The same password typed with a composed or decomposed accent matches (NFKC).
	h3, _ := HashPassword("café au lait every morning")
	if ok, _ := VerifyPassword("café au lait every morning", h3); !ok {
		t.Fatal("normalization")
	}
	// Older settings are replaced at the next sign-in.
	weak := "$argon2id$v=19$m=19456,t=2,p=1$" + strings.Split(h, "$")[4] + "$" + strings.Split(h, "$")[5]
	if ok, _ := VerifyPassword("correct horse battery staple", weak); ok {
		t.Fatal("different settings gave the same key")
	}
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$a$b", "$argon2id$v=19$m=999999999,t=1,p=1$YQ$YQ"} {
		if ok, _ := VerifyPassword("x", bad); ok {
			t.Fatalf("malformed hash accepted: %q", bad)
		}
	}
}

func TestPasswordRules(t *testing.T) {
	for pw, want := range map[string]string{
		"short":                        "at least 12",
		"1qaz2wsx3edc":                 "most common",
		"qzqzqzwqzqzqzwqz":             "variety",
		"theohartley-was-here!":        "email",
		"Maya loves gardens 2026":      "name",
		strings.Repeat("abcdefgh", 17): "at most 128",
		"tomato garden at dusk":        "",
		"🎬 cut the opening at 0:03 🎬":  "",
	} {
		err := CheckPassword(pw, "theohartley@example.com", "Maya Lindqvist")
		switch {
		case want == "" && err != nil:
			t.Errorf("%q refused: %v", pw, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%q: %v, want %q", pw, err, want)
		}
	}
	if !IsWeak(CheckPassword("short", "", "")) {
		t.Fatal("IsWeak")
	}
	if EmailKey("  Theo@Example.COM ") != "theo@example.com" || !ValidEmail("theo@example.com") || ValidEmail("theo@") || ValidEmail("not an email") {
		t.Fatal("emails")
	}
}
