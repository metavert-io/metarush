package auth

import (
	"testing"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/sso"
)

// A setup is checked before anything is kept (D425, D429): Google's and
// Microsoft's addresses are fixed by the provider, another provider's must be
// https (a stand-in on this computer in tests), and who may join is a domain
// for Google and others, a directory for Microsoft.
func TestSetupsAreCheckedBeforeTheyAreKept(t *testing.T) {
	dir := "11111111-2222-3333-4444-555555555555"
	ok := []models.SSOSettings{
		{Provider: "google", ClientID: "1234.apps.example", Domain: "Example.com", Issuer: "https://evil.example.net"},
		{Provider: "Microsoft", ClientID: "app", Tenant: dir, Domain: dir},
		{Provider: "microsoft", ClientID: "app", Tenant: "organizations"},
		{Provider: "oidc", ClientID: "metarush", Issuer: "http://127.0.0.1:5791", Label: "Test provider", Domain: "example.org"},
	}
	for _, in := range ok {
		st, err := CleanSSO(in)
		if err != nil {
			t.Fatalf("%+v: %v", in, err)
		}
		switch st.Provider {
		case sso.Google:
			if st.Issuer != sso.GoogleIssuer || st.Domain != "example.com" {
				t.Errorf("google: %+v", st)
			}
		case sso.Microsoft:
			if st.Issuer != sso.MicrosoftIssuer(st.Tenant) {
				t.Errorf("microsoft: %+v", st)
			}
		}
	}
	bad := []models.SSOSettings{
		{Provider: "google"},                                    // no client ID
		{Provider: "google", ClientID: "a b"},                   // spaces
		{Provider: "google", ClientID: "x", Domain: "@example"}, // not a domain
		{Provider: "microsoft", ClientID: "x"},                  // no directory
		{Provider: "microsoft", ClientID: "x", Tenant: "not a tenant!"},
		{Provider: "microsoft", ClientID: "x", Tenant: dir, Domain: "example.com"},                          // a directory joins, not a domain
		{Provider: "microsoft", ClientID: "x", Tenant: dir, Domain: "99999999-2222-3333-4444-555555555555"}, // another directory can't sign in at all
		{Provider: "microsoft", ClientID: "x", Tenant: "common"},                                            // personal accounts too (D452)
		{Provider: "microsoft", ClientID: "x", Tenant: "Consumers"},                                         // personal accounts only
		{Provider: "microsoft", ClientID: "x", Tenant: sso.MicrosoftPersonal},                               // the personal accounts' directory
		{Provider: "oidc", ClientID: "x", Issuer: "http://login.example.com"},                               // http off this computer
		{Provider: "oidc", ClientID: "x", Issuer: "https://login.example.com"},                              // a real provider under go test
		{Provider: "oidc", ClientID: "x"},
		{Provider: "saml", ClientID: "x"},
		{Provider: "oidc", ClientID: "x", Issuer: "http://127.0.0.1:5791", Label: "a name far too long for any sign-in button anywhere"},
	}
	for _, in := range bad {
		if st, err := CleanSSO(in); err == nil {
			t.Errorf("%+v kept as %+v", in, st)
		}
	}
	// The same sign-in: provider, address and app; who may join may differ.
	a := &models.SSOSettings{Provider: "google", Issuer: sso.GoogleIssuer, ClientID: "x", Domain: "a.example"}
	b := &models.SSOSettings{Provider: "google", Issuer: sso.GoogleIssuer, ClientID: "x", AlsoPassword: true}
	c := &models.SSOSettings{Provider: "google", Issuer: sso.GoogleIssuer, ClientID: "y"}
	if !sameSignIn(a, b) || sameSignIn(a, c) || sameSignIn(nil, a) {
		t.Error("sameSignIn")
	}
}
