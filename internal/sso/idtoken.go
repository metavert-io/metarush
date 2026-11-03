package sso

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
)

// The ID token is checked as OpenID Connect Core 3.1.3.7 asks, with nothing
// left optional (D427): the signature by one of the provider's published keys
// in an algorithm the provider says it uses; iss exactly the provider's
// (Google's two spellings; Microsoft's {tenantid} filled in with the token's
// own tid); aud exactly this app (several audiences only with azp naming it,
// and azp, when given, naming it); exp in the future and iat in the past
// within Skew, iat not older than MaxAge, nbf within Skew; the nonce this
// sign-in sent; a subject.

// Skew is how far the provider's clock and this computer's may disagree.
const Skew = time.Minute

// MaxAge is the oldest a fresh ID token may be: one just issued for this
// sign-in, never one kept somewhere.
const MaxAge = 10 * time.Minute

// Claims are what MetaRush reads from an ID token.
type Claims struct {
	Issuer        string      `json:"iss"`
	Subject       string      `json:"sub"`
	Audience      audience    `json:"aud"`
	AZP           string      `json:"azp"`
	Expires       numericDate `json:"exp"`
	IssuedAt      numericDate `json:"iat"`
	NotBefore     numericDate `json:"nbf"`
	Nonce         string      `json:"nonce"`
	Email         string      `json:"email"`
	EmailVerified flexBool    `json:"email_verified"`
	Name          string      `json:"name"`
	Preferred     string      `json:"preferred_username"`
	// Google: the account's Google Workspace domain (absent for personal accounts).
	HD string `json:"hd"`
	// Microsoft: the directory (tenant) and the person's ID in it.
	TID string `json:"tid"`
	OID string `json:"oid"`
	// Microsoft: the email's domain is one its directory has verified it owns
	// (an optional claim the app asks for).
	EmailDomainVerified flexBool `json:"xms_edov"`
}

// audience is aud: one string or a list.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

// numericDate is seconds since 1970 (a whole or decimal number); 0 = absent.
type numericDate int64

func (d *numericDate) UnmarshalJSON(b []byte) error {
	var f float64
	if err := json.Unmarshal(b, &f); err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 1e12 {
		return errors.New("not a time")
	}
	*d = numericDate(int64(f))
	return nil
}

func (d numericDate) Time() time.Time { return time.Unix(int64(d), 0) }

// flexBool is true, or "true" (some providers send it as text).
type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	var v bool
	if json.Unmarshal(b, &v) == nil {
		*f = flexBool(v)
		return nil
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		*f = flexBool(strings.EqualFold(s, "true"))
		return nil
	}
	*f = false
	return nil
}

// Identity is who signed in, as MetaRush keeps it: the provider's account
// and the email it vouches for.
type Identity struct {
	Provider string `json:"provider"` // google | microsoft | oidc
	Issuer   string `json:"issuer"`   // the token's iss
	Subject  string `json:"subject"`
	// Key is the stable account this is, hashed (Key on the person's record):
	// Google's sub, Microsoft's directory and object ID, else issuer and sub.
	Key string `json:"key"`
	// Email is the address the provider gave. EmailTrusted: the provider is
	// the authority for it, so people are found by it (D429). EmailRefuted:
	// the provider says it hasn't checked it.
	Email        string `json:"email,omitempty"`
	EmailTrusted bool   `json:"emailTrusted"`
	EmailRefuted bool   `json:"emailRefuted,omitempty"`
	Name         string `json:"name,omitempty"`
	// Domain is what an allowed domain is checked against: Google's hd,
	// Microsoft's tid, or the verified email's domain.
	Domain string `json:"domain,omitempty"`
	// Why says, when the email doesn't count, why not.
	Why string `json:"why,omitempty"`
}

// Token problems, in plain words; the reason codes are stable.
func badToken(reason, detail string) *Problem {
	return &Problem{Reason: reason, Msg: "The sign-in provider's answer didn't pass MetaRush's checks, so nobody was signed in.", Hint: "Sign in again; if it happens again, an owner checks the setup in Settings → People.", Detail: detail}
}

// Verify checks a raw ID token against the provider's keys and this
// sign-in's nonce, and says who it is.
func (c *Client) Verify(ctx context.Context, d *Discovery, raw, nonce string) (*Identity, error) {
	t, err := parseToken(raw)
	if err != nil {
		return nil, badToken("bad_token", err.Error())
	}
	if len(d.SigningAlgs) > 0 && !contains(d.SigningAlgs, t.header.Alg) {
		return nil, badToken("bad_token", "an algorithm the provider doesn't say it uses")
	}
	keys, err := c.keysFor(ctx, d.JWKSURI, false)
	if err != nil {
		return nil, err
	}
	payload, used, err := verify(t, keys)
	if errors.Is(err, errNoKey) {
		// A key MetaRush hasn't seen: the provider may have started using a new
		// one (key rotation). Ask once for its current keys.
		if keys, err = c.keysFor(ctx, d.JWKSURI, true); err != nil {
			return nil, err
		}
		payload, used, err = verify(t, keys)
	}
	if err != nil {
		return nil, badToken("bad_signature", err.Error())
	}
	var cl Claims
	dec := json.NewDecoder(bytes.NewReader(payload))
	if err := dec.Decode(&cl); err != nil {
		return nil, badToken("bad_token", "claims: "+err.Error())
	}
	if err := c.checkClaims(d, &cl, nonce); err != nil {
		return nil, err
	}
	if used.issuer != "" && !keyIssuerOK(used.issuer, &cl) {
		// Microsoft's shared key sets say which issuer each key signs for.
		return nil, badToken("bad_issuer", "a key published for another issuer")
	}
	return c.identity(&cl), nil
}

// checkClaims is OpenID Connect Core 3.1.3.7 for an already signed token.
func (c *Client) checkClaims(d *Discovery, cl *Claims, nonce string) error {
	if !c.issuerOK(d, cl) {
		return badToken("bad_issuer", "iss "+cl.Issuer)
	}
	// Exactly this app: a token also meant for others is refused (Core
	// 3.1.3.7: "additional audiences not trusted by the Client").
	if len(cl.Audience) != 1 || cl.Audience[0] != c.ClientID {
		return badToken("bad_audience", "aud "+strings.Join(cl.Audience, ","))
	}
	if cl.AZP != "" && cl.AZP != c.ClientID {
		return badToken("bad_audience", "azp "+cl.AZP)
	}
	now := c.now()
	switch {
	case cl.Expires == 0:
		return badToken("expired", "no exp")
	case !now.Before(cl.Expires.Time().Add(Skew)):
		return badToken("expired", "exp "+strconv.FormatInt(int64(cl.Expires), 10))
	case cl.IssuedAt == 0:
		return badToken("bad_time", "no iat")
	case cl.IssuedAt.Time().After(now.Add(Skew)):
		return badToken("bad_time", "iat in the future")
	case now.Sub(cl.IssuedAt.Time()) > MaxAge+Skew:
		return badToken("expired", "iat too old")
	case cl.NotBefore != 0 && cl.NotBefore.Time().After(now.Add(Skew)):
		return badToken("bad_time", "nbf in the future")
	}
	if nonce == "" || subtle.ConstantTimeCompare([]byte(cl.Nonce), []byte(nonce)) != 1 {
		return badToken("bad_nonce", "nonce")
	}
	if strings.TrimSpace(cl.Subject) == "" || len(cl.Subject) > 255 {
		return badToken("bad_token", "no sub")
	}
	return nil
}

// keyIssuerOK holds a key that names its issuer to it: exactly, or with
// {tenantid} filled in by the token's own directory.
func keyIssuerOK(issuer string, cl *Claims) bool {
	if strings.Contains(issuer, "{tenantid}") {
		return isGUID(cl.TID) && cl.Issuer == strings.ReplaceAll(issuer, "{tenantid}", cl.TID)
	}
	return cl.Issuer == issuer
}

// issuerOK compares iss exactly with the provider's issuer.
func (c *Client) issuerOK(d *Discovery, cl *Claims) bool {
	if c.Kind == Google && (cl.Issuer == GoogleIssuer || cl.Issuer == "accounts.google.com") {
		// Google's documentation: either spelling.
		return d.Issuer == GoogleIssuer
	}
	if d.tenantTemplate {
		// Microsoft's common and organizations: the token names its own directory.
		if !isGUID(cl.TID) {
			return false
		}
		return cl.Issuer == microsoftPrefix+strings.ToLower(cl.TID)+"/v2.0" || cl.Issuer == microsoftPrefix+cl.TID+"/v2.0"
	}
	if c.Kind == Microsoft && cl.TID != "" && !strings.Contains(strings.ToLower(cl.Issuer), strings.ToLower(cl.TID)) {
		return false // a directory's token always names that directory
	}
	return cl.Issuer == d.Issuer
}

// identity says who the token is, and whether its email counts (D429).
func (c *Client) identity(cl *Claims) *Identity {
	id := &Identity{Provider: c.Kind, Issuer: cl.Issuer, Subject: cl.Subject, Name: strings.TrimSpace(cl.Name)}
	email := strings.TrimSpace(cl.Email)
	if !plausibleEmail(email) {
		email = ""
	}
	id.Email = email
	switch c.Kind {
	case Google:
		id.Key = keyOf("google", cl.Subject)
		id.Domain = strings.ToLower(strings.TrimSpace(cl.HD))
		id.EmailRefuted = email != "" && !bool(cl.EmailVerified)
		// Google answers for an address only when it is a Gmail address or the
		// account belongs to a Google Workspace domain (hd); an outside address
		// it once checked may have changed hands since.
		id.EmailTrusted = email != "" && bool(cl.EmailVerified) && (gmail(email) || id.Domain != "")
		switch {
		case email == "":
			id.Why = "Google didn't give the account's email"
		case id.EmailRefuted:
			id.Why = "Google says it hasn't checked the email"
		case !id.EmailTrusted:
			id.Why = "Google doesn't answer for an email outside Gmail and Google Workspace"
		}
	case Microsoft:
		subject := cl.Subject
		if cl.OID != "" {
			subject = cl.OID // the same person whichever app they sign in to
		}
		id.Key = keyOf("microsoft", strings.ToLower(cl.TID), subject)
		id.Domain = strings.ToLower(cl.TID)
		// Microsoft's email claim is checked by nobody unless its domain's
		// owner is verified (the optional claim xms_edov): only then is it
		// the person's (its docs: never authorize by email alone).
		id.EmailTrusted = email != "" && bool(cl.EmailDomainVerified)
		switch {
		case email == "":
			id.Why = "Microsoft didn't give the account's email (the app's optional claims)"
		case !id.EmailTrusted:
			id.Why = "Microsoft doesn't say the email's domain is verified (the optional claim xms_edov)"
		}
	default:
		id.Key = keyOf("oidc", cl.Issuer, cl.Subject)
		id.EmailRefuted = email != "" && !bool(cl.EmailVerified)
		id.EmailTrusted = email != "" && bool(cl.EmailVerified)
		if id.EmailTrusted {
			_, domain, _ := strings.Cut(email, "@")
			id.Domain = strings.ToLower(domain)
		}
		switch {
		case email == "":
			id.Why = "the provider didn't give the account's email"
		case !id.EmailTrusted:
			id.Why = "the provider doesn't say it checked the email"
		}
	}
	if id.Name == "" {
		id.Name = strings.TrimSpace(cl.Preferred)
	}
	if len([]rune(id.Name)) > 80 {
		id.Name = string([]rune(id.Name)[:80])
	}
	return id
}

func gmail(email string) bool {
	_, domain, _ := strings.Cut(strings.ToLower(email), "@")
	return domain == "gmail.com" || domain == "googlemail.com"
}

// plausibleEmail keeps an address MetaRush could show and match: one @, no
// spaces or control characters, not too long.
func plausibleEmail(s string) bool {
	if len(s) < 3 || len(s) > 254 || strings.Count(s, "@") != 1 || strings.HasPrefix(s, "@") || strings.HasSuffix(s, "@") {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// keyOf is the stable account key: a hash, so the stored value has one shape.
func keyOf(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}
