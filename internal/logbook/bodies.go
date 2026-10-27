package logbook

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/media"
)

var optUnordered = options.InsertMany().SetOrdered(false)

// BodyKey is the media-store key of a long log text: ws/<ws>/logs/<sha[:2]>/<sha>.gz,
// inside the workspace's prefix, so per-machine storage keys can write it.
func BodyKey(workspaceID, sha string) string {
	return fmt.Sprintf("ws/%s/logs/%s/%s.gz", workspaceID, sha[:2], sha)
}

func sum(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func gz(s string) []byte {
	var b bytes.Buffer
	w, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.Bytes()
}

// storeBodies puts a record's long texts where log.prompts says: inline when
// short (workspace), the media store when long, this machine's home (local) or
// nowhere (off). Program error output always stays in the workspace.
func (l *Logger) storeBodies(ctx context.Context, rec *spoolRecord) error {
	if len(rec.Bodies) == 0 {
		return nil
	}
	e := &rec.Entry
	for _, b := range rec.Bodies {
		switch b.Slot {
		case "prompt", "response":
			if e.Claude == nil {
				continue
			}
			var ref *BodyRef
			switch e.Claude.Privacy {
			case PrivacyOff:
				continue
			case PrivacyLocal:
				if l.local.check(l.cfg.LogBodiesDir()) != "" {
					e.Claude.Privacy = PrivacyOff // "this machine only" would not hold in a synced or committed folder
					continue
				}
				r, err := l.putLocal(b.Text, e)
				if err != nil {
					return err
				}
				ref = r
			default:
				if len(b.Text) <= InlineMax {
					if b.Slot == "prompt" {
						e.Claude.Prompt = b.Text
					} else {
						e.Claude.Response = b.Text
					}
					continue
				}
				r, err := l.putWorkspace(ctx, b.Text)
				if err != nil {
					return err
				}
				ref = r
			}
			if b.Slot == "prompt" {
				e.Claude.PromptRef = ref
			} else {
				e.Claude.ResponseRef = ref
			}
		case "stderr":
			if e.Tool == nil {
				continue
			}
			if len(b.Text) <= InlineMax {
				e.Tool.StderrTail = b.Text
				continue
			}
			r, err := l.putWorkspace(ctx, b.Text)
			if err != nil {
				return err
			}
			e.Tool.StderrTail = Tail(b.Text, 600)
			e.Tool.StderrRef = r
		}
	}
	rec.Bodies = nil
	return nil
}

func (l *Logger) putWorkspace(ctx context.Context, text string) (*BodyRef, error) {
	if l.ms == nil {
		return nil, errors.New("no media store")
	}
	sha := sum(text)
	key := BodyKey(l.ws, sha)
	ref := &BodyRef{Store: StoreWorkspace, Key: key, SHA256: sha, Size: len(text)}
	if info, err := l.ms.Stat(ctx, key); err == nil && info.Size > 0 {
		return ref, nil // content-addressed: already stored
	}
	dir := l.cfg.LogSpoolDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	tmp := filepath.Join(dir, ".body-"+hex.EncodeToString(rnd[:])+".gz")
	if err := os.WriteFile(tmp, gz(text), 0o600); err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	if _, err := l.ms.PutFile(ctx, key, tmp, media.PutOptions{ContentType: "application/gzip"}, media.Resume{}, nil); err != nil {
		return nil, err
	}
	return ref, nil
}

// localBodyPath is where a local body lives: <home>/state/log-bodies/<day>/<sha>.gz.
func (l *Logger) putLocal(text string, e *Entry) (*BodyRef, error) {
	sha := sum(text)
	rel := e.At.Format("2006-01-02") + "/" + sha + ".gz"
	p := filepath.Join(l.cfg.LogBodiesDir(), filepath.FromSlash(rel))
	if _, err := os.Stat(p); err != nil {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return nil, err
		}
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, gz(text), 0o600); err != nil {
			return nil, err
		}
		if err := os.Rename(tmp, p); err != nil {
			return nil, err
		}
	}
	return &BodyRef{Store: StoreLocal, Key: rel, SHA256: sha, Size: len(text), InstallationID: l.inst}, nil
}

// ErrElsewhere means a body is kept on another machine (log.prompts: local).
var ErrElsewhere = errors.New("kept on another machine")

// ReadBody returns a long text an entry points at.
func (l *Logger) ReadBody(ctx context.Context, ref *BodyRef) (string, error) {
	if l == nil || ref == nil {
		return "", errors.New("no text")
	}
	var rc io.ReadCloser
	switch ref.Store {
	case StoreLocal:
		if ref.InstallationID != l.inst {
			return "", ErrElsewhere
		}
		if strings.Contains(ref.Key, "..") || strings.HasPrefix(ref.Key, "/") {
			return "", errors.New("invalid body key")
		}
		f, err := os.Open(filepath.Join(l.cfg.LogBodiesDir(), filepath.FromSlash(ref.Key)))
		if err != nil {
			return "", err
		}
		rc = f
	default:
		if l.ms == nil {
			return "", errors.New("no media store")
		}
		if err := media.ValidKey(ref.Key); err != nil || !strings.HasPrefix(ref.Key, "ws/"+l.ws+"/logs/") {
			return "", errors.New("invalid body key")
		}
		r, _, err := l.ms.Get(ctx, ref.Key, 0, -1)
		if err != nil {
			return "", err
		}
		rc = r
	}
	defer rc.Close()
	zr, err := gzip.NewReader(io.LimitReader(rc, 64<<20))
	if err != nil {
		return "", err
	}
	defer zr.Close()
	b, err := io.ReadAll(io.LimitReader(zr, 2*maxBodyBytes))
	if err != nil {
		return "", err
	}
	return string(b), nil
}
