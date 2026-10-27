// Package fakeserve stands in for `tailscale serve` in the remote-access
// end-to-end run (D128): an HTTPS server with a self-signed certificate for a
// made-up tailnet name that hands each request to MetaRush the way tailscale
// serve does (Rewrite in ipn/ipnlocal/serve.go): the Host the browser asked
// for is kept; X-Forwarded-Host, X-Forwarded-Proto: https and X-Forwarded-For
// (the browser's tailnet address) are set; any Tailscale-* header the browser
// sent is removed; and for a tailnet user Tailscale-User-Login, -Name (RFC 2047
// for letters outside ASCII), -Profile-Pic and -Headers-Info are added.
//
// Which tailnet user a browser is comes from a test-only header,
// X-Fake-Tailnet-User: <key> (removed before MetaRush sees it); without it the
// browser is a tagged computer (no identity headers), like real serve.
//
// A second, "spoof" listener plays a hop that is not tailscale serve on this
// computer: it keeps the browser's own Tailscale-* headers and connects to its
// backend from a source address that isn't loopback (the link-local address on
// lo0), which MetaRush must refuse.
package fakeserve

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// User is one person on the pretend tailnet.
type User struct {
	Key, Login, Name, IP string
}

// Options configure the stand-in.
type Options struct {
	// Name is the tailnet name the certificate is for (studio-mac.tail0000.ts.net).
	Name string
	// Listen and Backend: the HTTPS address browsers use and MetaRush's own.
	Listen, Backend string
	Users           []User
	// SpoofListen and SpoofBackend: the hop that keeps the browser's headers
	// and connects from SpoofFrom (a non-loopback address on this computer),
	// claiming SpoofHost (the public address) as the Host.
	SpoofListen, SpoofBackend, SpoofFrom, SpoofHost string
}

// Server is a running stand-in.
type Server struct {
	// SPKI is the base64 SHA-256 of the certificate's public key, for
	// Chromium's --ignore-certificate-errors-spki-list.
	SPKI    string
	servers []*http.Server
}

// Start runs the stand-in until Close.
func Start(o Options) (*Server, error) {
	cert, spki, err := selfSigned(o.Name)
	if err != nil {
		return nil, err
	}
	s := &Server{SPKI: spki}
	be, err := url.Parse(o.Backend)
	if err != nil {
		return nil, err
	}
	users := map[string]User{}
	for _, u := range o.Users {
		users[u.Key] = u
	}
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(be)
		r.Out.Host = r.In.Host
		for k := range r.Out.Header {
			if strings.HasPrefix(strings.ToLower(k), "tailscale-") {
				r.Out.Header.Del(k)
			}
		}
		key := r.In.Header.Get("X-Fake-Tailnet-User")
		r.Out.Header.Del("X-Fake-Tailnet-User")
		r.Out.Header.Set("X-Forwarded-Host", r.In.Host)
		r.Out.Header.Set("X-Forwarded-Proto", "https")
		ip := "100.100.100.100" // a tagged computer
		if u, ok := users[key]; ok {
			ip = u.IP
			r.Out.Header.Set("Tailscale-User-Login", mime.QEncoding.Encode("utf-8", u.Login))
			r.Out.Header.Set("Tailscale-User-Name", mime.QEncoding.Encode("utf-8", u.Name))
			r.Out.Header.Set("Tailscale-User-Profile-Pic", "")
			r.Out.Header.Set("Tailscale-Headers-Info", "https://tailscale.com/s/serve-headers")
		}
		r.Out.Header.Set("X-Forwarded-For", ip)
	}}
	if err := s.serve(o.Listen, cert, proxy); err != nil {
		return nil, err
	}
	if o.SpoofListen != "" {
		sb, err := url.Parse(o.SpoofBackend)
		if err != nil {
			return nil, err
		}
		from, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(o.SpoofFrom, "0"))
		if err != nil {
			return nil, err
		}
		tr := &http.Transport{DialContext: (&net.Dialer{LocalAddr: from, Timeout: 5 * time.Second}).DialContext}
		spoof := &httputil.ReverseProxy{Transport: tr, Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(sb)
			r.Out.Host = o.SpoofHost // claims the public address, and keeps every header the browser sent
			if r.Out.Host == "" {
				r.Out.Host = r.In.Host
			}
			r.Out.Header.Set("X-Forwarded-Proto", "https")
			r.Out.Header.Set("X-Forwarded-For", "100.101.102.103")
		}}
		if err := s.serve(o.SpoofListen, cert, spoof); err != nil {
			s.Close()
			return nil, err
		}
	}
	return s, nil
}

func (s *Server) serve(addr string, cert tls.Certificate, h http.Handler) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 15 * time.Second, TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}}
	s.servers = append(s.servers, srv)
	go func() {
		if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Println("fake serve:", err)
		}
	}()
	return nil
}

// Close stops every listener.
func (s *Server) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, srv := range s.servers {
		_ = srv.Shutdown(ctx)
	}
}

// selfSigned makes a certificate for name and returns its SPKI hash.
func selfSigned(name string) (tls.Certificate, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	sum := sha256.Sum256(pub)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, base64.StdEncoding.EncodeToString(sum[:]), nil
}
