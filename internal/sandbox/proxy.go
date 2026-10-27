package sandbox

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Proxy is MetaRush's proxy for one run of an ability (D232): the only way a
// sandboxed program reaches the network. It forwards HTTP and HTTPS only to
// the hosts the run declared (HTTPS is opened here with a certificate made
// for this run, so every request can be checked and recorded), puts each
// real key where the program put its stand-in — only on requests to the
// hosts that key is for, only over HTTPS — so the program never sees a key,
// takes keys back out of what comes back, refuses everything else (other
// hosts, this computer and local networks, plain HTTP with a key) and
// records every request without its keys or query values.
//
// Programs reach it in three ways: as an HTTP proxy (HTTPS_PROXY and
// friends; CONNECT, then TLS with the run's certificate), with absolute
// http:// requests, or — Node.js's fetch, which ignores proxy settings —
// through the preload MetaRush gives Node, which rewrites fetch's addresses
// to /__metarush/<scheme>/<host>/<path> on the proxy itself.
type Proxy struct {
	cfg       ProxyConfig
	ln        net.Listener
	unix      net.Listener
	srv       *http.Server
	caKey     *ecdsa.PrivateKey
	ca        *x509.Certificate
	caPEM     []byte
	leafMu    sync.Mutex
	leaves    map[string]*tls.Certificate
	transport *http.Transport
	logMu     sync.Mutex
	requests  []ProxyRequest
	dropped   int
	received  atomic.Int64
	sent      atomic.Int64
	count     atomic.Int64
	// localCount counts requests to the local service (D284): they never
	// leave this computer, so a words-only run's budget doesn't take them.
	localCount atomic.Int64
	localOnce  sync.Once
	port       int
}

// ProxyKey is one key a run may use.
type ProxyKey struct {
	Name    string   // its name in Settings → Integrations
	Value   string   // the real key: never logged, never returned
	StandIn string   // what the program gets in its environment instead
	Hosts   []string // the hosts it may go to (patterns as in Hosts)
	// Header is the request header the key goes in (Authorization when
	// neither is given); Query the query parameter instead. The stand-in
	// anywhere else in a request — another header, the address, the body —
	// is refused, so the code can't steer the key somewhere a host would
	// echo it.
	Header string
	Query  string
}

// header is the header a key goes in ("" when it goes in the address).
func (k ProxyKey) header() string {
	if k.Query != "" {
		return ""
	}
	if k.Header == "" {
		return "Authorization"
	}
	return http.CanonicalHeaderKey(k.Header)
}

// place says where a key goes, in plain words.
func (k ProxyKey) place() string {
	if k.Query != "" {
		return "the address's " + k.Query + " part"
	}
	return "the " + k.header() + " header"
}

// ProxyConfig is what one run may reach.
type ProxyConfig struct {
	// Hosts the run declared: exact names, or *.example.com for any name
	// under example.com (not example.com itself). Port 443 for HTTPS and 80
	// for HTTP, unless a host says host:port.
	Hosts []string
	// AnyHost lets the run reach any public host on ports 80 and 443 (an
	// open network, D235); it never takes keys.
	AnyHost bool
	// LocalPort lets the run reach one service on this computer (D284):
	// plain HTTP to 127.0.0.1, localhost or [::1] on this port, request by
	// request (never a raw connection, never HTTPS, never a key), and
	// nothing else on this computer or a local network. 0: none.
	LocalPort int
	// LocalCheck says why the local service may not be reached now ("" when
	// it may): asked before every request to it, so a different program on
	// the port is never reached; LocalChanged hears it once (the run is
	// broken off).
	LocalCheck func() string
	// LocalCheckNow asks without any memory: before every new connection to
	// the local service (a different program there always means a new
	// connection), so a program swapped onto the port gets nothing.
	LocalCheckNow func() string
	LocalChanged  func(why string)
	Keys          []ProxyKey
	// Token is the run's proxy password (in the program's proxy address);
	// "" lets any local program use the proxy while the run lasts.
	Token string
	// OnRequest hears about every request (keys and query values left out).
	OnRequest func(ProxyRequest)
	// Refuse says why a host the run may otherwise reach is refused anyway
	// ("" not refused): a client's Content Settings that let their things go
	// only to some hosts (What may leave, D290). Asked for every request.
	Refuse func(host string) string
	// MaxBytes is how much may come back in all (0: 4 GB); MaxRequests how
	// many requests a run may make (0: 10,000).
	MaxBytes    int64
	MaxRequests int64
	// MaxSend is the most a run may send out in all — request lines,
	// headers and bodies (0: no limit): an ability that says it sends only
	// words gets a small one (D234).
	MaxSend int64
	// Test seams: Dial replaces dialing the hosts (loopback test servers;
	// it also lifts the refusal of this computer's own addresses) and
	// RootCAs replaces the system's certificates for them.
	Dial    func(ctx context.Context, network, addr string) (net.Conn, error)
	RootCAs *x509.CertPool
}

// ProxyRequest is one request as the run's record keeps it.
type ProxyRequest struct {
	At       time.Time `json:"at" bson:"at"`
	Method   string    `json:"method" bson:"method"`
	Host     string    `json:"host" bson:"host"`
	Port     int       `json:"port,omitempty" bson:"port,omitempty" jsonschema:"description=The port it went to (443 for HTTPS and 80 for HTTP unless the address said another)."`
	Path     string    `json:"path" bson:"path" jsonschema:"description=The path without its query (query values are never kept)."`
	Query    []string  `json:"query,omitempty" bson:"query,omitempty" jsonschema:"description=The names of the query's parameters."`
	Status   int       `json:"status,omitempty" bson:"status,omitempty"`
	Sent     int64     `json:"sent" bson:"sent"`
	Received int64     `json:"received" bson:"received"`
	Millis   int64     `json:"ms" bson:"ms"`
	Keys     []string  `json:"keys,omitempty" bson:"keys,omitempty" jsonschema:"description=The keys MetaRush put in (names only)."`
	Refused  string    `json:"refused,omitempty" bson:"refused,omitempty" jsonschema:"description=Why MetaRush refused it\\, in plain words."`
}

const (
	maxKeptRequests  = 2000
	defaultMaxBytes  = 4 << 30
	defaultMaxReqs   = 10000
	maxBodyToReplace = 1 << 20
	// maxHeaderBytes caps one request's headers (Go's server adds a little).
	maxHeaderBytes = 32 << 10
	// EncodedPrefix is where the Node.js preload sends fetch's requests.
	EncodedPrefix = "/__metarush/"
	// TokenHeader carries the run's token on those requests.
	TokenHeader = "X-Metarush-Proxy"
)

// StartProxy starts a proxy on a loopback port of its own.
func StartProxy(cfg ProxyConfig) (*Proxy, error) {
	for i, h := range cfg.Hosts {
		cfg.Hosts[i] = strings.ToLower(strings.TrimSpace(h))
	}
	p := &Proxy{cfg: cfg, leaves: map[string]*tls.Certificate{}}
	if err := p.makeCA(); err != nil {
		return nil, err
	}
	p.transport = &http.Transport{
		DialContext:           p.dial,
		TLSClientConfig:       &tls.Config{RootCAs: cfg.RootCAs, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		ExpectContinueTimeout: time.Second,
		DisableCompression:    true,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p.ln = ln
	p.port = ln.Addr().(*net.TCPAddr).Port
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 30 * time.Second, ErrorLog: quietLog(), MaxHeaderBytes: maxHeaderBytes}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// ListenUnix also serves on a socket file (the Linux forwarder's way in).
func (p *Proxy) ListenUnix(path string) error {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	p.unix = ln
	go func() { _ = p.srv.Serve(ln) }()
	return nil
}

// Port is the loopback port it listens on.
func (p *Proxy) Port() int { return p.port }

// URL is the proxy's address for the program's environment, with the run's
// token as its password.
func (p *Proxy) URL() string {
	u := url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(p.port)}
	if p.cfg.Token != "" {
		u.User = url.UserPassword("metarush", p.cfg.Token)
	}
	return u.String()
}

// CAPEM is the run's certificate authority (public part), which the program
// trusts for the hosts it reaches through the proxy.
func (p *Proxy) CAPEM() []byte { return append([]byte(nil), p.caPEM...) }

// Close stops it.
func (p *Proxy) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := p.srv.Shutdown(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		err = p.srv.Close()
	}
	p.transport.CloseIdleConnections()
	return err
}

// Requests are the requests so far (at most 2,000 kept) and how many more
// were left out.
func (p *Proxy) Requests() ([]ProxyRequest, int) {
	p.logMu.Lock()
	defer p.logMu.Unlock()
	return append([]ProxyRequest(nil), p.requests...), p.dropped
}

// Received is how many bytes came back in all.
func (p *Proxy) Received() int64 { return p.received.Load() }

func (p *Proxy) record(r ProxyRequest) {
	p.logMu.Lock()
	if len(p.requests) < maxKeptRequests {
		p.requests = append(p.requests, r)
	} else {
		p.dropped++
	}
	p.logMu.Unlock()
	if p.cfg.OnRequest != nil {
		p.cfg.OnRequest(r)
	}
}

// ---- the certificate authority of one run ----

func (p *Proxy) makeCA() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "MetaRush ability proxy (one run)", Organization: []string{"MetaRush"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(48 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	p.caKey, p.ca = key, ca
	p.caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return nil
}

// leaf is the certificate the proxy shows the program for one host.
func (p *Proxy) leaf(host string) (*tls.Certificate, error) {
	host = strings.ToLower(host)
	p.leafMu.Lock()
	defer p.leafMu.Unlock()
	if c, ok := p.leaves[host]; ok {
		return c, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(47 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tpl.IPAddresses = []net.IP{ip}
	} else {
		tpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, p.ca, &key.PublicKey, p.caKey)
	if err != nil {
		return nil, err
	}
	c := &tls.Certificate{Certificate: [][]byte{der, p.ca.Raw}, PrivateKey: key}
	p.leaves[host] = c
	return c, nil
}

// ---- what may be reached ----

// hostAllowed reports whether a host (and port) is one the run declared.
func (p *Proxy) hostAllowed(host, port, scheme string) bool {
	if p.localTarget(host, port, scheme) {
		return true
	}
	if p.cfg.AnyHost && len(p.cfg.Keys) == 0 && (port == "443" || port == "80") {
		return true
	}
	return hostMatches(p.cfg.Hosts, host, port, scheme)
}

// localTarget reports the one service on this computer the run may reach
// (D284): plain HTTP to a loopback name on LocalPort.
func (p *Proxy) localTarget(host, port, scheme string) bool {
	return p.cfg.LocalPort > 0 && scheme == "http" && port == strconv.Itoa(p.cfg.LocalPort) && loopbackName(host)
}

// ThisComputer reports a host that is this computer's own name or address:
// a request there didn't leave it.
func ThisComputer(host string) bool { return loopbackName(host) }

// loopbackName is this computer's own name or address as a program writes it.
func loopbackName(host string) bool {
	switch strings.ToLower(strings.Trim(strings.TrimSuffix(host, "."), "[]")) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

func hostMatches(patterns []string, host, port, scheme string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	def := "443"
	if scheme == "http" {
		def = "80"
	}
	for _, pat := range patterns {
		ph, pp := pat, def
		if h, p, err := net.SplitHostPort(pat); err == nil {
			ph, pp = h, p
		}
		if pp != port {
			continue
		}
		if strings.HasPrefix(ph, "*.") {
			if strings.HasSuffix(host, ph[1:]) && len(host) > len(ph)-1 {
				return true
			}
			continue
		}
		if host == ph {
			return true
		}
	}
	return false
}

// publicIP reports an address on the internet: not this computer, a local
// network, link-local or a special range.
func publicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 0, v4[0] == 100 && v4[1]&0xc0 == 64, v4[0] == 192 && v4[1] == 0 && v4[2] == 0, v4[0] == 198 && (v4[1] == 18 || v4[1] == 19), v4[0] >= 240:
			return false
		}
		return true
	}
	// IPv6: IPv4-mapped and -translated forms of the ranges above, unique local.
	if len(ip) == net.IPv6len && ip[0] == 0x00 && ip[1] == 0x64 && ip[2] == 0xff && ip[3] == 0x9b {
		return publicIP(net.IP(ip[12:16]))
	}
	return true
}

var errLocal = errors.New("that host is on this computer or a local network")

// dial connects to a declared host's public address.
func (p *Proxy) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if h, port, err := net.SplitHostPort(addr); err == nil && p.cfg.LocalPort > 0 && port == strconv.Itoa(p.cfg.LocalPort) && loopbackName(h) && p.cfg.LocalCheckNow != nil {
		if why := p.cfg.LocalCheckNow(); why != "" {
			if p.cfg.LocalChanged != nil {
				p.localOnce.Do(func() { p.cfg.LocalChanged(why) })
			}
			return nil, errors.New(why)
		}
	}
	if p.cfg.Dial != nil {
		return p.cfg.Dial(ctx, network, addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if p.cfg.LocalPort > 0 && port == strconv.Itoa(p.cfg.LocalPort) && loopbackName(host) {
		// The one service on this computer an owner agreed to (D284): its
		// loopback address, never a name looked up.
		d := net.Dialer{Timeout: 15 * time.Second}
		if strings.Contains(host, ":") {
			return d.DialContext(ctx, network, net.JoinHostPort("::1", port))
		}
		return d.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 15 * time.Second}
	var last error = errLocal
	for _, ip := range ips {
		if !publicIP(ip.IP) {
			continue
		}
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

// ---- serving ----

func (p *Proxy) authorized(r *http.Request) bool {
	if p.cfg.Token == "" {
		return true
	}
	h := r.Header.Get("Proxy-Authorization")
	scheme, cred, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Basic") {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cred))
	if err != nil {
		return false
	}
	_, pass, _ := strings.Cut(string(raw), ":")
	return subtle.ConstantTimeCompare([]byte(pass), []byte(p.cfg.Token)) == 1
}

func (p *Proxy) tokenOK(v string) bool {
	return p.cfg.Token == "" || subtle.ConstantTimeCompare([]byte(v), []byte(p.cfg.Token)) == 1
}

func plainRefusal(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Metarush-Refused", "1")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, msg+"\n")
}

// ServeHTTP answers the program.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodConnect:
		if !p.authorized(r) {
			w.Header().Set("Proxy-Authenticate", `Basic realm="MetaRush"`)
			plainRefusal(w, http.StatusProxyAuthRequired, "MetaRush's proxy for this run needs the address it was given.")
			return
		}
		ch, cp, _ := net.SplitHostPort(withPort(r.Host, "443"))
		if !p.localTarget(ch, cp, "http") && !p.spend(requestBytes(r)) {
			p.record(ProxyRequest{At: time.Now().UTC(), Method: "CONNECT", Host: hostOf(r.Host), Port: portOf(withPort(r.Host, "443")), Refused: p.overBudget()})
			plainRefusal(w, http.StatusRequestEntityTooLarge, p.overBudget())
			return
		}
		p.connect(w, r)
	case !r.URL.IsAbs() && strings.HasPrefix(r.URL.Path, EncodedPrefix):
		if !p.tokenOK(r.Header.Get(TokenHeader)) {
			plainRefusal(w, http.StatusProxyAuthRequired, "MetaRush's proxy for this run needs the address it was given.")
			return
		}
		r.Header.Del(TokenHeader)
		scheme, hostport, rest, ok := decodeTarget(r.URL)
		if !ok {
			plainRefusal(w, http.StatusBadRequest, "MetaRush's proxy didn't understand where this request should go.")
			return
		}
		p.forward(w, r, scheme, hostport, rest, true)
	case r.URL.IsAbs():
		if !p.authorized(r) {
			w.Header().Set("Proxy-Authenticate", `Basic realm="MetaRush"`)
			plainRefusal(w, http.StatusProxyAuthRequired, "MetaRush's proxy for this run needs the address it was given.")
			return
		}
		if r.URL.Scheme != "http" {
			plainRefusal(w, http.StatusBadRequest, "Send HTTPS requests through the proxy with CONNECT.")
			return
		}
		p.forward(w, r, "http", withPort(r.URL.Host, "80"), r.URL.RequestURI(), false)
	default:
		plainRefusal(w, http.StatusBadRequest, "This is MetaRush's proxy: it forwards an ability's requests to the hosts it declared, nothing else.")
	}
}

// decodeTarget reads /__metarush/<scheme>/<host[:port]>/<rest>.
func decodeTarget(u *url.URL) (scheme, hostport, rest string, ok bool) {
	p := strings.TrimPrefix(u.EscapedPath(), EncodedPrefix)
	parts := strings.SplitN(p, "/", 3)
	if len(parts) < 2 || (parts[0] != "http" && parts[0] != "https") || parts[1] == "" {
		return "", "", "", false
	}
	scheme = parts[0]
	host, err := url.PathUnescape(parts[1])
	if err != nil || strings.ContainsAny(host, "/@?#") {
		return "", "", "", false
	}
	def := "443"
	if scheme == "http" {
		def = "80"
	}
	rest = "/"
	if len(parts) == 3 {
		rest = "/" + parts[2]
	}
	if u.RawQuery != "" {
		rest += "?" + u.RawQuery
	}
	return scheme, withPort(host, def), rest, true
}

// EncodeTarget is where a request for target goes on the proxy (the Node.js
// preload's form; tests use it too).
func EncodeTarget(target *url.URL) string {
	host := target.Host
	return EncodedPrefix + target.Scheme + "/" + url.PathEscape(host) + target.EscapedPath() + func() string {
		if target.RawQuery != "" {
			return "?" + target.RawQuery
		}
		return ""
	}()
}

func withPort(host, def string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), def)
}

// connect opens a CONNECT tunnel: TLS with the run's certificate for the
// host (or plain HTTP), then every request in it is forwarded.
func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	hostport := withPort(r.Host, "443")
	host, port, _ := net.SplitHostPort(hostport)
	rec := ProxyRequest{At: time.Now().UTC(), Method: "CONNECT", Host: host, Port: portOf(hostport)}
	if !p.hostAllowed(host, port, "https") && !p.hostAllowed(host, port, "http") {
		rec.Refused = refusedHost(host)
		p.record(rec)
		plainRefusal(w, http.StatusForbidden, rec.Refused)
		return
	}
	if why := p.refusedUnlessHere(host, port, "http"); why != "" {
		rec.Refused = why
		p.record(rec)
		plainRefusal(w, http.StatusForbidden, why)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		plainRefusal(w, http.StatusInternalServerError, "MetaRush's proxy couldn't open the connection.")
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
	br := buf.Reader
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	first, err := br.Peek(1)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		conn.Close()
		return
	}
	inner := &peekedConn{Conn: conn, r: br}
	if first[0] == 0x16 { // a TLS handshake
		tc := tls.Server(inner, &tls.Config{
			NextProtos: []string{"http/1.1"},
			MinVersion: tls.VersionTLS12,
			GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
				name := hello.ServerName
				if name != "" && !strings.EqualFold(strings.TrimSuffix(name, "."), host) {
					return nil, fmt.Errorf("the program asked for %s inside a connection to %s", name, host)
				}
				return p.leaf(host)
			},
		})
		p.serveTunnel(tc, "https", hostport)
		return
	}
	p.serveTunnel(inner, "http", hostport)
}

// serveTunnel serves the requests inside one tunnel.
func (p *Proxy) serveTunnel(c net.Conn, scheme, hostport string) {
	host, _, _ := net.SplitHostPort(hostport)
	ln := &oneConn{c: c, done: make(chan struct{})}
	srv := &http.Server{ReadHeaderTimeout: 30 * time.Second, ErrorLog: quietLog(), MaxHeaderBytes: maxHeaderBytes, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The request must be for the host the tunnel was opened to.
		if rh := hostOf(r.Host); rh != "" && !strings.EqualFold(rh, host) {
			msg := fmt.Sprintf("This request names %s inside a connection to %s; MetaRush refuses that.", rh, host)
			p.record(ProxyRequest{At: time.Now().UTC(), Method: r.Method, Host: rh, Port: portOf(hostport), Path: r.URL.Path, Refused: msg})
			plainRefusal(w, http.StatusForbidden, msg)
			return
		}
		p.forward(w, r, scheme, hostport, r.URL.RequestURI(), false)
	})}
	srv.SetKeepAlivesEnabled(true)
	_ = srv.Serve(ln)
}

// portOf is host:port's port (0 when it has none).
func portOf(hostport string) int {
	_, p, err := net.SplitHostPort(hostport)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}

func hostOf(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.Trim(h, "[]")
	}
	return strings.Trim(hostport, "[]")
}

// refused asks the run's Refuse (D290).
func (p *Proxy) refused(host string) string {
	if p.cfg.Refuse == nil {
		return ""
	}
	return p.cfg.Refuse(strings.ToLower(strings.TrimSuffix(host, ".")))
}

// refusedUnlessHere asks the run's Refuse about every host but the one
// service on this computer (D284): what goes there doesn't leave it, so a
// client's rules about what may leave (D290) have nothing to refuse.
func (p *Proxy) refusedUnlessHere(host, port, scheme string) string {
	if p.localTarget(host, port, scheme) {
		return ""
	}
	return p.refused(host)
}

func refusedHost(host string) string {
	return fmt.Sprintf("%s isn't one of the hosts this ability declared, so MetaRush didn't send the request.", host)
}

// hopHeaders are never passed on.
var hopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

// forward sends one request to the host and the answer back.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, scheme, hostport, uri string, encoded bool) {
	start := time.Now()
	host, port, _ := net.SplitHostPort(hostport)
	host = strings.ToLower(host)
	rec := ProxyRequest{At: start.UTC(), Method: r.Method, Host: host, Port: portOf(hostport)}
	if u, err := url.ParseRequestURI(uri); err == nil {
		rec.Path = u.Path
		for k := range u.Query() {
			rec.Query = append(rec.Query, k)
		}
	}
	defer func() {
		rec.Millis = time.Since(start).Milliseconds()
		p.record(rec)
	}()
	refuse := func(code int, msg string) {
		rec.Refused, rec.Status = msg, code
		plainRefusal(w, code, msg)
	}
	// The one service on this computer (D284) has a count of its own and
	// spends nothing of a words-only run's budget: nothing sent there leaves
	// this computer.
	local := p.localTarget(host, port, scheme)
	if local {
		if n := p.localCount.Add(1); n > defaultMaxReqs {
			refuse(http.StatusTooManyRequests, fmt.Sprintf("This run made more than %d requests to %s, the most MetaRush passes on for one run.", defaultMaxReqs, host))
			return
		}
	} else if n := p.count.Add(1); n > p.maxRequests() {
		refuse(http.StatusTooManyRequests, fmt.Sprintf("This run made more than %d requests, the most MetaRush sends for one run.", p.maxRequests()))
		return
	}
	if why := p.refusedUnlessHere(host, port, scheme); why != "" {
		refuse(http.StatusForbidden, why)
		return
	}
	if !p.hostAllowed(host, port, scheme) {
		refuse(http.StatusForbidden, refusedHost(host))
		return
	}
	if local && p.cfg.LocalCheck != nil {
		if why := p.cfg.LocalCheck(); why != "" {
			refuse(http.StatusForbidden, why)
			if p.cfg.LocalChanged != nil {
				p.localOnce.Do(func() { p.cfg.LocalChanged(why) })
			}
			return
		}
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		refuse(http.StatusNotImplemented, "MetaRush doesn't carry WebSocket connections for an ability yet.")
		return
	}
	target := scheme + "://" + hostport + uri
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		target = scheme + "://" + host + uri
	}
	// The keys: stand-ins become keys only for their hosts, only over HTTPS.
	out := r.Header.Clone()
	for _, h := range hopHeaders {
		out.Del(h)
	}
	if c := r.Header.Get("Connection"); c != "" {
		for _, f := range strings.Split(c, ",") {
			out.Del(strings.TrimSpace(f))
		}
	}
	out.Del(TokenHeader)
	out.Set("Accept-Encoding", "identity") // answers come back readable, so keys can be taken out
	// What it sends counts toward the run's budget: the request line and
	// headers now, the body as it goes.
	if !local && (!p.spend(requestBytes(r)) || (r.ContentLength > 0 && !p.spend(r.ContentLength))) {
		refuse(http.StatusRequestEntityTooLarge, p.overBudget())
		return
	}
	var body io.Reader = r.Body
	length := r.ContentLength
	var small []byte // a small text body: where a stand-in must not be
	if length > 0 && length <= maxBodyToReplace && textBody(r.Header.Get("Content-Type")) {
		b, err := io.ReadAll(io.LimitReader(r.Body, maxBodyToReplace+1))
		if err != nil {
			refuse(http.StatusBadRequest, "MetaRush's proxy couldn't read the request.")
			return
		}
		small = b
	}
	target, out, used, why := p.putKeys(target, out, small, host, port, scheme)
	if why != "" {
		refuse(http.StatusForbidden, why)
		return
	}
	rec.Keys = used
	switch {
	case small != nil:
		body, length = bytes.NewReader(small), int64(len(small))
	case r.ContentLength == 0:
		body = nil
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, body)
	if err != nil {
		refuse(http.StatusBadRequest, "MetaRush's proxy couldn't make sense of the request's address.")
		return
	}
	req.Header = out
	req.ContentLength = length
	var bodyReader io.Reader = req.Body
	unknown := p.cfg.MaxSend > 0 && req.Body != nil && length < 0 && !local
	if unknown {
		bodyReader = &capReader{r: req.Body, left: p.cfg.MaxSend - p.sent.Load()}
	}
	cr := &countingReader{r: bodyReader}
	if req.Body != nil {
		req.Body = io.NopCloser(cr)
	}
	resp, err := p.transport.RoundTrip(req)
	rec.Sent = cr.n
	if unknown {
		p.sent.Add(cr.n)
	}
	if err != nil {
		code, msg := http.StatusBadGateway, fmt.Sprintf("MetaRush couldn't reach %s: %s", host, plainNetError(err))
		switch {
		case errors.Is(err, errLocal):
			code, msg = http.StatusForbidden, fmt.Sprintf("%s points at this computer or a local network, which an ability never reaches.", host)
		case errors.Is(err, errTooMuch):
			code, msg = http.StatusRequestEntityTooLarge, p.overBudget()
		}
		refuse(code, msg)
		return
	}
	defer resp.Body.Close()
	for _, h := range hopHeaders {
		resp.Header.Del(h)
	}
	for k, vs := range resp.Header {
		for i, v := range vs {
			v = p.takeKeysOut(v)
			if encoded && strings.EqualFold(k, "Location") {
				v = p.encodeLocation(v, scheme, hostport)
			}
			vs[i] = v
		}
		w.Header()[k] = vs
	}
	var src io.Reader = resp.Body
	if len(p.cfg.Keys) > 0 && r.Method != http.MethodHead {
		// Every answer of a run with keys is read as text before the program
		// sees it: compressed ones are opened (or refused), keys in any of
		// their written forms taken out, and a key taken out changes the
		// length: the answer is sent in chunks.
		plain, err := decoded(resp)
		if err != nil {
			w.Header().Del("Content-Encoding")
			w.Header().Del("Content-Length")
			refuse(http.StatusBadGateway, fmt.Sprintf("%s answered in a compressed form MetaRush can't read, so it didn't pass the answer on.", host))
			return
		}
		src = p.keyFilter(plain)
		w.Header().Del("Content-Encoding")
		w.Header().Del("Content-Length")
	}
	w.WriteHeader(resp.StatusCode)
	rec.Status = resp.StatusCode
	n, err := copyLimited(w, src, p.maxBytes()-p.received.Load())
	p.received.Add(n)
	rec.Received = n
	if errors.Is(err, errTooMuch) {
		rec.Refused = fmt.Sprintf("This run received more than %s, the most MetaRush lets in for one run; the rest was cut off.", sizeWords(p.maxBytes()))
		// Break the answer off, so the program sees it fail instead of a
		// shorter file that looks whole (the deferred record still runs).
		panic(http.ErrAbortHandler)
	}
}

func (p *Proxy) maxBytes() int64 {
	if p.cfg.MaxBytes > 0 {
		return p.cfg.MaxBytes
	}
	return defaultMaxBytes
}

func (p *Proxy) maxRequests() int64 {
	if p.cfg.MaxRequests > 0 {
		return p.cfg.MaxRequests
	}
	return defaultMaxReqs
}

func textBody(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return ct == ""
	}
	return strings.HasPrefix(mt, "text/") || strings.HasSuffix(mt, "json") || strings.HasSuffix(mt, "+json") || mt == "application/x-www-form-urlencoded" ||
		strings.HasSuffix(mt, "xml")
}

// putKeys puts each key in its one place — its header, or its query
// parameter — on requests to its hosts over HTTPS. A stand-in anywhere else
// in the request (another header, the path, another parameter, a readable
// body) is refused, so the key can't be steered somewhere a host would
// repeat it.
func (p *Proxy) putKeys(target string, h http.Header, body []byte, host, port, scheme string) (string, http.Header, []string, string) {
	u, err := url.Parse(target)
	if err != nil {
		return target, h, nil, "MetaRush's proxy couldn't make sense of the request's address."
	}
	var used []string
	for _, k := range p.cfg.Keys {
		if k.StandIn == "" || k.Value == "" {
			continue
		}
		inPlace, elsewhere := false, ""
		if strings.Contains(u.EscapedPath(), k.StandIn) || strings.Contains(u.Path, k.StandIn) || strings.Contains(u.Fragment, k.StandIn) {
			elsewhere = "the address"
		}
		segments := []string{}
		if u.RawQuery != "" {
			segments = strings.Split(u.RawQuery, "&")
		}
		for _, seg := range segments {
			name, val, _ := strings.Cut(seg, "=")
			un, _ := url.QueryUnescape(name)
			uv, _ := url.QueryUnescape(val)
			switch {
			case strings.Contains(name, k.StandIn) || strings.Contains(un, k.StandIn):
				elsewhere = "the address"
			case strings.Contains(val, k.StandIn) || strings.Contains(uv, k.StandIn):
				if k.Query != "" && un == k.Query {
					inPlace = true
				} else {
					elsewhere = "the address"
				}
			}
		}
		for name, vs := range h {
			for _, v := range vs {
				if !strings.Contains(v, k.StandIn) && !strings.Contains(name, k.StandIn) {
					continue
				}
				if hn := k.header(); hn != "" && http.CanonicalHeaderKey(name) == hn {
					inPlace = true
				} else {
					elsewhere = "the " + name + " header"
				}
			}
		}
		if bytes.Contains(body, []byte(k.StandIn)) {
			elsewhere = "the request's body"
		}
		if elsewhere != "" {
			return target, h, nil, fmt.Sprintf("The %s key goes only in %s, so MetaRush didn't send a request carrying it in %s.", k.Name, k.place(), elsewhere)
		}
		if !inPlace {
			continue
		}
		if scheme != "https" {
			return target, h, nil, fmt.Sprintf("The %s key goes only over HTTPS, so MetaRush didn't send this plain HTTP request.", k.Name)
		}
		if !hostMatches(k.Hosts, host, port, scheme) {
			return target, h, nil, fmt.Sprintf("The %s key isn't for %s, so MetaRush didn't send the request.", k.Name, host)
		}
		if k.Query != "" {
			for i, seg := range segments {
				name, val, _ := strings.Cut(seg, "=")
				if un, _ := url.QueryUnescape(name); un == k.Query {
					uv, _ := url.QueryUnescape(val)
					segments[i] = name + "=" + url.QueryEscape(strings.ReplaceAll(uv, k.StandIn, k.Value))
				}
			}
			u.RawQuery = strings.Join(segments, "&")
		} else {
			hn := k.header()
			for name, vs := range h {
				if http.CanonicalHeaderKey(name) == hn {
					for i, v := range vs {
						vs[i] = strings.ReplaceAll(v, k.StandIn, k.Value)
					}
				}
			}
		}
		used = append(used, k.Name)
	}
	return u.String(), h, used, ""
}

// KeyForms are the ways a key may come back written: as it is, escaped for
// an address, escaped for JSON, and in base64. Each is taken out of answers,
// logs and what a run brings back.
func KeyForms(key string) []string {
	forms := []string{key, url.QueryEscape(key), url.PathEscape(key)}
	if b, err := json.Marshal(key); err == nil && len(b) >= 2 {
		j := string(b[1 : len(b)-1])
		forms = append(forms, j, strings.ReplaceAll(j, "/", `\/`))
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		forms = append(forms, enc.EncodeToString([]byte(key)))
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range forms {
		if len(f) >= 8 && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// keyPairs are every key's written forms and the stand-in each becomes.
func (p *Proxy) keyPairs() ([][2][]byte, int) {
	var pairs [][2][]byte
	longest := 0
	for _, k := range p.cfg.Keys {
		if k.Value == "" {
			continue
		}
		for _, f := range KeyForms(k.Value) {
			pairs = append(pairs, [2][]byte{[]byte(f), []byte(k.StandIn)})
			longest = max(longest, len(f))
		}
	}
	// Longer forms first, so a form inside another is taken out whole.
	sort.SliceStable(pairs, func(i, j int) bool { return len(pairs[i][0]) > len(pairs[j][0]) })
	return pairs, longest
}

// takeKeysOut puts stand-ins back where a host repeated a key (headers).
func (p *Proxy) takeKeysOut(s string) string {
	pairs, _ := p.keyPairs()
	for _, pr := range pairs {
		s = strings.ReplaceAll(s, string(pr[0]), string(pr[1]))
	}
	return s
}

// keyFilter takes keys out of what comes back, as it streams.
func (p *Proxy) keyFilter(r io.Reader) io.Reader {
	pairs, longest := p.keyPairs()
	if len(pairs) == 0 {
		return r
	}
	return &replacer{r: r, pairs: pairs, keep: longest - 1}
}

// decoded is an answer's body as plain bytes: gzip and deflate opened,
// anything else compressed refused.
func decoded(resp *http.Response) (io.Reader, error) {
	switch enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))); enc {
	case "", "identity":
		return resp.Body, nil
	case "gzip", "x-gzip":
		return gzip.NewReader(resp.Body)
	case "deflate":
		br := bufio.NewReader(resp.Body)
		if head, err := br.Peek(2); err == nil && head[0]&0x0f == 8 && (uint16(head[0])<<8|uint16(head[1]))%31 == 0 {
			return zlib.NewReader(br)
		}
		return flate.NewReader(br), nil
	default:
		return nil, fmt.Errorf("content encoding %q", enc)
	}
}

// requestBytes is what a request's line and headers send.
func requestBytes(r *http.Request) int64 {
	n := int64(len(r.Method) + len(r.RequestURI) + len(r.Proto) + 4 + len(r.Host) + 8)
	for k, vs := range r.Header {
		for _, v := range vs {
			n += int64(len(k) + len(v) + 4)
		}
	}
	return n + 2
}

// spend takes n bytes from the run's outbound budget.
func (p *Proxy) spend(n int64) bool {
	if p.cfg.MaxSend <= 0 {
		return true
	}
	return p.sent.Add(n) <= p.cfg.MaxSend
}

func (p *Proxy) overBudget() string {
	return fmt.Sprintf("This ability says it sends only words, so MetaRush stopped it sending more: a run may send at most %s in all.", sizeWords(p.cfg.MaxSend))
}

// encodeLocation keeps a redirect on the proxy (the Node.js preload's form).
func (p *Proxy) encodeLocation(loc, scheme, hostport string) string {
	base, _ := url.Parse(scheme + "://" + hostport + "/")
	u, err := base.Parse(loc)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return loc
	}
	return "http://127.0.0.1:" + strconv.Itoa(p.port) + EncodeTarget(u)
}

// ---- helpers ----

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(b []byte) (int, error) {
	if c.r == nil {
		return 0, io.EOF
	}
	n, err := c.r.Read(b)
	c.n += int64(n)
	return n, err
}

var errTooMuch = errors.New("too much")

// capReader stops a request body of unknown length past its limit.
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(b []byte) (int, error) {
	if c.left <= 0 {
		return 0, errTooMuch
	}
	if int64(len(b)) > c.left {
		b = b[:c.left]
	}
	n, err := c.r.Read(b)
	c.left -= int64(n)
	return n, err
}

func copyLimited(w http.ResponseWriter, r io.Reader, left int64) (int64, error) {
	buf := make([]byte, 32<<10)
	var n int64
	fl, _ := w.(http.Flusher)
	for {
		m, err := r.Read(buf)
		if m > 0 {
			if int64(m) > left-n {
				return n, errTooMuch
			}
			if _, werr := w.Write(buf[:m]); werr != nil {
				return n, werr
			}
			n += int64(m)
			if fl != nil {
				fl.Flush()
			}
		}
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}

// replacer replaces byte strings in a stream (keys split across reads too).
type replacer struct {
	r       io.Reader
	pairs   [][2][]byte
	keep    int
	pending []byte
	out     []byte
	eof     bool
}

func (x *replacer) Read(b []byte) (int, error) {
	for len(x.out) == 0 {
		if x.eof {
			if len(x.pending) > 0 {
				x.out, x.pending = x.apply(x.pending), nil
				continue
			}
			return 0, io.EOF
		}
		buf := make([]byte, 32<<10)
		n, err := x.r.Read(buf)
		x.pending = append(x.pending, buf[:n]...)
		if err == io.EOF {
			x.eof = true
		} else if err != nil {
			return 0, err
		}
		x.pending = x.apply(x.pending)
		// Keep the tail that could be the start of a key.
		if !x.eof && len(x.pending) > x.keep {
			cut := len(x.pending) - x.keep
			x.out = append(x.out, x.pending[:cut]...)
			x.pending = append([]byte(nil), x.pending[cut:]...)
		}
	}
	n := copy(b, x.out)
	x.out = x.out[n:]
	return n, nil
}

func (x *replacer) apply(b []byte) []byte {
	for _, p := range x.pairs {
		if bytes.Contains(b, p[0]) {
			b = bytes.ReplaceAll(b, p[0], p[1])
		}
	}
	return b
}

// peekedConn is a connection whose first bytes were read into a buffer.
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// oneConn is a listener of one connection (a tunnel's requests).
type oneConn struct {
	c    net.Conn
	once sync.Once
	done chan struct{}
	used bool
	mu   sync.Mutex
}

func (l *oneConn) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.used {
		l.used = true
		l.mu.Unlock()
		return &closeNotify{Conn: l.c, l: l}, nil
	}
	l.mu.Unlock()
	<-l.done
	return nil, net.ErrClosed
}

func (l *oneConn) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *oneConn) Addr() net.Addr { return l.c.LocalAddr() }

type closeNotify struct {
	net.Conn
	l *oneConn
}

func (c *closeNotify) Close() error {
	err := c.Conn.Close()
	_ = c.l.Close()
	return err
}

func plainNetError(err error) string {
	var dns *net.DNSError
	switch {
	case errors.As(err, &dns):
		return "its name couldn't be looked up"
	case errors.Is(err, context.DeadlineExceeded):
		return "it didn't answer in time"
	}
	var cert x509.UnknownAuthorityError
	if errors.As(err, &cert) {
		return "its certificate isn't one this computer trusts"
	}
	s := err.Error()
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

func sizeWords(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%d GB", n>>30)
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%d KB", n>>10)
}

// quietLog keeps the proxy's server from printing connection errors (a
// program that drops a connection is its own business).
func quietLog() *log.Logger { return log.New(io.Discard, "", 0) }
