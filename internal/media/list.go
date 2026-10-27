package media

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"google.golang.org/api/iterator"
)

// Lister is a store that can enumerate its objects (moving a workspace's
// media to another store, `metarush storage migrate`).
type Lister interface {
	// List calls fn for every object whose key starts with prefix, in key order.
	List(ctx context.Context, prefix string, fn func(ObjectInfo) error) error
}

// List walks the files under the root (unfinished uploads in .staging are not objects).
func (f *FS) List(ctx context.Context, prefix string, fn func(ObjectInfo) error) error {
	// Start from the deepest folder the prefix names.
	dir := f.Root
	if i := strings.LastIndex(prefix, "/"); i > 0 {
		if err := ValidKey(prefix[:i]); err != nil {
			return err
		}
		dir = filepath.Join(f.Root, filepath.FromSlash(prefix[:i]))
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		rel, rerr := filepath.Rel(f.Root, p)
		if rerr != nil {
			return rerr
		}
		key := filepath.ToSlash(rel)
		if d.IsDir() {
			if key == ".staging" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasPrefix(key, prefix) || strings.HasPrefix(path.Base(key), ".") {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		return fn(ObjectInfo{Key: key, Size: info.Size(), ModTime: info.ModTime()})
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// List pages through ListObjectsV2 under the store's prefix.
func (s *S3) List(ctx context.Context, prefix string, fn func(ObjectInfo) error) error {
	full := prefix
	if s.cfg.Prefix != "" {
		full = s.cfg.Prefix + "/" + prefix
	}
	pg := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: &s.cfg.Bucket, Prefix: &full})
	for pg.HasMorePages() {
		page, err := pg.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, o := range page.Contents {
			key := aws.ToString(o.Key)
			if s.cfg.Prefix != "" {
				key = strings.TrimPrefix(key, s.cfg.Prefix+"/")
			}
			info := ObjectInfo{Key: key, Size: aws.ToInt64(o.Size)}
			if o.LastModified != nil {
				info.ModTime = *o.LastModified
			}
			if err := fn(info); err != nil {
				return err
			}
		}
	}
	return nil
}

// List iterates the bucket's objects under the store's prefix.
func (g *GCS) List(ctx context.Context, prefix string, fn func(ObjectInfo) error) error {
	full := prefix
	if g.cfg.Prefix != "" {
		full = g.cfg.Prefix + "/" + prefix
	}
	it := g.bkt.Objects(ctx, &storage.Query{Prefix: full})
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			return err
		}
		key := attrs.Name
		if g.cfg.Prefix != "" {
			key = strings.TrimPrefix(key, g.cfg.Prefix+"/")
		}
		if err := fn(ObjectInfo{Key: key, Size: attrs.Size, ContentType: attrs.ContentType, ModTime: attrs.Updated}); err != nil {
			return err
		}
	}
}
