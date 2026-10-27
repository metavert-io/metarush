package media

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	htransport "google.golang.org/api/transport/http"
)

// GCSConfig configures the native Google Cloud Storage driver.
type GCSConfig struct {
	Bucket string
	Prefix string
	// CredentialsFile is the path of a service-account JSON key. Empty means
	// Application Default Credentials (an attached service account on GCP,
	// GOOGLE_APPLICATION_CREDENTIALS, or `gcloud auth application-default login`).
	CredentialsFile string

	// Endpoint, NoAuth, HTTPClient, ChunkSize and RetryBackoff exist for
	// emulators and tests. STORAGE_EMULATOR_HOST is honored the same way the
	// Google client library honors it (no authentication).
	Endpoint     string // base URL, e.g. http://127.0.0.1:4443
	NoAuth       bool
	HTTPClient   *http.Client
	ChunkSize    int64 // resumable chunk size, rounded up to 256 KiB (default 16 MiB)
	RetryBackoff time.Duration
}

const (
	gcsDefaultChunk = 16 << 20
	gcsChunkQuantum = 256 << 10
	gcsMaxRetries   = 5
)

// GCS is the native Google Cloud Storage driver.
//
// Reads, metadata, deletes and V4 signed URLs use the official client library.
// Large uploads use the JSON API's resumable-upload protocol directly, because
// the library does not expose the session URI: MetaRush persists that URI and
// the committed offset in the ingest session (media.Resume), so an upload
// interrupted by a crash, a closed laptop or a restart continues from the last
// byte GCS acknowledged — on this machine or another one. The expected CRC32C
// and MD5 are declared when the session starts, so GCS itself rejects the object
// if the bytes do not match, and the final object is checked again here.
type GCS struct {
	cfg        GCSConfig
	client     *storage.Client
	bkt        *storage.BucketHandle
	hc         *http.Client // authenticated, never follows redirects
	uploadBase string       // https://storage.googleapis.com/upload/storage/v1
	baseURL    *url.URL     // scheme+host every session URI must match
	host       string       // non-default endpoint (emulator) for the locator
	chunk      int64
	backoff    time.Duration
	authDesc   string

	saEmail string
	saKey   []byte

	signMu    sync.Mutex
	signState int // 0 unknown, 1 can sign, 2 cannot
	signNote  string
	probing   bool
}

// NewGCS builds the driver. Only a service-account key file or Application
// Default Credentials are accepted (never HMAC keys, never key contents in config).
func NewGCS(cfg GCSConfig) (*GCS, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("gcs store needs a bucket")
	}
	cfg.Prefix = strings.Trim(cfg.Prefix, "/")
	g := &GCS{cfg: cfg, chunk: cfg.ChunkSize, backoff: cfg.RetryBackoff}
	if g.chunk <= 0 {
		g.chunk = gcsDefaultChunk
	}
	g.chunk = (g.chunk + gcsChunkQuantum - 1) / gcsChunkQuantum * gcsChunkQuantum
	if g.backoff <= 0 {
		g.backoff = 500 * time.Millisecond
	}
	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	noAuth := cfg.NoAuth
	if endpoint == "" {
		if h := os.Getenv("STORAGE_EMULATOR_HOST"); h != "" {
			endpoint = strings.TrimRight(h, "/")
			if !strings.Contains(endpoint, "://") {
				endpoint = "http://" + endpoint
			}
			noAuth = true
		}
	}
	var credOpts []option.ClientOption
	switch {
	case cfg.CredentialsFile != "":
		b, err := os.ReadFile(cfg.CredentialsFile)
		if err != nil {
			return nil, fmt.Errorf("gcs credentials file: %w", err)
		}
		var sa struct {
			Type        string `json:"type"`
			ClientEmail string `json:"client_email"`
			PrivateKey  string `json:"private_key"`
		}
		if err := json.Unmarshal(b, &sa); err != nil {
			return nil, fmt.Errorf("gcs credentials file %s is not a JSON key file: %w", cfg.CredentialsFile, err)
		}
		if sa.Type != "service_account" {
			return nil, fmt.Errorf("gcs credentials file %s holds %q credentials; it must be a service-account key (for user or workload-identity credentials leave credentialsFile empty and use Application Default Credentials)", cfg.CredentialsFile, sa.Type)
		}
		if sa.ClientEmail == "" || sa.PrivateKey == "" {
			return nil, fmt.Errorf("gcs credentials file %s has no client_email/private_key", cfg.CredentialsFile)
		}
		g.saEmail, g.saKey = sa.ClientEmail, []byte(sa.PrivateKey)
		credOpts = append(credOpts, option.WithAuthCredentialsJSON(option.ServiceAccount, b))
		g.authDesc = "service account " + sa.ClientEmail + " (key file " + cfg.CredentialsFile + ")"
	default:
		g.authDesc = "Application Default Credentials"
	}
	var opts []option.ClientOption
	if noAuth {
		opts = append(opts, option.WithoutAuthentication())
		g.authDesc = "none (emulator at " + endpoint + ")"
		if g.saEmail != "" {
			g.authDesc += "; URLs signed as " + g.saEmail
		}
	} else {
		opts = append(opts, credOpts...)
	}
	base := "https://storage.googleapis.com"
	if endpoint != "" {
		opts = append(opts, option.WithEndpoint(endpoint+"/storage/v1/"))
		base = endpoint
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	ctx := context.Background()
	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("gcs: %w (set media.gcs.credentialsFile to a service-account key, or configure Application Default Credentials)", err)
	}
	g.client = client
	g.bkt = client.Bucket(cfg.Bucket)
	var hc *http.Client
	switch {
	case cfg.HTTPClient != nil:
		hc = cfg.HTTPClient
	case noAuth:
		hc = &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
	default:
		hc, _, err = htransport.NewClient(ctx, append([]option.ClientOption{option.WithScopes(storage.ScopeReadWrite)}, credOpts...)...)
		if err != nil {
			return nil, fmt.Errorf("gcs: %w (set media.gcs.credentialsFile to a service-account key, or configure Application Default Credentials)", err)
		}
	}
	nc := *hc
	nc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	g.hc = &nc
	if g.baseURL, err = url.Parse(base); err != nil || g.baseURL.Host == "" {
		return nil, fmt.Errorf("gcs: invalid endpoint %q", base)
	}
	if endpoint != "" {
		g.host = g.baseURL.Host
	}
	g.uploadBase = base + "/upload/storage/v1"
	switch {
	case g.saKey != nil:
		g.signState, g.signNote = 1, "signed with the service-account key"
	case noAuth:
		g.signState, g.signNote = 2, "no credentials (emulator)"
	}
	return g, nil
}

func (g *GCS) Name() string { return "gcs" }

// Locator is gs://bucket[/prefix], plus @host for a non-Google endpoint.
func (g *GCS) Locator() string {
	loc := "gs://" + g.cfg.Bucket
	if g.cfg.Prefix != "" {
		loc += "/" + g.cfg.Prefix
	}
	if g.host != "" {
		loc += "@" + g.host
	}
	return loc
}

// Capabilities: PresignedGet is true only once signing is known to work (a
// service-account key, or IAM signBlob for an attached service account).
// Until then the dashboard streams media through the daemon.
func (g *GCS) Capabilities() Capabilities {
	return Capabilities{RangeRead: true, PresignedGet: g.canSignAsync(), ResumableUpload: true, Shared: true}
}

// Auth describes how the driver authenticates (no secrets).
func (g *GCS) Auth() string { return g.authDesc }

// SigningStatus reports whether V4 signed URLs can be produced, probing once if
// needed (Application Default Credentials may or may not be able to sign).
func (g *GCS) SigningStatus() (bool, string) {
	g.signMu.Lock()
	st := g.signState
	g.signMu.Unlock()
	if st == 0 {
		g.probeSigning()
	}
	g.signMu.Lock()
	defer g.signMu.Unlock()
	return g.signState == 1, g.signNote
}

func (g *GCS) canSignAsync() bool {
	g.signMu.Lock()
	defer g.signMu.Unlock()
	if g.signState == 0 && !g.probing {
		g.probing = true
		go g.probeSigning()
	}
	return g.signState == 1
}

func (g *GCS) probeSigning() {
	done := make(chan error, 1)
	go func() {
		_, err := g.bkt.SignedURL("probes/signing-check", &storage.SignedURLOptions{Scheme: storage.SigningSchemeV4, Method: http.MethodGet, Expires: time.Now().Add(time.Minute)})
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		err = errors.New("timed out")
	}
	g.signMu.Lock()
	defer g.signMu.Unlock()
	g.probing = false
	if err != nil {
		g.signState, g.signNote = 2, "cannot sign URLs with these credentials ("+err.Error()+"); the dashboard streams media through the daemon"
		return
	}
	g.signState, g.signNote = 1, "signed through IAM signBlob for the attached service account"
}

func (g *GCS) full(key string) (string, error) {
	if err := ValidKey(key); err != nil {
		return "", err
	}
	if g.cfg.Prefix == "" {
		return key, nil
	}
	return g.cfg.Prefix + "/" + key, nil
}

func (g *GCS) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	name, err := g.full(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	attrs, err := g.bkt.Object(name).Attrs(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return ObjectInfo{}, ErrNotFound
	}
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Key: key, Size: attrs.Size, ContentType: attrs.ContentType, ModTime: attrs.Updated, ETag: fmt.Sprintf("%d-%s", attrs.Generation, attrs.Etag)}, nil
}

func (g *GCS) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, ObjectInfo, error) {
	name, err := g.full(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	r, err := g.bkt.Object(name).NewRangeReader(ctx, offset, length)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil, ObjectInfo{}, ErrNotFound
	}
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	return r, ObjectInfo{Key: key, Size: r.Attrs.Size, ContentType: r.Attrs.ContentType, ModTime: r.Attrs.LastModified}, nil
}

func (g *GCS) Delete(ctx context.Context, key string) error {
	name, err := g.full(key)
	if err != nil {
		return err
	}
	if err := g.bkt.Object(name).Delete(ctx); err != nil && !errors.Is(err, storage.ErrObjectNotExist) {
		return err
	}
	return nil
}

// PresignGet returns a V4 signed GET URL (range-capable) when the credentials
// can sign; otherwise ErrUnsupported, and the dashboard streams instead.
func (g *GCS) PresignGet(ctx context.Context, key string, ttl time.Duration, contentType, downloadName string) (string, error) {
	name, err := g.full(key)
	if err != nil {
		return "", err
	}
	if ok, _ := g.SigningStatus(); !ok {
		return "", ErrUnsupported
	}
	q := url.Values{}
	if contentType != "" {
		q.Set("response-content-type", contentType)
	}
	if downloadName != "" {
		q.Set("response-content-disposition", fmt.Sprintf(`attachment; filename="%s"`, strings.ReplaceAll(downloadName, `"`, "")))
	}
	opts := &storage.SignedURLOptions{Scheme: storage.SigningSchemeV4, Method: http.MethodGet, Expires: time.Now().Add(ttl), QueryParameters: q, Style: g.urlStyle()}
	if g.saKey != nil {
		opts.GoogleAccessID, opts.PrivateKey = g.saEmail, g.saKey
	}
	if g.host != "" {
		opts.Hostname, opts.Insecure = g.host, g.baseURL.Scheme == "http"
	}
	return g.bkt.SignedURL(name, opts)
}

// ListIncomplete returns nothing: GCS resumable sessions cannot be listed. The
// sessions MetaRush starts are recorded in ingest sessions (which `ingest
// reconcile` cancels when abandoned), and GCS discards any session that is not
// completed within a week.
func (g *GCS) ListIncomplete(context.Context) ([]Incomplete, error) { return []Incomplete{}, nil }

// AbortIncomplete cancels a resumable session whose URI is in UploadID.
func (g *GCS) AbortIncomplete(ctx context.Context, in Incomplete) error {
	if !g.ownSession(in.UploadID) {
		return fmt.Errorf("not a resumable session of this store")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, in.UploadID, nil)
	if err != nil {
		return err
	}
	res, err := g.hc.Do(req)
	if err != nil {
		return err
	}
	drain(res)
	switch res.StatusCode {
	case 499, http.StatusOK, http.StatusNoContent, http.StatusNotFound, http.StatusGone:
		return nil
	}
	return fmt.Errorf("cancel resumable session: HTTP %d", res.StatusCode)
}

// ownSession checks that a session URI points at this store's upload endpoint,
// so an altered database record can never make the authenticated client send
// credentials to another host.
func (g *GCS) ownSession(uri string) bool {
	u, err := url.Parse(uri)
	if err != nil || !strings.Contains(u.Path, "/upload/") || u.Query().Get("upload_id") == "" {
		return false
	}
	if g.host == "" { // Google's endpoint: session URIs are on a googleapis.com host
		return u.Scheme == "https" && (u.Host == "storage.googleapis.com" || strings.HasSuffix(u.Host, ".googleapis.com"))
	}
	return u.Scheme == g.baseURL.Scheme && u.Host == g.baseURL.Host
}

// gcsObject is the JSON API object resource returned when an upload completes.
type gcsObject struct {
	Name        string          `json:"name"`
	Size        json.RawMessage `json:"size"`
	CRC32C      string          `json:"crc32c"`
	MD5Hash     string          `json:"md5Hash"`
	ContentType string          `json:"contentType"`
}

func (o *gcsObject) size() int64 {
	n, _ := strconv.ParseInt(strings.Trim(string(o.Size), `"`), 10, 64)
	return n
}

var (
	errSessionGone = errors.New("gcs: resumable session expired or unknown")
	errExists      = errors.New("gcs: object already exists")
)

// transientError is retried in-process (after asking GCS what it committed).
type transientError struct{ err error }

func (t *transientError) Error() string { return t.err.Error() }
func (t *transientError) Unwrap() error { return t.err }

type chunkResult struct {
	complete bool
	offset   int64
	obj      *gcsObject
}

// PutFile uploads a file. Objects up to one chunk go in a single request; larger
// ones use a resumable session that survives process restarts (see GCS).
func (g *GCS) PutFile(ctx context.Context, key, path string, opts PutOptions, r Resume, progress Progress) (ObjectInfo, error) {
	name, err := g.full(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return ObjectInfo{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ObjectInfo{}, err
	}
	total := st.Size()
	if info, err := g.Stat(ctx, key); err == nil && info.Size == total {
		if progress != nil {
			progress(total, total, r)
		}
		return info, nil // content-addressed object already present
	}
	crc, sum, err := fileChecksums(f, total)
	if err != nil {
		return ObjectInfo{}, err
	}
	ct := opts.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	meta := map[string]string{}
	if opts.SHA256 != "" {
		meta["sha256"] = opts.SHA256
	}
	if total <= g.chunk {
		return g.putSingle(ctx, key, name, f, total, ct, meta, crc, sum, r, progress)
	}
	return g.putResumable(ctx, key, name, f, total, ct, meta, crc, sum, r, progress)
}

func (g *GCS) putSingle(ctx context.Context, key, name string, f *os.File, total int64, ct string, meta map[string]string, crc uint32, sum []byte, r Resume, progress Progress) (ObjectInfo, error) {
	w := g.bkt.Object(name).If(storage.Conditions{DoesNotExist: true}).NewWriter(ctx)
	w.ChunkSize = 0 // one request
	w.ContentType, w.Metadata, w.MD5 = ct, meta, sum
	w.CRC32C, w.SendCRC32C = crc, true
	_, cerr := io.Copy(w, io.NewSectionReader(f, 0, total))
	err := w.Close()
	if cerr != nil && err == nil {
		err = cerr
	}
	var ge *googleapi.Error
	if err != nil && !(errors.As(err, &ge) && ge.Code == http.StatusPreconditionFailed) {
		return ObjectInfo{}, fmt.Errorf("put %s: %w", key, err)
	}
	if err == nil {
		if a := w.Attrs(); a != nil {
			if err := verifyUpload(key, total, crc, sum, a.Size, a.CRC32C, a.CRC32C != 0, a.MD5); err != nil {
				return ObjectInfo{}, err
			}
		}
	}
	info, err := g.Stat(ctx, key)
	if err != nil {
		return ObjectInfo{}, err
	}
	if info.Size != total {
		return ObjectInfo{}, fmt.Errorf("integrity check failed for %s: stored %d bytes, expected %d", key, info.Size, total)
	}
	if progress != nil {
		progress(total, total, r)
	}
	return info, nil
}

func (g *GCS) putResumable(ctx context.Context, key, name string, f *os.File, total int64, ct string, meta map[string]string, crc uint32, sum []byte, r Resume, progress Progress) (ObjectInfo, error) {
	var res *chunkResult
	offset := int64(-1)
	if r.SessionURI != "" && g.ownSession(r.SessionURI) {
		cr, err := g.queryStatus(ctx, r.SessionURI, total)
		switch {
		case err == nil:
			res, offset = cr, cr.offset
		case errors.Is(err, errSessionGone):
			// expired (sessions last a week) or canceled: start over below
		case errors.Is(err, errExists):
			return g.finish(ctx, key, total, crc, sum, nil, r, progress)
		default:
			return ObjectInfo{}, fmt.Errorf("resume upload of %s: %w", key, err) // keep the session for the next attempt
		}
	}
	// newSession starts over and reports the new session at once, so the caller
	// persists it before any bytes move.
	newSession := func() error {
		uri, err := g.startSession(ctx, name, total, ct, meta, crc, sum)
		if err != nil {
			return err
		}
		r = Resume{SessionURI: uri, PartSize: g.chunk}
		offset, res = 0, nil
		if progress != nil {
			progress(0, total, r)
		}
		return nil
	}
	if offset < 0 {
		if err := newSession(); err != nil {
			if errors.Is(err, errExists) {
				return g.finish(ctx, key, total, crc, sum, nil, r, progress)
			}
			return ObjectInfo{}, err
		}
	} else {
		r.Offset, r.PartSize = offset, g.chunk
		if progress != nil {
			progress(offset, total, r) // resuming: report where GCS says we are
		}
	}
	retries, stalls := 0, 0
	for res == nil || !res.complete {
		if err := ctx.Err(); err != nil {
			return ObjectInfo{}, err
		}
		var err error
		sentAll := offset >= total
		if sentAll {
			res, err = g.queryStatus(ctx, r.SessionURI, total) // all bytes sent; ask for the object
		} else {
			res, err = g.putChunk(ctx, r.SessionURI, f, offset, min(g.chunk, total-offset), total)
		}
		if err == nil {
			retries = 0
			if !res.complete {
				switch {
				case res.offset < offset:
					return ObjectInfo{}, fmt.Errorf("upload %s: GCS reported %d committed bytes after %d were acknowledged", key, res.offset, offset)
				case sentAll && res.offset >= total:
					return ObjectInfo{}, fmt.Errorf("upload %s: GCS holds all %d bytes but did not finalize the object", key, total)
				case res.offset == offset:
					if stalls++; stalls > gcsMaxRetries {
						return ObjectInfo{}, fmt.Errorf("upload %s: no progress at byte %d after %d attempts (resumable later)", key, offset, stalls)
					}
				default:
					stalls = 0
				}
				offset = res.offset
				r.Offset = offset
				if progress != nil {
					progress(offset, total, r)
				}
			}
			continue
		}
		if ctx.Err() != nil {
			return ObjectInfo{}, ctx.Err()
		}
		switch {
		case errors.Is(err, errExists):
			return g.finish(ctx, key, total, crc, sum, nil, r, progress)
		case errors.Is(err, errSessionGone):
			if retries++; retries > gcsMaxRetries {
				return ObjectInfo{}, err
			}
			if err := newSession(); err != nil {
				return ObjectInfo{}, err
			}
			continue
		}
		var te *transientError
		if !errors.As(err, &te) {
			return ObjectInfo{}, fmt.Errorf("upload %s at byte %d: %w", key, offset, err)
		}
		if retries++; retries > gcsMaxRetries {
			return ObjectInfo{}, fmt.Errorf("upload %s at byte %d (resumable later): %w", key, offset, err)
		}
		if err := sleepCtx(ctx, g.backoff<<(retries-1)); err != nil {
			return ObjectInfo{}, err
		}
		// Ask GCS how much it actually committed before sending more.
		cr, qerr := g.queryStatus(ctx, r.SessionURI, total)
		switch {
		case qerr == nil:
			if cr.complete {
				res = cr
			} else {
				offset, r.Offset = cr.offset, cr.offset
			}
		case errors.Is(qerr, errExists):
			return g.finish(ctx, key, total, crc, sum, nil, r, progress)
		case errors.Is(qerr, errSessionGone):
			if err := newSession(); err != nil {
				return ObjectInfo{}, err
			}
		}
	}
	return g.finish(ctx, key, total, crc, sum, res.obj, r, progress)
}

// finish verifies the stored object (size, CRC32C, MD5) and reports completion.
func (g *GCS) finish(ctx context.Context, key string, total int64, crc uint32, sum []byte, obj *gcsObject, r Resume, progress Progress) (ObjectInfo, error) {
	if obj != nil {
		if err := verifyObject(key, total, crc, sum, obj); err != nil {
			return ObjectInfo{}, err
		}
	}
	info, err := g.Stat(ctx, key)
	if err != nil {
		return ObjectInfo{}, err
	}
	if info.Size != total {
		return ObjectInfo{}, fmt.Errorf("integrity check failed for %s: stored %d bytes, expected %d", key, info.Size, total)
	}
	r.Offset = total
	if progress != nil {
		progress(total, total, r)
	}
	return info, nil
}

func verifyObject(key string, total int64, crc uint32, sum []byte, o *gcsObject) error {
	var gotCRC uint32
	hasCRC := false
	if o.CRC32C != "" {
		b, err := base64.StdEncoding.DecodeString(o.CRC32C)
		if err != nil || len(b) != 4 {
			return fmt.Errorf("integrity check failed for %s: unreadable crc32c %q", key, o.CRC32C)
		}
		gotCRC, hasCRC = binary.BigEndian.Uint32(b), true
	}
	var gotMD5 []byte
	if o.MD5Hash != "" {
		b, err := base64.StdEncoding.DecodeString(o.MD5Hash)
		if err != nil {
			return fmt.Errorf("integrity check failed for %s: unreadable md5Hash %q", key, o.MD5Hash)
		}
		gotMD5 = b
	}
	return verifyUpload(key, total, crc, sum, o.size(), gotCRC, hasCRC, gotMD5)
}

func verifyUpload(key string, total int64, crc uint32, sum []byte, gotSize int64, gotCRC uint32, hasCRC bool, gotMD5 []byte) error {
	if gotSize != total {
		return fmt.Errorf("integrity check failed for %s: stored %d bytes, expected %d", key, gotSize, total)
	}
	if hasCRC && gotCRC != crc {
		return fmt.Errorf("integrity check failed for %s: stored crc32c %08x, expected %08x", key, gotCRC, crc)
	}
	if len(gotMD5) > 0 && !bytes.Equal(gotMD5, sum) {
		return fmt.Errorf("integrity check failed for %s: stored md5 differs", key)
	}
	return nil
}

// startSession opens a resumable session. The object is created only if it
// does not exist yet (ifGenerationMatch=0), and GCS validates the declared
// checksums when the last byte arrives.
func (g *GCS) startSession(ctx context.Context, name string, total int64, ct string, meta map[string]string, crc uint32, sum []byte) (string, error) {
	var cb [4]byte
	binary.BigEndian.PutUint32(cb[:], crc)
	body, _ := json.Marshal(map[string]any{
		"name": name, "contentType": ct, "metadata": meta,
		"crc32c": base64.StdEncoding.EncodeToString(cb[:]), "md5Hash": base64.StdEncoding.EncodeToString(sum),
	})
	u := g.uploadBase + "/b/" + url.PathEscape(g.cfg.Bucket) + "/o?uploadType=resumable&ifGenerationMatch=0&name=" + url.QueryEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Type", ct)
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(total, 10))
	res, err := g.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("start resumable upload: %w", err)
	}
	defer drain(res)
	switch {
	case res.StatusCode == http.StatusPreconditionFailed:
		return "", errExists
	case res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated:
		return "", fmt.Errorf("start resumable upload: %s", httpErr(res))
	}
	loc := res.Header.Get("Location")
	if !g.ownSession(loc) {
		return "", fmt.Errorf("start resumable upload: unexpected session URI from %s", g.baseURL.Host)
	}
	return loc, nil
}

func (g *GCS) putChunk(ctx context.Context, session string, f *os.File, off, n, total int64) (*chunkResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, session, io.NewSectionReader(f, off, n))
	if err != nil {
		return nil, err
	}
	req.ContentLength = n
	req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, off+n-1, total))
	return g.doSession(req)
}

// queryStatus asks GCS how many bytes of a session it has committed.
func (g *GCS) queryStatus(ctx context.Context, session string, total int64) (*chunkResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, session, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.ContentLength = 0
	req.Header.Set("Content-Range", fmt.Sprintf("bytes */%d", total))
	return g.doSession(req)
}

func (g *GCS) doSession(req *http.Request) (*chunkResult, error) {
	res, err := g.hc.Do(req)
	if err != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		return nil, &transientError{err}
	}
	defer drain(res)
	switch {
	case res.StatusCode == http.StatusOK || res.StatusCode == http.StatusCreated:
		var o gcsObject
		if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&o); err != nil {
			return nil, fmt.Errorf("upload completed but the object resource is unreadable: %w", err)
		}
		return &chunkResult{complete: true, obj: &o}, nil
	case res.StatusCode == http.StatusPermanentRedirect: // "308 Resume Incomplete"
		cr := &chunkResult{}
		if rg := res.Header.Get("Range"); rg != "" {
			_, end, ok := strings.Cut(strings.TrimPrefix(rg, "bytes="), "-")
			n, err := strconv.ParseInt(end, 10, 64)
			if !ok || err != nil {
				return nil, fmt.Errorf("unreadable Range %q in resumable-upload response", rg)
			}
			cr.offset = n + 1
		}
		return cr, nil
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return nil, errSessionGone
	case res.StatusCode == http.StatusPreconditionFailed:
		return nil, errExists
	case res.StatusCode == http.StatusTooManyRequests || res.StatusCode == http.StatusRequestTimeout || res.StatusCode >= 500:
		return nil, &transientError{errors.New(httpErr(res))}
	default:
		return nil, errors.New(httpErr(res))
	}
}

func httpErr(res *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
	msg := strings.TrimSpace(string(b))
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
	}
	if msg == "" {
		return fmt.Sprintf("HTTP %d", res.StatusCode)
	}
	return fmt.Sprintf("HTTP %d: %s", res.StatusCode, msg)
}

func drain(res *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// fileChecksums computes CRC32C (what GCS stores for every object) and MD5 in
// one pass.
func fileChecksums(f *os.File, size int64) (uint32, []byte, error) {
	c := crc32.New(castagnoli)
	m := md5.New()
	if _, err := io.Copy(io.MultiWriter(c, m), io.NewSectionReader(f, 0, size)); err != nil {
		return 0, nil, err
	}
	return c.Sum32(), m.Sum(nil), nil
}
