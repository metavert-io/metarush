// Package c2pa puts Content Credentials (C2PA manifests) into the videos
// MetaRush delivers (D415–D424): what a manifest says about how a video was
// made, the signing credential an owner sets up on a computer, and c2patool —
// the C2PA project's own tool — run so that the private key never leaves
// MetaRush's own process (c2patool asks MetaRush to sign, D416).
package c2pa

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // the subject key identifier is SHA-1 by definition (RFC 5280), not a signature
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
)

// The algorithms C2PA signs with, by c2patool's own names.
const (
	AlgES256   = "es256"
	AlgES384   = "es384"
	AlgES512   = "es512"
	AlgPS256   = "ps256"
	AlgEd25519 = "ed25519"
)

// The purposes (extended key usages) a signing certificate may say it is
// for: what c2pa-rs accepts for a manifest's signer once MetaRush's settings
// list them (c2patool 0.28.3, checked on a Mac): email protection, document
// signing, C2PA's own claim signing and Microsoft's C2PA signing.
var (
	oidDocumentSigning  = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 36}
	oidClaimSigning     = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 62558, 2, 1}
	oidMicrosoftSigning = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 76, 59, 1, 9}
)

// TrustConfig is the list of those purposes in c2pa-rs's settings form (one
// OID per line): without it c2pa-rs 0.91.6 refuses a certificate for document
// or claim signing alone after signing, and only email protection passes.
const TrustConfig = "1.3.6.1.5.5.7.3.4\n1.3.6.1.5.5.7.3.36\n1.3.6.1.4.1.62558.2.1\n1.3.6.1.4.1.311.76.59.1.9\n"

// testMarker is the organisational unit of a test certificate MetaRush made
// (and of the throwaway authority that issued it): how it is told apart from
// one a person brought, wherever it shows up.
const testMarker = "Made by MetaRush for testing"

// Credential is a signing credential: the signing certificate, then the
// certificates that issued it (not the root), and its private key. The key
// never leaves MetaRush's process: c2patool asks MetaRush to sign (D416).
type Credential struct {
	Chain []*x509.Certificate
	Key   crypto.Signer
	Alg   string
}

// Leaf is the signing certificate.
func (c *Credential) Leaf() *x509.Certificate { return c.Chain[0] }

// ChainPEM is the certificate chain as PEM text (public: c2patool and every
// verifier see it).
func (c *Credential) ChainPEM() []byte {
	var b bytes.Buffer
	for _, x := range c.Chain {
		_ = pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: x.Raw})
	}
	return b.Bytes()
}

// Test reports a test certificate MetaRush made: verifiers never trust it.
func (c *Credential) Test() bool { return isTest(c.Leaf()) }

func isTest(x *x509.Certificate) bool {
	return slices.Contains(x.Issuer.OrganizationalUnit, testMarker) || slices.Contains(x.Subject.OrganizationalUnit, testMarker)
}

// Facts are what anyone may see about the credential: never the key.
func (c *Credential) Facts() models.SigningCertificate {
	leaf := c.Leaf()
	sum := sha256.Sum256(leaf.Raw)
	f := models.SigningCertificate{Name: leaf.Subject.CommonName, Issuer: nameOf(leaf.Issuer), Fingerprint: hex.EncodeToString(sum[:]),
		Serial: leaf.SerialNumber.String(), ValidFrom: leaf.NotBefore.UTC(), ValidUntil: leaf.NotAfter.UTC(), Alg: c.Alg, Test: c.Test(), Certificates: len(c.Chain)}
	if len(leaf.Subject.Organization) > 0 {
		f.Organisation = leaf.Subject.Organization[0]
	}
	f.Signer = nameOf(leaf.Subject)
	return f
}

// nameOf is how verifiers name a certificate's holder: its organisation,
// else its common name.
func nameOf(n pkix.Name) string {
	if len(n.Organization) > 0 && strings.TrimSpace(n.Organization[0]) != "" {
		return n.Organization[0]
	}
	return n.CommonName
}

// Parse reads a signing credential a person brought: the certificate chain
// (the signing certificate first) and its private key, both PEM. It says in
// plain words what is wrong with the files; Problems says what is wrong with
// the certificate.
func Parse(chainPEM, keyPEM []byte) (*Credential, error) {
	var chain []*x509.Certificate
	rest := chainPEM
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		switch {
		case blk.Type == "CERTIFICATE":
			x, err := x509.ParseCertificate(blk.Bytes)
			if err != nil {
				return nil, apperr.Wrap(apperr.InvalidInput, err, "One of the certificates in the certificate file can't be read.")
			}
			chain = append(chain, x)
		case strings.Contains(blk.Type, "PRIVATE KEY"):
			return nil, apperr.New(apperr.InvalidInput, "The certificate file holds a private key. Give the certificates in one file and the key in another.")
		}
	}
	if len(chain) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "The certificate file holds no certificate MetaRush can read: it takes PEM files, the text form certificate authorities send.")
	}
	if len(chain) > 8 {
		return nil, apperr.New(apperr.InvalidInput, "The certificate file holds %d certificates; a signing certificate and the ones that issued it are a few at most.", len(chain))
	}
	key, err := parseKey(keyPEM)
	if err != nil {
		return nil, err
	}
	alg, err := algFor(key)
	if err != nil {
		return nil, err
	}
	c := &Credential{Chain: chain, Key: key, Alg: alg}
	if !samePublic(chain[0].PublicKey, key.Public()) {
		// Maybe the chain starts with the issuer: say which way round it is.
		for _, x := range chain[1:] {
			if samePublic(x.PublicKey, key.Public()) {
				return nil, apperr.New(apperr.InvalidInput, "The signing certificate must come first in the certificate file, before the certificates that issued it.")
			}
		}
		return nil, apperr.New(apperr.InvalidInput, "The private key doesn't belong to the signing certificate (the first certificate in the file).")
	}
	return c, nil
}

// parseKey reads a private key: PKCS #8, or the older EC and RSA forms. A key
// protected with a password is refused (MetaRush keeps it in the keychain).
func parseKey(b []byte) (crypto.Signer, error) {
	rest := b
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		if blk.Headers["Proc-Type"] != "" || blk.Type == "ENCRYPTED PRIVATE KEY" {
			return nil, apperr.New(apperr.InvalidInput, "The key file is protected with a password. Give MetaRush the key without one: it keeps it in this computer's keychain, never in a file.")
		}
		var k any
		var err error
		switch blk.Type {
		case "PRIVATE KEY":
			k, err = x509.ParsePKCS8PrivateKey(blk.Bytes)
		case "EC PRIVATE KEY":
			k, err = x509.ParseECPrivateKey(blk.Bytes)
		case "RSA PRIVATE KEY":
			k, err = x509.ParsePKCS1PrivateKey(blk.Bytes)
		case "CERTIFICATE":
			return nil, apperr.New(apperr.InvalidInput, "The key file holds a certificate, not a private key.")
		default:
			continue // EC PARAMETERS and the like
		}
		if err != nil {
			return nil, apperr.Wrap(apperr.InvalidInput, err, "The private key can't be read.")
		}
		s, ok := k.(crypto.Signer)
		if !ok {
			return nil, apperr.New(apperr.InvalidInput, "That kind of private key can't sign Content Credentials.")
		}
		return s, nil
	}
	return nil, apperr.New(apperr.InvalidInput, "The key file holds no private key MetaRush can read: it takes PEM files, the text form.")
}

// algFor is the C2PA algorithm a key signs with.
func algFor(k crypto.Signer) (string, error) {
	switch key := k.(type) {
	case *ecdsa.PrivateKey:
		switch key.Curve {
		case elliptic.P256():
			return AlgES256, nil
		case elliptic.P384():
			return AlgES384, nil
		case elliptic.P521():
			return AlgES512, nil
		}
		return "", apperr.New(apperr.InvalidInput, "Content Credentials take elliptic-curve keys on P-256, P-384 or P-521 only.")
	case *rsa.PrivateKey:
		if key.N.BitLen() < 2048 {
			return "", apperr.New(apperr.InvalidInput, "That RSA key is %d bits; Content Credentials need 2048 or more.", key.N.BitLen())
		}
		return AlgPS256, nil
	case ed25519.PrivateKey:
		return AlgEd25519, nil
	}
	return "", apperr.New(apperr.InvalidInput, "That kind of private key can't sign Content Credentials (they take ECDSA, RSA or Ed25519 keys).")
}

func samePublic(a, b crypto.PublicKey) bool {
	type equaler interface{ Equal(crypto.PublicKey) bool }
	e, ok := a.(equaler)
	return ok && e.Equal(b)
}

// Problems says in plain words what keeps a certificate from signing Content
// Credentials now (nil: nothing): what c2pa-rs itself refuses (checked
// against c2patool 0.28.3 on a Mac), so setup says it before any delivery.
func (c *Credential) Problems(now time.Time) []string {
	leaf := c.Leaf()
	var out []string
	switch {
	case now.Before(leaf.NotBefore):
		out = append(out, fmt.Sprintf("The signing certificate isn't valid until %s.", leaf.NotBefore.UTC().Format("2 Jan 2006")))
	case now.After(leaf.NotAfter):
		out = append(out, fmt.Sprintf("The signing certificate ran out on %s.", leaf.NotAfter.UTC().Format("2 Jan 2006")))
	}
	if selfSigned(leaf) {
		out = append(out, "The signing certificate is signed by itself, so verifiers can't tell who it is: for trying Content Credentials, make a test certificate here instead; for clients, use one a certificate authority issued.")
	}
	if leaf.IsCA {
		out = append(out, "The first certificate is a certificate authority's, not a certificate for signing.")
	}
	if leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		out = append(out, "The signing certificate isn't marked for digital signatures (its key usage).")
	}
	if !leaf.IsCA && leaf.KeyUsage&x509.KeyUsageCertSign != 0 {
		out = append(out, "The signing certificate is also marked for issuing certificates, which Content Credentials refuse.")
	}
	if len(leaf.AuthorityKeyId) == 0 && !selfSigned(leaf) {
		out = append(out, "The signing certificate doesn't name the key of the authority that issued it (authority key identifier), which Content Credentials need.")
	}
	if p := purposeProblem(leaf); p != "" {
		out = append(out, p)
	}
	switch leaf.SignatureAlgorithm {
	case x509.MD5WithRSA, x509.SHA1WithRSA, x509.ECDSAWithSHA1, x509.DSAWithSHA1, x509.DSAWithSHA256:
		out = append(out, "The signing certificate was issued with an outdated signature (SHA-1 or older).")
	}
	for i := 0; i+1 < len(c.Chain); i++ {
		if err := c.Chain[i].CheckSignatureFrom(c.Chain[i+1]); err != nil {
			out = append(out, "The certificates in the file aren't in order, or one is missing: each must be issued by the next.")
			break
		}
	}
	for _, x := range c.Chain[1:] {
		if now.After(x.NotAfter) {
			out = append(out, fmt.Sprintf("The certificate of “%s”, which issued the signing certificate, ran out on %s.", nameOf(x.Subject), x.NotAfter.UTC().Format("2 Jan 2006")))
			break
		}
	}
	return out
}

// selfSigned reports a certificate signed with its own key (checked on the
// signature itself: Go's CheckSignatureFrom wants an authority as the parent).
func selfSigned(x *x509.Certificate) bool {
	return bytes.Equal(x.RawIssuer, x.RawSubject) && x.CheckSignature(x.SignatureAlgorithm, x.RawTBSCertificate, x.Signature) == nil
}

// purposeProblem says what is wrong with what a signing certificate says it
// is for ("" nothing): it must name signing documents or email, or C2PA's
// own claim signing, and never "anything".
func purposeProblem(x *x509.Certificate) string {
	if len(x.ExtKeyUsage) == 0 && len(x.UnknownExtKeyUsage) == 0 {
		return "The signing certificate doesn't say what it is for (no extended key usage); Content Credentials need one for document signing, email protection or C2PA claim signing."
	}
	ok := false
	for _, u := range x.ExtKeyUsage {
		switch u {
		case x509.ExtKeyUsageAny:
			return "The signing certificate says it is for anything (any extended key usage), which Content Credentials refuse."
		case x509.ExtKeyUsageEmailProtection:
			ok = true
		}
	}
	for _, u := range x.UnknownExtKeyUsage {
		if u.Equal(oidDocumentSigning) || u.Equal(oidClaimSigning) || u.Equal(oidMicrosoftSigning) {
			ok = true
		}
	}
	if !ok {
		return "The signing certificate isn't for signing documents (its extended key usage); Content Credentials need one for document signing, email protection or C2PA claim signing."
	}
	return ""
}

// MakeTest makes a test credential on this computer: a throwaway authority
// (its key is forgotten at once) issues an ES256 signing certificate for a
// year. Verifiers show its signer as unknown: it is for trying Content
// Credentials, never for a client. org is an organisation's name a person
// chose to put in it ("" none).
func MakeTest(org string, now time.Time) (*Credential, error) {
	org = cleanName(org)
	name := "MetaRush test certificate"
	if org != "" {
		name = org + " (test certificate)"
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	notBefore, notAfter := now.Add(-time.Hour).UTC().Truncate(time.Second), now.AddDate(1, 0, 0).UTC().Truncate(time.Second)
	caTpl := &x509.Certificate{SerialNumber: serialNumber(), Subject: pkix.Name{CommonName: "MetaRush test authority (not trusted)", OrganizationalUnit: []string{testMarker}},
		NotBefore: notBefore, NotAfter: notAfter.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, caKey.Public(), caKey)
	if err != nil {
		return nil, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	subject := pkix.Name{CommonName: name, OrganizationalUnit: []string{testMarker}}
	if org != "" {
		subject.Organization = []string{org}
	}
	pub, _ := x509.MarshalPKIXPublicKey(key.Public())
	ski := sha1.Sum(pub) //nolint:gosec // RFC 5280's key identifier
	tpl := &x509.Certificate{SerialNumber: serialNumber(), Subject: subject, NotBefore: notBefore, NotAfter: notAfter,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, SubjectKeyId: ski[:],
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}, UnknownExtKeyUsage: []asn1.ObjectIdentifier{oidDocumentSigning}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca, key.Public(), caKey)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &Credential{Chain: []*x509.Certificate{leaf}, Key: key, Alg: AlgES256}, nil
}

func serialNumber() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil || n.Sign() == 0 {
		return big.NewInt(time.Now().UnixNano())
	}
	return n
}

// cleanName keeps an organisation's name to one short line of printable letters.
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > 64 {
		s = strings.TrimSpace(string(r[:64]))
	}
	return s
}

// ErrNotSignature refuses bytes that aren't a COSE signature's input.
var ErrNotSignature = errors.New("not a COSE Sig_structure")

// sigStructure starts every COSE_Sign1 signature's input: an array of four
// whose first item is the text "Signature1" (RFC 9052, 4.4).
var sigStructure = append([]byte{0x84, 0x6a}, "Signature1"...)

// Sign signs a C2PA claim's signature input as COSE wants it: ECDSA as r‖s
// (fixed width), RSA-PSS with the hash's own salt length, Ed25519 as is. It
// signs nothing but COSE_Sign1 signature inputs.
func (c *Credential) Sign(msg []byte) ([]byte, error) {
	if !bytes.HasPrefix(msg, sigStructure) {
		return nil, ErrNotSignature
	}
	switch key := c.Key.(type) {
	case *ecdsa.PrivateKey:
		var digest []byte
		switch c.Alg {
		case AlgES384:
			s := sha512.Sum384(msg)
			digest = s[:]
		case AlgES512:
			s := sha512.Sum512(msg)
			digest = s[:]
		default:
			s := sha256.Sum256(msg)
			digest = s[:]
		}
		r, s, err := ecdsa.Sign(rand.Reader, key, digest)
		if err != nil {
			return nil, err
		}
		n := (key.Curve.Params().BitSize + 7) / 8
		out := make([]byte, 2*n)
		r.FillBytes(out[:n])
		s.FillBytes(out[n:])
		return out, nil
	case *rsa.PrivateKey:
		s := sha256.Sum256(msg)
		return rsa.SignPSS(rand.Reader, key, crypto.SHA256, s[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	case ed25519.PrivateKey:
		return ed25519.Sign(key, msg), nil
	}
	return nil, fmt.Errorf("c2pa: no signing for %T", c.Key)
}
