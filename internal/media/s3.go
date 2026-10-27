package media

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// S3Config configures any S3-compatible service.
type S3Config struct {
	Endpoint        string
	Region          string
	Bucket          string
	Prefix          string
	AccessKeyID     string
	SecretAccessKey string
	ForcePathStyle  bool
	HTTPClient      *http.Client // tests
}

// S3Presets documents known providers. Endpoint/region are defaults the installer
// offers; everything stays editable.
var S3Presets = map[string]struct {
	Endpoint, Region, Note string
	PathStyle              bool
}{
	"tigris": {"https://fly.storage.tigris.dev", "auto", "Fly.io's object storage (`fly storage create`). No egress fees.", false},
	"r2":     {"https://<account-id>.r2.cloudflarestorage.com", "auto", "Cloudflare R2. No egress fees.", false},
	"b2":     {"https://s3.<region>.backblazeb2.com", "us-west-004", "Backblaze B2 S3-compatible API.", false},
	"aws":    {"", "us-east-1", "Amazon S3 (endpoint chosen by region).", false},
	"gcs":    {"https://storage.googleapis.com", "auto", "Google Cloud Storage via S3 interoperability (HMAC keys). The native driver (--media gcs) avoids long-lived HMAC keys. Internet egress is billed.", true},
	"custom": {"", "us-east-1", "Any S3-compatible endpoint.", true},
}

// S3 is the S3-compatible driver.
type S3 struct {
	cfg     S3Config
	client  *s3.Client
	presign *s3.PresignClient
}

// minPartSize is S3's minimum for all but the last part.
const minPartSize = 8 << 20

// NewS3 builds the driver. Credentials are passed explicitly — never read from
// ambient AWS profiles, so a misconfigured machine cannot write to the wrong account.
func NewS3(cfg S3Config) (*S3, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("s3 store needs a bucket")
	}
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("s3 store needs an access key id and secret")
	}
	if cfg.Region == "" {
		cfg.Region = "auto"
	}
	cfg.Prefix = strings.Trim(cfg.Prefix, "/")
	awsCfg := aws.Config{
		Region:      cfg.Region,
		Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		// Newer SDKs send CRC checksums on every request, which several
		// S3-compatible services reject; only send them when an API requires it.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	}
	if cfg.HTTPClient != nil {
		awsCfg.HTTPClient = cfg.HTTPClient
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.ForcePathStyle
	})
	return &S3{cfg: cfg, client: client, presign: s3.NewPresignClient(client)}, nil
}

func (s *S3) Name() string { return "s3" }

func (s *S3) Locator() string {
	host := s.cfg.Endpoint
	if u, err := url.Parse(s.cfg.Endpoint); err == nil && u.Host != "" {
		host = u.Host
	}
	loc := "s3://" + s.cfg.Bucket
	if s.cfg.Prefix != "" {
		loc += "/" + s.cfg.Prefix
	}
	if host != "" {
		loc += "@" + host
	}
	return loc
}

func (s *S3) Capabilities() Capabilities {
	return Capabilities{RangeRead: true, PresignedGet: true, ResumableUpload: true, Shared: true}
}

func (s *S3) full(key string) (string, error) {
	if err := ValidKey(key); err != nil {
		return "", err
	}
	if s.cfg.Prefix == "" {
		return key, nil
	}
	return s.cfg.Prefix + "/" + key, nil
}

func isNotFound(err error) bool {
	var nsk *types.NoSuchKey
	var nf *types.NotFound
	if errors.As(err, &nsk) || errors.As(err, &nf) {
		return true
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "NotFound", "NoSuchKey", "404":
			return true
		}
	}
	return false
}

func (s *S3) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	k, err := s.full(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.cfg.Bucket, Key: &k})
	if err != nil {
		if isNotFound(err) {
			return ObjectInfo{}, ErrNotFound
		}
		return ObjectInfo{}, err
	}
	info := ObjectInfo{Key: key, Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType), ETag: aws.ToString(out.ETag)}
	if out.LastModified != nil {
		info.ModTime = *out.LastModified
	}
	return info, nil
}

// PutFile uploads with S3 multipart. On retry it lists the parts the service
// already has and uploads only the rest, so multi-GB uploads never restart.
func (s *S3) PutFile(ctx context.Context, key, path string, opts PutOptions, r Resume, progress Progress) (ObjectInfo, error) {
	k, err := s.full(key)
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
	if info, err := s.Stat(ctx, key); err == nil && info.Size == total {
		if progress != nil {
			progress(total, total, r)
		}
		return info, nil // content-addressed object already present
	}
	meta := map[string]string{}
	if opts.SHA256 != "" {
		meta["sha256"] = opts.SHA256
	}
	ct := opts.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	partSize := int64(minPartSize)
	for total/partSize >= 9000 {
		partSize *= 2
	}
	if total <= partSize {
		sum, err := md5Section(f, 0, total)
		if err != nil {
			return ObjectInfo{}, err
		}
		_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: &s.cfg.Bucket, Key: &k, Body: io.NewSectionReader(f, 0, total), ContentLength: aws.Int64(total),
			ContentType: &ct, ContentMD5: &sum, Metadata: meta,
		})
		if err != nil {
			return ObjectInfo{}, fmt.Errorf("put %s: %w", key, err)
		}
		if progress != nil {
			progress(total, total, r)
		}
		return s.Stat(ctx, key)
	}
	done := map[int32]types.CompletedPart{}
	if r.UploadID != "" && r.PartSize == partSize {
		var marker *string
		for {
			lp, err := s.client.ListParts(ctx, &s3.ListPartsInput{Bucket: &s.cfg.Bucket, Key: &k, UploadId: &r.UploadID, PartNumberMarker: marker})
			if err != nil {
				r.UploadID = "" // upload expired or was aborted: start over
				done = map[int32]types.CompletedPart{}
				break
			}
			for _, p := range lp.Parts {
				done[aws.ToInt32(p.PartNumber)] = types.CompletedPart{ETag: p.ETag, PartNumber: p.PartNumber}
			}
			if !aws.ToBool(lp.IsTruncated) {
				break
			}
			marker = lp.NextPartNumberMarker
		}
	} else {
		r.UploadID = ""
	}
	if r.UploadID == "" {
		cm, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: &s.cfg.Bucket, Key: &k, ContentType: &ct, Metadata: meta})
		if err != nil {
			return ObjectInfo{}, fmt.Errorf("start multipart upload: %w", err)
		}
		r = Resume{UploadID: aws.ToString(cm.UploadId), PartSize: partSize}
		if progress != nil {
			progress(0, total, r)
		}
	}
	nparts := int32((total + partSize - 1) / partSize)
	var bytesDone int64
	for n := int32(1); n <= nparts; n++ {
		if _, ok := done[n]; ok {
			bytesDone += min(partSize, total-int64(n-1)*partSize)
		}
	}
	for n := int32(1); n <= nparts; n++ {
		if _, ok := done[n]; ok {
			continue
		}
		if err := ctx.Err(); err != nil {
			return ObjectInfo{}, err
		}
		off := int64(n-1) * partSize
		size := min(partSize, total-off)
		sum, err := md5Section(f, off, size)
		if err != nil {
			return ObjectInfo{}, err
		}
		pn := n
		up, err := s.client.UploadPart(ctx, &s3.UploadPartInput{
			Bucket: &s.cfg.Bucket, Key: &k, UploadId: &r.UploadID, PartNumber: &pn,
			Body: io.NewSectionReader(f, off, size), ContentLength: aws.Int64(size), ContentMD5: &sum,
		})
		if err != nil {
			return ObjectInfo{}, fmt.Errorf("upload part %d/%d: %w", n, nparts, err)
		}
		done[n] = types.CompletedPart{ETag: up.ETag, PartNumber: &pn}
		bytesDone += size
		r.PartsDone = len(done)
		if progress != nil {
			progress(bytesDone, total, r)
		}
	}
	parts := make([]types.CompletedPart, 0, len(done))
	for _, p := range done {
		parts = append(parts, p)
	}
	sort.Slice(parts, func(i, j int) bool { return aws.ToInt32(parts[i].PartNumber) < aws.ToInt32(parts[j].PartNumber) })
	if _, err := s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: &s.cfg.Bucket, Key: &k, UploadId: &r.UploadID, MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	}); err != nil {
		return ObjectInfo{}, fmt.Errorf("complete multipart upload: %w", err)
	}
	info, err := s.Stat(ctx, key)
	if err != nil {
		return ObjectInfo{}, err
	}
	if info.Size != total {
		return ObjectInfo{}, fmt.Errorf("integrity check failed for %s: stored %d bytes, expected %d", key, info.Size, total)
	}
	return info, nil
}

func md5Section(f *os.File, off, size int64) (string, error) {
	h := md5.New()
	if _, err := io.Copy(h, io.NewSectionReader(f, off, size)); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}

func (s *S3) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, ObjectInfo, error) {
	k, err := s.full(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	in := &s3.GetObjectInput{Bucket: &s.cfg.Bucket, Key: &k}
	if offset > 0 || length >= 0 {
		rng := fmt.Sprintf("bytes=%d-", offset)
		if length >= 0 {
			rng = fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
		}
		in.Range = &rng
	}
	out, err := s.client.GetObject(ctx, in)
	if err != nil {
		if isNotFound(err) {
			return nil, ObjectInfo{}, ErrNotFound
		}
		return nil, ObjectInfo{}, err
	}
	info := ObjectInfo{Key: key, Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType)}
	if out.ContentRange != nil {
		// "bytes a-b/total"
		if i := strings.LastIndex(*out.ContentRange, "/"); i >= 0 {
			var t int64
			if _, err := fmt.Sscan((*out.ContentRange)[i+1:], &t); err == nil {
				info.Size = t
			}
		}
	}
	return out.Body, info, nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	k, err := s.full(key)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.cfg.Bucket, Key: &k})
	if err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

func (s *S3) PresignGet(ctx context.Context, key string, ttl time.Duration, contentType, downloadName string) (string, error) {
	k, err := s.full(key)
	if err != nil {
		return "", err
	}
	in := &s3.GetObjectInput{Bucket: &s.cfg.Bucket, Key: &k}
	if contentType != "" {
		in.ResponseContentType = &contentType
	}
	if downloadName != "" {
		cd := fmt.Sprintf(`attachment; filename="%s"`, strings.ReplaceAll(downloadName, `"`, ""))
		in.ResponseContentDisposition = &cd
	}
	req, err := s.presign.PresignGetObject(ctx, in, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

func (s *S3) ListIncomplete(ctx context.Context) ([]Incomplete, error) {
	in := &s3.ListMultipartUploadsInput{Bucket: &s.cfg.Bucket}
	if s.cfg.Prefix != "" {
		p := s.cfg.Prefix + "/"
		in.Prefix = &p
	}
	var out []Incomplete
	for {
		res, err := s.client.ListMultipartUploads(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, u := range res.Uploads {
			key := strings.TrimPrefix(aws.ToString(u.Key), s.cfg.Prefix+"/")
			inc := Incomplete{Key: key, UploadID: aws.ToString(u.UploadId)}
			if u.Initiated != nil {
				inc.Initiated = *u.Initiated
			}
			out = append(out, inc)
		}
		if !aws.ToBool(res.IsTruncated) {
			return out, nil
		}
		in.KeyMarker, in.UploadIdMarker = res.NextKeyMarker, res.NextUploadIdMarker
	}
}

func (s *S3) AbortIncomplete(ctx context.Context, in Incomplete) error {
	k, err := s.full(in.Key)
	if err != nil {
		return err
	}
	_, err = s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: &s.cfg.Bucket, Key: &k, UploadId: &in.UploadID})
	return err
}
