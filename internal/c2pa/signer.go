package c2pa

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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
	"time"
)

// c2patool signs through a program of its own choosing (`--signer-path`,
// "the production way: private key material never passes through c2patool",
// its own docs say): MetaRush gives it MetaRush itself (D416). That signer
// holds no key either — it asks, over a socket in a private folder, the
// MetaRush that started c2patool, whose process holds the key it read from
// the keychain. So the key never reaches c2patool, a command line, an
// environment, a file or an ability. The signer answers c2patool's
// `--signer-info` with the certificate chain and algorithm, and signs only
// COSE signature inputs, at most twice per signing; every signature is
// counted (a signing that MetaRush didn't sign is refused afterwards).

// SignerCommand is the first argument that makes this binary c2patool's
// signer (cli.Execute and test binaries' TestMain check it first).
const SignerCommand = "c2pa-signer"

const (
	envSocket = "METARUSH_C2PA_SIGNER_SOCKET"
	envTicket = "METARUSH_C2PA_SIGNER_TICKET"
	// maxPayload is the largest signature input taken (a claim's is a few KB).
	maxPayload = 1 << 20
	maxHeader  = 4 << 10
	// maxSigns: c2patool signs a claim once; a second try is allowed, no more.
	maxSigns = 2
)

// MaybeSigner makes a test binary act as c2patool's signer when c2patool
// runs it: call it first in TestMain (the metarush binary does it in
// cli.Execute).
func MaybeSigner() {
	if len(os.Args) > 1 && os.Args[1] == SignerCommand {
		os.Exit(SignerMain(os.Args[2:]))
	}
}

type request struct {
	Ticket string `json:"ticket"`
	Op     string `json:"op"`
	Size   int    `json:"size,omitempty"`
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
// input. It asks the MetaRush that ran c2patool for both.
func SignerMain(args []string) int {
	sock, ticket := os.Getenv(envSocket), os.Getenv(envTicket)
	if sock == "" || ticket == "" {
		fmt.Fprintln(os.Stderr, "MetaRush signs Content Credentials only for a delivery it is making.")
		return 2
	}
	req := request{Ticket: ticket, Op: "sign"}
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
	conn, err := net.DialTimeout("unix", sock, 10*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "MetaRush's signer couldn't reach MetaRush:", err)
		return 1
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	head, _ := json.Marshal(req)
	if _, err := conn.Write(append(head, '\n')); err != nil {
		fmt.Fprintln(os.Stderr, "MetaRush's signer couldn't reach MetaRush:", err)
		return 1
	}
	if _, err := conn.Write(payload); err != nil {
		fmt.Fprintln(os.Stderr, "MetaRush's signer couldn't reach MetaRush:", err)
		return 1
	}
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
	sig := make([]byte, ans.Size)
	if ans.Size <= 0 || ans.Size > maxPayload {
		fmt.Fprintln(os.Stderr, "MetaRush's signer got no signature from MetaRush.")
		return 1
	}
	if _, err := io.ReadFull(br, sig); err != nil {
		fmt.Fprintln(os.Stderr, "MetaRush's signer got no signature from MetaRush.")
		return 1
	}
	_, _ = os.Stdout.Write(sig)
	return 0
}

// session is one signing: the private folder c2patool works in (0700), the
// socket MetaRush answers c2patool's signer on, and what it signed.
type session struct {
	dir    string
	sock   string
	ticket string
	cred   *Credential
	info   signerInfo
	ln     net.Listener
	wg     sync.WaitGroup
	mu     sync.Mutex
	signed []string // SHA-256 of each input signed
	asked  int      // --signer-info answers
	refuse []string // what was refused, for the error people read
}

// newSession makes the private folder, the socket and the link through
// which c2patool runs this binary as its signer (c2patool splits its signer
// command at spaces, so the link's path has none).
func newSession(cred *Credential, tsa string) (*session, error) {
	dir, err := privateDir()
	if err != nil {
		return nil, err
	}
	s := &session{dir: dir, sock: filepath.Join(dir, "s"), cred: cred}
	fail := func(err error) (*session, error) {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	t := make([]byte, 16)
	if _, err := rand.Read(t); err != nil {
		return fail(err)
	}
	s.ticket = hex.EncodeToString(t)
	s.info = signerInfo{Alg: cred.Alg, SignCert: string(cred.ChainPEM()), TSAURL: tsa}
	if tsa != "" {
		s.info.ReserveSize = 20000 + len(s.info.SignCert) // room for the timestamp's own certificates
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(fmt.Errorf("c2pa: this program's own path: %w", err))
	}
	if err := os.Symlink(exe, filepath.Join(dir, "metarush")); err != nil {
		return fail(err)
	}
	ln, err := net.Listen("unix", s.sock)
	if err != nil {
		return fail(err)
	}
	_ = os.Chmod(s.sock, 0o600)
	s.ln = ln
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

// privateDir is a new 0700 folder whose path has no spaces and is short
// enough for a socket (macOS takes 104 bytes).
func privateDir() (string, error) {
	for _, base := range []string{os.TempDir(), "/tmp"} {
		if strings.ContainsAny(base, " \t\n") || len(base) > 60 {
			continue
		}
		if d, err := os.MkdirTemp(base, "metarush-c2pa-"); err == nil {
			_ = os.Chmod(d, 0o700)
			return d, nil
		}
	}
	return "", errors.New("c2pa: no folder for the signer whose path has no spaces")
}

// signerPath is c2patool's --signer-path: this binary (through the link) as the signer.
func (s *session) signerPath() string {
	return filepath.Join(s.dir, "metarush") + " " + SignerCommand
}

// env is what c2patool and its signer need to find this session.
func (s *session) env() []string {
	return []string{envSocket + "=" + s.sock, envTicket + "=" + s.ticket}
}

// close stops answering and removes the private folder (and all in it).
func (s *session) close() {
	_ = s.ln.Close()
	s.wg.Wait()
	_ = os.RemoveAll(s.dir)
}

// signatures counts what this session signed.
func (s *session) signatures() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.signed)
}

func (s *session) refused() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.refuse, "; ")
}

func (s *session) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
		}()
	}
}

func (s *session) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	br := bufio.NewReaderSize(io.LimitReader(conn, maxHeader+maxPayload), maxHeader)
	reply := func(a answer, data []byte) {
		b, _ := json.Marshal(a)
		_, _ = conn.Write(append(b, '\n'))
		if len(data) > 0 {
			_, _ = conn.Write(data)
		}
	}
	no := func(why string) {
		s.mu.Lock()
		s.refuse = append(s.refuse, why)
		s.mu.Unlock()
		reply(answer{Error: "MetaRush didn't sign: " + why}, nil)
	}
	line, err := br.ReadSlice('\n')
	var req request
	if err != nil || json.Unmarshal(line, &req) != nil {
		no("the request couldn't be read")
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Ticket), []byte(s.ticket)) != 1 {
		no("the request isn't from this signing")
		return
	}
	switch req.Op {
	case "info":
		s.mu.Lock()
		s.asked++
		s.mu.Unlock()
		info := s.info
		reply(answer{OK: true, Info: &info}, nil)
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
		s.mu.Lock()
		if len(s.signed) >= maxSigns {
			s.mu.Unlock()
			no("it was asked to sign more than once")
			return
		}
		s.mu.Unlock()
		sig, err := s.cred.Sign(msg)
		if err != nil {
			if errors.Is(err, ErrNotSignature) {
				no("what it was asked to sign isn't a Content Credentials signature")
			} else {
				no("the signature couldn't be made")
			}
			return
		}
		sum := sha256.Sum256(msg)
		s.mu.Lock()
		s.signed = append(s.signed, hex.EncodeToString(sum[:]))
		s.mu.Unlock()
		reply(answer{OK: true, Size: len(sig)}, sig)
	default:
		no("it was asked for something it doesn't do")
	}
}
