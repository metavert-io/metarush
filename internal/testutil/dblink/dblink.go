// Package dblink is a stand-in between a test's app and the test MongoDB that
// the test can cut, to play a database outage inside the test process (D180);
// the shared mongod itself is never stopped. Cut drops every live connection
// (a reply on its way ends half read — "incomplete read of message header", as
// in a real outage) and holds new connections without an answer, so the
// driver waits in server selection; Restore lets connections through again.
//
//	link := dblink.Start(t)
//	a := testapp.NewWith(t, testapp.Options{DatabaseURI: link.URI()})
//	link.Cut()
//	…
//	link.Restore()
package dblink

import (
	"io"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/testutil"
)

// Link is one cuttable connection to the test MongoDB.
type Link struct {
	ln       net.Listener
	upstream string
	uri      string

	mu   sync.Mutex
	cut  bool
	live map[net.Conn]net.Conn // the app's side → the database's side
	held []net.Conn            // connections that arrived while cut
}

// Start listens on a loopback port and forwards to the test MongoDB
// (testutil.URI); it is closed when the test ends.
func Start(t testing.TB) *Link {
	t.Helper()
	u, err := url.Parse(testutil.URI())
	if err != nil || u.Host == "" {
		t.Fatalf("dblink: the test MongoDB address %q can't be used: %v", testutil.URI(), err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := &Link{ln: ln, upstream: u.Host, live: map[net.Conn]net.Conn{}}
	u.Host = ln.Addr().String()
	l.uri = u.String()
	go l.accept()
	t.Cleanup(l.close)
	return l
}

// URI is the address an app uses to reach the test MongoDB through the link.
func (l *Link) URI() string { return l.uri }

// Cut makes the database stop answering: live connections are dropped and new
// ones wait without an answer until Restore.
func (l *Link) Cut() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cut = true
	for c, up := range l.live {
		_ = c.Close()
		_ = up.Close()
	}
	l.live = map[net.Conn]net.Conn{}
}

// Restore lets connections through again (the ones held while cut are closed,
// so the driver connects again at once).
func (l *Link) Restore() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cut = false
	for _, c := range l.held {
		_ = c.Close()
	}
	l.held = nil
}

func (l *Link) close() {
	_ = l.ln.Close()
	l.Restore()
	l.mu.Lock()
	defer l.mu.Unlock()
	for c, up := range l.live {
		_ = c.Close()
		_ = up.Close()
	}
}

func (l *Link) accept() {
	for {
		c, err := l.ln.Accept()
		if err != nil {
			return
		}
		l.mu.Lock()
		if l.cut {
			l.held = append(l.held, c)
			l.mu.Unlock()
			continue
		}
		l.mu.Unlock()
		go l.pipe(c)
	}
}

func (l *Link) pipe(c net.Conn) {
	up, err := net.DialTimeout("tcp", l.upstream, 5*time.Second)
	if err != nil {
		_ = c.Close()
		return
	}
	l.mu.Lock()
	if l.cut {
		l.mu.Unlock()
		_ = c.Close()
		_ = up.Close()
		return
	}
	l.live[c] = up
	l.mu.Unlock()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
	_ = c.Close()
	_ = up.Close()
	l.mu.Lock()
	delete(l.live, c)
	l.mu.Unlock()
}
