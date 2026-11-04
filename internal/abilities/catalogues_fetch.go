package abilities

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/resources"
)

// Reading what a catalogue names (D482): only over HTTPS; redirects only
// within the same site (at most three); a size and a time limit; never this
// computer or a local network, by the address its name has when MetaRush
// connects; an index asked for again with the ETag its last copy came with.
// Nothing MetaRush reads this way ever runs here: an index is words, an
// ability file is checked and waits for an owner.

// Limits on reading (variables only so tests can shorten them).
var (
	indexTimeout = 30 * time.Second
	fileTimeout  = 5 * time.Minute
	// fileMaxBytes is the largest ability file MetaRush downloads (as large
	// as the dashboard sends).
	fileMaxBytes int64 = 256 << 20
)

const maxRedirects = 3

// EnvCatalogueCA names a file of certificate authorities (PEM) MetaRush also
// trusts when reading catalogues and ability files — only under go test and
// in browser runs (providers.RealServicesRefused), and only for a test server
// on this computer.
const EnvCatalogueCA = "METARUSH_CATALOGUE_CA"

// testMode: under go test and in browser runs, only a server on this
// computer's loopback stands in for the internet; nothing else is read.
func testMode() bool { return providers.RealServicesRefused() }

// testLoopback reports a loopback host MetaRush may read from in tests.
func testLoopback(host string) bool {
	if !testMode() {
		return false
	}
	h := strings.ToLower(strings.Trim(host, "[]"))
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// readFrom is what was read: the body, or that the copy MetaRush has is
// still the one there.
type readFrom struct {
	Body        []byte
	ETag        string
	NotModified bool
}

// errElsewhere is a redirect to another site.
type errElsewhere struct{ host string }

func (e *errElsewhere) Error() string { return "a redirect to " + e.host }

// errLocal is an address on this computer or a local network.
type errLocal struct{ host string }

func (e *errLocal) Error() string { return e.host + " is on this computer or a local network" }

// readHTTPS reads an address under D482's rules. what names it for people
// ("the catalogue", "the ability file").
func readHTTPS(ctx context.Context, what, raw, etag string, max int64, timeout time.Duration) (*readFrom, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	switch {
	case err != nil || u.Host == "" || u.Opaque != "":
		return nil, apperr.New(apperr.InvalidInput, "That isn't a web address for %s.", what)
	case u.Scheme != "https":
		return nil, apperr.New(apperr.InvalidInput, "MetaRush reads %s only over a secure connection: give an address that starts with https://.", what)
	case u.User != nil:
		return nil, apperr.New(apperr.InvalidInput, "Give the address for %s without a sign-in in it.", what)
	case resources.LocalAddress(u.Hostname()) && !testLoopback(u.Hostname()):
		return nil, apperr.New(apperr.InvalidInput, "That address is on this computer or a local network, so MetaRush won't read %s from it.", what)
	case testMode() && !testLoopback(u.Hostname()):
		return nil, apperr.New(apperr.NotConfigured, "Tests never read %s from the internet: serve it on this computer.", what)
	}
	u.Fragment = ""
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "That isn't a web address for %s.", what)
	}
	req.Header.Set("User-Agent", "MetaRush (ability catalogues)")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	client, err := catalogueClient(u)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, readProblem(what, u, err, timeout)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified && etag != "":
		return &readFrom{NotModified: true, ETag: etag}, nil
	case resp.StatusCode != http.StatusOK:
		return nil, apperr.New(apperr.Provider, "%s answered “%d %s”: MetaRush found nothing it can read there.", upperFirst(what)+" ("+u.Host+")", resp.StatusCode, http.StatusText(resp.StatusCode)).
			WithDetails(map[string]any{"status": resp.StatusCode, "url": u.String()})
	case resp.ContentLength > max:
		return nil, apperr.New(apperr.InvalidInput, "%s is larger than MetaRush reads (%s).", upperFirst(what), sizeWords(max))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, readProblem(what, u, err, timeout)
	}
	if int64(len(b)) > max {
		return nil, apperr.New(apperr.InvalidInput, "%s is larger than MetaRush reads (%s).", upperFirst(what), sizeWords(max))
	}
	return &readFrom{Body: b, ETag: cleanETag(resp.Header.Get("ETag"))}, nil
}

// cleanETag keeps an ETag only when it is a short, plain token.
func cleanETag(s string) string {
	if len(s) > 200 || strings.IndexFunc(s, func(r rune) bool { return r < 0x21 || r > 0x7e }) >= 0 {
		return ""
	}
	return s
}

// readProblem says why reading failed, in plain words.
func readProblem(what string, u *url.URL, err error, timeout time.Duration) error {
	var other *errElsewhere
	var local *errLocal
	var ue *url.Error
	switch {
	case errors.As(err, &other):
		e := apperr.New(apperr.InvalidInput, "%s sent MetaRush on to another site (%s); MetaRush follows only within the same site. Give the address it ends at.", upperFirst(what)+" ("+u.Host+")", other.host)
		if strings.EqualFold(u.Hostname(), "github.com") {
			e = e.WithHint("For a file kept on GitHub, give its raw address (on raw.githubusercontent.com).")
		}
		return e
	case errors.As(err, &local):
		return apperr.New(apperr.InvalidInput, "%s is on this computer or a local network, so MetaRush won't read %s from it.", local.host, what)
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ue) && ue.Timeout()):
		return apperr.New(apperr.Provider, "%s didn't answer within %s; try again later.", upperFirst(what)+" ("+u.Host+")", durationWords(timeout))
	case strings.Contains(err.Error(), "certificate") || strings.Contains(err.Error(), "tls:"):
		return apperr.New(apperr.Provider, "MetaRush couldn't make a secure connection to %s, so it read nothing from it.", u.Host).
			WithDetails(map[string]any{"problem": err.Error()})
	}
	return apperr.New(apperr.Provider, "MetaRush couldn't reach %s for %s; try again later.", u.Host, what).WithDetails(map[string]any{"problem": err.Error()})
}

func durationWords(d time.Duration) string {
	if d >= time.Minute {
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	return fmt.Sprintf("%d seconds", int(d.Seconds()))
}

// catalogueClient connects only to public addresses (in tests, loopback) and
// follows redirects only within the same site, over HTTPS.
func catalogueClient(start *url.URL) (*http.Client, error) {
	tlsConf := &tls.Config{MinVersion: tls.VersionTLS12}
	if testMode() {
		if p := strings.TrimSpace(os.Getenv(EnvCatalogueCA)); p != "" {
			pem, err := os.ReadFile(p)
			if err != nil {
				return nil, fmt.Errorf("the test certificate authority: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, errors.New("the test certificate authority holds no certificate")
			}
			tlsConf.RootCAs = pool
		}
	}
	d := &net.Dialer{Timeout: 15 * time.Second}
	tr := &http.Transport{Proxy: nil, TLSClientConfig: tlsConf, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
		MaxResponseHeaderBytes: 64 << 10, ForceAttemptHTTP2: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("%s has no address", host)
			}
			for _, ip := range ips {
				if resources.LocalIP(ip.IP) && !(testMode() && ip.IP.IsLoopback()) {
					return nil, &errLocal{host: host}
				}
			}
			return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		}}
	return &http.Client{Transport: tr, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		switch {
		case len(via) > maxRedirects:
			return errors.New("too many redirects")
		case r.URL.Scheme != "https" || !strings.EqualFold(r.URL.Host, start.Host):
			return &errElsewhere{host: printableHost(r.URL.Host)}
		}
		return nil
	}}, nil
}

// printableHost keeps a host's name readable in a message.
func printableHost(h string) string {
	h = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) && !unicode.IsSpace(r) {
			return r
		}
		return -1
	}, h)
	if len(h) > 100 {
		h = h[:100]
	}
	return h
}
