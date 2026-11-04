package sso

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"hash"
	"math/big"
	"strings"
)

// A signed token (JWS compact serialization, RFC 7515) is checked strictly
// here with the standard library, as the rest of MetaRush signs and checks
// tokens (D426): three parts, base64url without padding, a header that names
// one of the algorithms below and nothing MetaRush doesn't understand (crit),
// and a signature made by a key from the provider's published set — never a
// key the token brings itself (jwk, jku, x5u are ignored). "none" and the
// shared-secret algorithms (HS256…) are refused: a provider's public keys
// could otherwise be used as an HMAC secret.

// maxToken bounds a token MetaRush will read.
const maxToken = 16 << 10

// algorithms MetaRush checks, by family.
var algorithms = map[string]struct {
	kty  string
	hash crypto.Hash
	pss  bool
	crv  string // EC: the curve the algorithm fixes
}{
	"RS256": {kty: "RSA", hash: crypto.SHA256}, "RS384": {kty: "RSA", hash: crypto.SHA384}, "RS512": {kty: "RSA", hash: crypto.SHA512},
	"PS256": {kty: "RSA", hash: crypto.SHA256, pss: true}, "PS384": {kty: "RSA", hash: crypto.SHA384, pss: true}, "PS512": {kty: "RSA", hash: crypto.SHA512, pss: true},
	"ES256": {kty: "EC", hash: crypto.SHA256, crv: "P-256"}, "ES384": {kty: "EC", hash: crypto.SHA384, crv: "P-384"}, "ES512": {kty: "EC", hash: crypto.SHA512, crv: "P-521"},
	"EdDSA": {kty: "OKP"},
}

// Supported reports whether MetaRush checks signatures made with alg.
func Supported(alg string) bool { _, ok := algorithms[alg]; return ok }

// header is a token's protected header (only what MetaRush reads).
type header struct {
	Alg  string          `json:"alg"`
	Kid  string          `json:"kid"`
	Typ  string          `json:"typ"`
	Crit json.RawMessage `json:"crit"`
}

// token is a signed token split into its parts.
type token struct {
	header  header
	signed  []byte // header.payload, as signed
	payload []byte
	sig     []byte
}

// Problems with a token's form and signature.
var (
	errMalformed = errors.New("the sign-in answer isn't a token MetaRush can read")
	errAlg       = errors.New("the sign-in answer is signed in a way MetaRush doesn't accept")
	errSignature = errors.New("the sign-in answer's signature doesn't match the provider's keys")
	errNoKey     = errors.New("none of the provider's keys signed the sign-in answer")
)

// b64 decodes base64url without padding, refusing anything else.
func b64(s string) ([]byte, error) {
	if strings.ContainsAny(s, "=+/ \t\r\n") {
		return nil, errMalformed
	}
	return base64.RawURLEncoding.DecodeString(s)
}

// parseToken splits a compact JWS and reads its header; the signature is not
// checked yet.
func parseToken(raw string) (*token, error) {
	if len(raw) == 0 || len(raw) > maxToken {
		return nil, errMalformed
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, errMalformed
	}
	hb, err := b64(parts[0])
	if err != nil {
		return nil, errMalformed
	}
	var h header
	if err := json.Unmarshal(hb, &h); err != nil || h.Alg == "" {
		return nil, errMalformed
	}
	if len(h.Crit) > 0 && string(h.Crit) != "null" {
		return nil, errAlg // an extension MetaRush must understand, and doesn't
	}
	if _, ok := algorithms[h.Alg]; !ok {
		return nil, errAlg
	}
	if h.Typ != "" && !strings.EqualFold(h.Typ, "JWT") && !strings.EqualFold(h.Typ, "application/jwt") {
		return nil, errAlg // an access token (at+jwt) or something else, not an ID token
	}
	payload, err := b64(parts[1])
	if err != nil {
		return nil, errMalformed
	}
	sig, err := b64(parts[2])
	if err != nil || len(sig) == 0 {
		return nil, errMalformed
	}
	return &token{header: h, signed: []byte(parts[0] + "." + parts[1]), payload: payload, sig: sig}, nil
}

// verifyWith checks the token's signature with one key.
func (t *token) verifyWith(k *key) error {
	a := algorithms[t.header.Alg]
	if k.kty != a.kty || (k.alg != "" && k.alg != t.header.Alg) {
		return errNoKey
	}
	switch k.kty {
	case "RSA":
		sum := digest(a.hash, t.signed)
		if a.pss {
			if rsa.VerifyPSS(k.rsa, a.hash, sum, t.sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: a.hash}) != nil {
				return errSignature
			}
			return nil
		}
		if rsa.VerifyPKCS1v15(k.rsa, a.hash, sum, t.sig) != nil {
			return errSignature
		}
		return nil
	case "EC":
		if k.crv != a.crv {
			return errNoKey
		}
		size := (k.ec.Curve.Params().BitSize + 7) / 8
		if len(t.sig) != 2*size {
			return errSignature
		}
		r, s := new(big.Int).SetBytes(t.sig[:size]), new(big.Int).SetBytes(t.sig[size:])
		if !ecdsa.Verify(k.ec, digest(a.hash, t.signed), r, s) {
			return errSignature
		}
		return nil
	case "OKP":
		if len(t.sig) != ed25519.SignatureSize || !ed25519.Verify(k.ed, t.signed, t.sig) {
			return errSignature
		}
		return nil
	}
	return errNoKey
}

func digest(h crypto.Hash, b []byte) []byte {
	var hh hash.Hash
	switch h {
	case crypto.SHA384:
		hh = sha512.New384()
	case crypto.SHA512:
		hh = sha512.New()
	default:
		hh = sha256.New()
	}
	hh.Write(b)
	return hh.Sum(nil)
}

// verify checks a token's signature against a key set: the key its kid names
// (or, without a kid, each key of the right kind). It returns the payload and
// the key that signed it.
func verify(t *token, keys []*key) ([]byte, *key, error) {
	var candidates []*key
	for _, k := range keys {
		if t.header.Kid != "" && k.kid != t.header.Kid {
			continue
		}
		if k.kty == algorithms[t.header.Alg].kty {
			candidates = append(candidates, k)
		}
	}
	if len(candidates) == 0 {
		return nil, nil, errNoKey
	}
	if len(candidates) > 8 {
		candidates = candidates[:8] // a token without a kid tries a few keys, not every one
	}
	last := errNoKey
	for _, k := range candidates {
		err := t.verifyWith(k)
		if err == nil {
			return t.payload, k, nil
		}
		last = err
	}
	return nil, nil, last
}
