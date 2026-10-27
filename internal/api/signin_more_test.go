package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Sign-in tries are limited per address (D183): a few a minute, each address
// on its own, allowed again a minute later; addresses not seen for a while are
// forgotten.
func TestSignInTriesAreLimitedPerAddress(t *testing.T) {
	l := newRateLimit(3)
	now := time.Now()
	for i := 0; i < 3; i++ {
		if !l.allow("100.64.0.7", now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("try %d refused", i+1)
		}
	}
	if l.allow("100.64.0.7", now.Add(5*time.Second)) {
		t.Fatal("a fourth try within the minute")
	}
	if !l.allow("100.64.0.8", now.Add(5*time.Second)) {
		t.Fatal("another address is limited by the first")
	}
	if !l.allow("100.64.0.7", now.Add(70*time.Second)) {
		t.Fatal("not allowed again a minute later")
	}
	// Ten minutes on: the old addresses are swept out when the next try comes.
	l.allow("100.64.0.9", now.Add(10*time.Minute))
	if _, kept := l.seen["100.64.0.8"]; kept || len(l.seen) != 1 {
		t.Fatalf("addresses kept: %v", l.seen)
	}
}

// Where a sign-in comes from: the tailnet address tailscale serve forwarded,
// else the connection's own address.
func TestSignInSource(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	r.RemoteAddr = "127.0.0.1:50123"
	if got := sourceOf(r); got != "127.0.0.1" {
		t.Fatalf("local: %q", got)
	}
	r.RemoteAddr = "[::1]:50123"
	if got := sourceOf(r); got != "[::1]" {
		t.Fatalf("local IPv6: %q", got)
	}
	r = r.WithContext(context.WithValue(r.Context(), accessKey{}, access{public: true, from: "100.101.102.103"}))
	if got := sourceOf(r); got != "100.101.102.103" {
		t.Fatalf("over the tailnet: %q", got)
	}
	// A request body with fields MetaRush doesn't know is refused plainly.
	bad := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"a@example.com","password":"x","admin":true}`))
	var in SignInRequest
	if err := readJSON(bad, &in); err == nil || !strings.Contains(err.Error(), "couldn't read the request") {
		t.Fatalf("unknown fields: %v", err)
	}
}
