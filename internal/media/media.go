// Package media defines the canonical media store abstraction and its drivers.
//
// Objects are immutable and content-addressed: the key embeds the SHA-256 of the
// bytes, so writing the same file twice is a no-op and a key never changes meaning.
// MongoDB holds metadata only; bytes live here.
package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ErrNotFound means the object does not exist.
var ErrNotFound = errors.New("media: object not found")

// ErrUnsupported means the driver lacks a capability.
var ErrUnsupported = errors.New("media: operation not supported by this store")

// Capabilities advertises what a driver can do.
type Capabilities struct {
	RangeRead       bool `json:"rangeRead"`
	PresignedGet    bool `json:"presignedGet"`
	ResumableUpload bool `json:"resumableUpload"`
	Shared          bool `json:"shared"` // reachable by every collaborator
}

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Key         string    `json:"key"`
	Size        int64     `json:"size"`
	ContentType string    `json:"contentType,omitempty"`
	ModTime     time.Time `json:"modTime"`
	// ETag changes whenever the object's bytes do (Stat fills it; a store's
	// times are whole seconds, so two writes in one second share one).
	ETag string `json:"etag,omitempty"`
}

// PutOptions are object metadata.
type PutOptions struct {
	ContentType string
	SHA256      string
}

// Resume persists resumable-upload progress between attempts. Drivers fill it in;
// callers save it (in the ingest session) after every Progress call, so an upload
// survives a process restart. S3 uses UploadID/PartSize/PartsDone (multipart);
// GCS uses SessionURI/Offset (a resumable session queried for its committed size).
type Resume struct {
	UploadID   string `json:"uploadId,omitempty"`
	PartSize   int64  `json:"partSize,omitempty"`
	PartsDone  int    `json:"partsDone,omitempty"`
	SessionURI string `json:"sessionUri,omitempty"`
	Offset     int64  `json:"offset,omitempty"`
}

// Progress reports bytes done; it is also the hook to persist Resume.
type Progress func(done, total int64, r Resume)

// Incomplete is an abandoned in-progress upload.
type Incomplete struct {
	Key       string    `json:"key"`
	UploadID  string    `json:"uploadId"`
	Initiated time.Time `json:"initiated"`
}

// Store is a canonical media store.
type Store interface {
	// Name is a short driver name (filesystem, s3).
	Name() string
	// Locator identifies the store without secrets, e.g. "s3://bucket/prefix@host".
	Locator() string
	Capabilities() Capabilities
	// PutFile uploads a local file to key, resuming from r when possible. The object
	// becomes visible only once complete.
	PutFile(ctx context.Context, key, path string, opts PutOptions, r Resume, progress Progress) (ObjectInfo, error)
	// Get streams [offset, offset+length) of key; length < 0 means to the end.
	Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, ObjectInfo, error)
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	Delete(ctx context.Context, key string) error
	// PresignGet returns a short-lived URL for direct (range-capable) reads.
	PresignGet(ctx context.Context, key string, ttl time.Duration, contentType, downloadName string) (string, error)
	// ListIncomplete lists abandoned uploads under the store's prefix.
	ListIncomplete(ctx context.Context) ([]Incomplete, error)
	AbortIncomplete(ctx context.Context, in Incomplete) error
}

// ObjectKey is the content-addressed key for bytes with the given SHA-256.
func ObjectKey(workspaceID, sha256 string) string {
	return fmt.Sprintf("ws/%s/objects/%s/%s", workspaceID, sha256[:2], sha256)
}

// ValidKey rejects anything that could escape a root or address another workspace's
// objects through traversal.
func ValidKey(key string) error {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "\\") {
		return fmt.Errorf("invalid key %q", key)
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("invalid key %q", key)
		}
	}
	return nil
}
