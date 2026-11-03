package c2pa

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// c2patool signs through a program of its own choosing (`--signer-path`,
// "the production way: private key material never passes through c2patool",
// its own docs say): MetaRush gives it MetaRush itself (D416). That signer
// holds no key either: it asks the MetaRush that started c2patool, over one
// end of a socket pair MetaRush made for that signing alone and handed to
// c2patool as its file descriptor 3, which c2patool's signer inherits
// (D436). Nothing else can reach it: there is no socket file to connect to,
// no ticket, and nothing about it in an environment or on a command line;
// another program can't take an open socket from c2patool. MetaRush answers
// `--signer-info` with the certificate chain and algorithm and signs exactly
// one COSE signature input per signing, reserved before it signs; it keeps
// what it returned, and Sign requires the signature in the signed copy to
// be exactly that.

// SignerCommand is the first argument that makes this binary c2patool's
// signer (cli.Execute and test binaries' TestMain check it first).
const SignerCommand = "c2pa-signer"

const (
	// signerFD is where the signer finds MetaRush: the socket c2patool
	// inherited from MetaRush (ExtraFiles' first) and passed on to it.
	signerFD = 3
	// maxPayload is the largest signature input taken (a claim's is a few KB).
	maxPayload = 1 << 20
	maxHeader  = 4 << 10
	// signsPerSigning: c2patool signs one claim per signing; that one
	// signature is reserved before MetaRush signs, and nothing more is.
	signsPerSigning = 1
	// maxInfos: c2patool asks --signer-info once.
	maxInfos = 4
)

// errOutsider is what the signer says when it isn't run by c2patool for a
// signing MetaRush started.
const errOutsider = "MetaRush signs Content Credentials only for a delivery it is making."

// MaybeSigner makes a test binary act as c2patool's signer when c2patool
// runs it: call it first in TestMain (the metarush binary does it in
// cli.Execute).
func MaybeSigner() {
	if len(os.Args) > 1 && os.Args[1] == SignerCommand {
		os.Exit(SignerMain(os.Args[2:]))
	}
}

type request struct {
	Op   string `json:"op"`
	Size int    `json:"size,omitempty"`
}

type answer struct {
	OK    bool        `json:"ok"`
	Error string      `json:"error,omitempty"`
	Info  *signerInfo `json:"info,omitempty"`
	Size  int         `json:"size,omitempty"`
}

// signerInfo is what c2patool asks `--signer-info` for (its SignerInfo).
type signerInfo struct {
	Alg         string `json:"alg"`
	SignCert    string `json:"sign_cert"`
	TSAURL      string `json:"tsa_url,omitempty"`
	ReserveSize int    `json:"reserve_size,omitempty"`
}

// SignerMain is c2patool's signer: with --signer-info it prints the
// certificate chain and algorithm, else it signs what comes on standard
// input. It asks the MetaRush that ran c2patool for both, over the socket it
// inherited.
func SignerMain(args []string) int {
	f := os.NewFile(signerFD, "metarush")
	st, err := f.Stat()
	if err != nil || st.Mode()&os.ModeSocket == 0 {
		fmt.Fprintln(os.Stderr, errOutsider)
		return 2
	}
	conn, err := net.FileConn(f)
	_ = f.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, errOutsider)
		return 2
	}
	defer conn.Close()
	req := request{Op: "sign"}
	var payload []byte
	if slices.Contains(args, "--signer-info") {
		req.Op = "info"
	} else {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, maxPayload+1))
		if err != nil || len(b) == 0 || len(b) > maxPayload {
			fmt.Fprintln(os.Stderr, "MetaRush's signer got nothing it could sign.")
			return 2
		}
		payload, req.Size = b, len(b)
	}
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	// The request in one write, the way MetaRush reads it.
	head, _ := json.Marshal(req)
	if _, err := conn.Write(append(append(head, '\n'), payload...)); err != nil {
		fmt.Fprintln(os.Stderr, "MetaRush's signer couldn't reach MetaRush:", err)
		return 1
	}
	// Exactly the answer: the socket stays c2patool's for its next signer.
	br := bufio.NewReaderSize(conn, maxHeader)
	line, err := br.ReadSlice('\n')
	var ans answer
	if err != nil || json.Unmarshal(line, &ans) != nil {
		fmt.Fprintln(os.Stderr, "MetaRush's signer got no answer from MetaRush.")
		return 1
	}
	if !ans.OK {
		fmt.Fprintln(os.Stderr, ans.Error)
		return 1
	}
	if req.Op == "info" {
		b, _ := json.Marshal(ans.Info)
		_, _ = os.Stdout.Write(b)
		return 0
	}
	if ans.Size <= 0 || ans.Size > maxPayload {
		fmt.Fprintln(os.Stderr, "MetaRush's signer got no signature from MetaRush.")
		return 1
	}
	sig := make([]byte, ans.Size)
	if _, err := io.ReadFull(br, sig); err != nil {
		fmt.Fprintln(os.Stderr, "MetaRush's signer got no signature from MetaRush.")
		return 1
	}
	_, _ = os.Stdout.Write(sig)
	return 0
}

// session is one signing: the private folder it works in (0700, under the
// home's private state: the manifest, MetaRush's settings, the link to the
// video, the signed copy), the socket pair whose far end c2patool inherits,
// and what MetaRush signed.
type session struct {
	dir  string
	conn net.Conn // MetaRush's end
	peer *os.File // c2patool's end (its descriptor 3), closed here once c2patool runs
	// signer is c2patool's --signer-path; linkDir a folder made only for a
	// link to this binary when its path has a space (c2patool splits it).
	signer  string
	linkDir string
	cred    *Credential
	info    signerInfo
	wg      sync.WaitGroup

	mu       sync.Mutex
	reserved int      // signatures reserved (at most signsPerSigning)
	sigs     [][]byte // the signatures MetaRush returned
	signed   []string // SHA-256 of each input it signed
	infos    int
	refuse   []string // what was refused, for the error people read
	closed   bool
}

// newSession makes the private folder under root and the socket pair.
func newSession(root string, cred *Credential, tsa string) (*session, error) {
	dir, err := privateDir(root)
	if err != nil {
		return nil, err
	}
	s := &session{dir: dir, cred: cred}
	fail := func(err error) (*session, error) {
		s.close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	s.info = signerInfo{Alg: cred.Alg, SignCert: string(cred.ChainPEM()), TSAURL: tsa}
	if tsa != "" {
		s.info.ReserveSize = 20000 + len(s.info.SignCert) // room for the timestamp's own certificates
	}
	if s.signer, s.linkDir, err = signerCommand(dir); err != nil {
		return fail(err)
	}
	mine, theirs, err := socketPair()
	if err != nil {
		return fail(err)
	}
	s.peer = theirs
	conn, err := net.FileConn(mine)
	_ = mine.Close()
	if err != nil {
		return fail(err)
	}
	s.conn = conn
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

// socketPair makes a connected pair of Unix sockets that no program started
// meanwhile inherits (both close on exec; ExtraFiles hands one on).
func socketPair() (mine, theirs *os.File, err error) {
	syscall.ForkLock.RLock()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fds[0])
		syscall.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, nil, fmt.Errorf("c2pa: the signer's socket: %w", err)
	}
	return os.NewFile(uintptr(fds[0]), "c2pa-signer"), os.NewFile(uintptr(fds[1]), "c2pa-signer-peer"), nil
}

// privateDir is a new 0700 folder for one signing under root (the home's
// private state, 0700 too).
func privateDir(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("c2pa: no private folder to sign in")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(root, 0o700)
	d, err := os.MkdirTemp(root, "c2pa-")
	if err != nil {
		return "", err
	}
	_ = os.Chmod(d, 0o700)
	return d, nil
}

// signerCommand is c2patool's --signer-path: this binary as the signer.
// c2patool splits it at spaces, so a path with one is reached through a
// link in a folder whose path has none (the signing's own, else a new one
// in the temporary folder, which holds only that link).
func signerCommand(dir string) (cmd, linkDir string, err error) {
	exe, err := os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("c2pa: this program's own path: %w", err)
	}
	if !strings.ContainsAny(exe, " \t\n") {
		return exe + " " + SignerCommand, "", nil
	}
	if !strings.ContainsAny(dir, " \t\n") {
		link := filepath.Join(dir, "metarush")
		if err := os.Symlink(exe, link); err != nil {
			return "", "", err
		}
		return link + " " + SignerCommand, "", nil
	}
	for _, base := range []string{os.TempDir(), "/tmp"} {
		if strings.ContainsAny(base, " \t\n") {
			continue
		}
		d, err := os.MkdirTemp(base, "metarush-signer-")
		if err != nil {
			continue
		}
		_ = os.Chmod(d, 0o700)
		link := filepath.Join(d, "metarush")
		if err := os.Symlink(exe, link); err != nil {
			_ = os.RemoveAll(d)
			return "", "", err
		}
		return link + " " + SignerCommand, d, nil
	}
	return "", "", errors.New("c2pa: no folder for the signer whose path has no spaces")
}

// started is called once c2patool runs: it holds the far end now, so
// MetaRush lets go of its copy (the pair ends when c2patool and its signer do).
func (s *session) started() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.peer != nil {
		_ = s.peer.Close()
		s.peer = nil
	}
}

// close stops answering (the folder stays: the signed copy is in it).
func (s *session) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.peer != nil {
		_ = s.peer.Close()
		s.peer = nil
	}
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	s.wg.Wait()
	if s.linkDir != "" {
		_ = os.RemoveAll(s.linkDir)
	}
}

// returned is every signature MetaRush made in this signing.
func (s *session) returned() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sigs)
}

// signatures counts what this session signed.
func (s *session) signatures() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sigs)
}

func (s *session) refused() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.refuse, "; ")
}

// serve answers c2patool's signers, one request after another, until the
// pair ends.
func (s *session) serve() {
	defer s.wg.Done()
	br := bufio.NewReaderSize(s.conn, maxHeader)
	reply := func(a answer, data []byte) bool {
		b, _ := json.Marshal(a)
		_ = s.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		_, err := s.conn.Write(append(append(b, '\n'), data...))
		return err == nil
	}
	no := func(why string) bool {
		s.mu.Lock()
		s.refuse = append(s.refuse, why)
		s.mu.Unlock()
		return reply(answer{Error: "MetaRush didn't sign: " + why}, nil)
	}
	for {
		line, err := br.ReadSlice('\n')
		if err != nil {
			return // the pair ended (or a request longer than any it takes)
		}
		var req request
		if json.Unmarshal(line, &req) != nil {
			no("the request couldn't be read")
			return // nothing after it can be trusted to line up
		}
		switch req.Op {
		case "info":
			s.mu.Lock()
			s.infos++
			n := s.infos
			s.mu.Unlock()
			if n > maxInfos {
				if !no("it was asked who signs too many times") {
					return
				}
				continue
			}
			info := s.info
			if !reply(answer{OK: true, Info: &info}, nil) {
				return
			}
		case "sign":
			if req.Size <= 0 || req.Size > maxPayload {
				no("there was nothing to sign")
				return
			}
			msg := make([]byte, req.Size)
			if _, err := io.ReadFull(br, msg); err != nil {
				no("the bytes to sign didn't arrive")
				return
			}
			// The one signature a signing needs, reserved before signing.
			s.mu.Lock()
			if s.reserved >= signsPerSigning {
				s.mu.Unlock()
				if !no("it was asked to sign more than one claim") {
					return
				}
				continue
			}
			s.reserved++
			s.mu.Unlock()
			sig, err := s.cred.Sign(msg)
			if err != nil {
				why := "the signature couldn't be made"
				if errors.Is(err, ErrNotSignature) {
					why = "what it was asked to sign isn't a Content Credentials signature"
				}
				if !no(why) {
					return
				}
				continue
			}
			sum := sha256.Sum256(msg)
			s.mu.Lock()
			s.sigs = append(s.sigs, slices.Clone(sig))
			s.signed = append(s.signed, hex.EncodeToString(sum[:]))
			s.mu.Unlock()
			if !reply(answer{OK: true, Size: len(sig)}, sig) {
				return
			}
		default:
			if !no("it was asked for something it doesn't do") {
				return
			}
		}
	}
}
