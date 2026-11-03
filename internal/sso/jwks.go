package sso

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The provider's signing keys (its JWKS, RFC 7517) are fetched from the
// discovery document's jwks_uri and kept in memory for as long as the answer
// says (Cache-Control max-age, between 5 minutes and a day; an hour when it
// doesn't say). A token signed by a key MetaRush hasn't seen fetches the set
// again at once — that is how a provider's new key is picked up — but at most
// every refetchGap, so tokens naming made-up keys can't make MetaRush hammer
// the provider. Keys for encryption, of a kind MetaRush doesn't check, or too
// small (RSA under 2048 bits) are left out; one odd key never spoils the set.

// key is one usable public key.
type key struct {
	kid, kty, alg, crv string
	// issuer: the issuer this key signs for, when the set says (Microsoft's).
	issuer string
	rsa    *rsa.PublicKey
	ec     *ecdsa.PublicKey
	ed     ed25519.PublicKey
}

// jwk is a key as published.
type jwk struct {
	Kty    string   `json:"kty"`
	Kid    string   `json:"kid"`
	Use    string   `json:"use"`
	Alg    string   `json:"alg"`
	KeyOps []string `json:"key_ops"`
	N      string   `json:"n"`
	E      string   `json:"e"`
	Crv    string   `json:"crv"`
	X      string   `json:"x"`
	Y      string   `json:"y"`
	// Issuer: Microsoft's key sets name the issuer each key signs for.
	Issuer string `json:"issuer"`
}

// parseKeys reads a key set, keeping the keys MetaRush can check signatures with.
func parseKeys(b []byte) ([]*key, error) {
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(b, &set); err != nil {
		return nil, errors.New("the provider's keys aren't in a form MetaRush can read")
	}
	var out []*key
	for _, raw := range set.Keys {
		var j jwk
		if json.Unmarshal(raw, &j) != nil {
			continue
		}
		if k := j.key(); k != nil {
			out = append(out, k)
		}
	}
	return out, nil
}

func (j jwk) key() *key {
	if j.Use != "" && j.Use != "sig" {
		return nil
	}
	if len(j.KeyOps) > 0 {
		verify := false
		for _, op := range j.KeyOps {
			verify = verify || op == "verify"
		}
		if !verify {
			return nil
		}
	}
	if j.Alg != "" && !Supported(j.Alg) {
		return nil
	}
	k := &key{kid: j.Kid, kty: j.Kty, alg: j.Alg, crv: j.Crv, issuer: j.Issuer}
	switch j.Kty {
	case "RSA":
		n, err1 := b64(j.N)
		e, err2 := b64(j.E)
		if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 {
			return nil
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		if pub.N.BitLen() < 2048 || pub.E < 3 || pub.E%2 == 0 {
			return nil
		}
		k.rsa = pub
	case "EC":
		var curve elliptic.Curve
		switch j.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil
		}
		x, err1 := b64(j.X)
		y, err2 := b64(j.Y)
		size := (curve.Params().BitSize + 7) / 8
		if err1 != nil || err2 != nil || len(x) != size || len(y) != size {
			return nil
		}
		point := append(append([]byte{4}, x...), y...)
		pub, err := ecdsa.ParseUncompressedPublicKey(curve, point) // refuses a point off the curve
		if err != nil {
			return nil
		}
		k.ec = pub
	case "OKP":
		x, err := b64(j.X)
		if j.Crv != "Ed25519" || err != nil || len(x) != ed25519.PublicKeySize {
			return nil
		}
		k.ed = ed25519.PublicKey(x)
	default:
		return nil
	}
	return k
}

// Key set cache times.
const (
	keysDefault = time.Hour
	keysMin     = 5 * time.Minute
	keysMax     = 24 * time.Hour
)

// refetchGap is the least time between two fetches of a key set made for an
// unknown key (tests shorten it).
var refetchGap = 30 * time.Second

type keySet struct {
	mu      sync.Mutex
	keys    []*key
	fetched time.Time // when the keys were read (this process's clock)
	life    time.Duration
	tried   time.Time // the last fetch, kept or not
}

var keySets sync.Map // jwks_uri → *keySet

// keysFor returns the keys at uri, fetching them when there are none, they
// are past their time, or fresh asks for them again (an unknown key).
func (c *Client) keysFor(ctx context.Context, uri string, fresh bool) ([]*key, error) {
	v, _ := keySets.LoadOrStore(uri, &keySet{})
	ks := v.(*keySet)
	ks.mu.Lock()
	defer ks.mu.Unlock()
	age := time.Since(ks.fetched)
	stale := ks.keys == nil || age > ks.life
	if fresh && time.Since(ks.tried) >= refetchGap {
		stale = true
	}
	if !stale {
		return ks.keys, nil
	}
	if ks.keys != nil && !fresh && time.Since(ks.tried) < refetchGap {
		return ks.keys, nil // a failed refresh moments ago: keep what we have
	}
	ks.tried = time.Now()
	b, hdr, err := c.get(ctx, uri, "read its signing keys")
	if err != nil {
		if ks.keys != nil {
			return ks.keys, nil // the provider didn't answer: the keys it gave before still count
		}
		return nil, err
	}
	keys, err := parseKeys(b)
	if err != nil {
		if ks.keys != nil {
			return ks.keys, nil
		}
		return nil, err
	}
	if len(keys) == 0 {
		return nil, errors.New("the provider published no signing keys MetaRush can use")
	}
	ks.keys, ks.fetched, ks.life = keys, time.Now(), maxAge(hdr)
	return keys, nil
}

// maxAge reads how long an answer may be kept, within bounds.
func maxAge(h http.Header) time.Duration {
	for _, part := range strings.Split(h.Get("Cache-Control"), ",") {
		name, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || !strings.EqualFold(name, "max-age") {
			continue
		}
		if n, err := strconv.Atoi(strings.Trim(val, `"`)); err == nil {
			return min(max(time.Duration(n)*time.Second, keysMin), keysMax)
		}
	}
	return keysDefault
}

// forgetKeys drops the remembered keys (tests).
func forgetKeys() { keySets.Range(func(k, _ any) bool { keySets.Delete(k); return true }) }
