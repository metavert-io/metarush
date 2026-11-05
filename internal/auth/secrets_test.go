package auth

import (
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/sso"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// An app's client secret is taken for a secret — hidden in everything this
// process logs, left out of exports — once this computer keeps it. One it
// refuses (a person's words pasted by mistake) is no one's secret: before, it
// was taken for one before its shape was checked, and blanked those words out
// of the log and of every export.
func TestOnlyAKeptClientSecretIsRemembered(t *testing.T) {
	keyring.MockInit()
	a := testapp.New(t)
	st := &models.SSOSettings{Provider: sso.OIDC, Issuer: "https://login.example.com", ClientID: "app-remembered"}
	const words = "Interview with Sasha Okafor"
	if err := setNextSecret(a.Cfg, st, words); err == nil {
		t.Fatal("words with spaces were kept as a client secret")
	}
	if logbook.Remembered(words) {
		t.Fatal("a refused client secret was taken for a secret")
	}
	secret := sso.Random(24)
	if err := setNextSecret(a.Cfg, st, secret); err != nil {
		t.Fatal(err)
	}
	if !logbook.Remembered(secret) {
		t.Fatal("a kept client secret isn't hidden")
	}
}
