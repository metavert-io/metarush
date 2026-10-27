package machines

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// DefaultTigrisIAMEndpoint is the IAM API the Tigris CLI and SDKs use. Keys
// created by `fly storage create` work against it.
const DefaultTigrisIAMEndpoint = "https://iam.storageapi.dev"

// Tigris bucket roles (cumulative). ReadWrite is the least privilege MetaRush
// needs: object reads/writes/deletes and multipart uploads, no bucket settings.
const (
	RoleReadOnly  = "ReadOnly"
	RoleReadWrite = "ReadWrite"
	RoleEditor    = "Editor"
)

// Access key scopes: "standard" keys also get organization-wide default
// operations (list/create buckets, create keys and policies);
// "no_default_allow" keys can do only what their bucket role allows.
const (
	ScopeStandard       = "standard"
	ScopeNoDefaultAllow = "no_default_allow"
)

var scopeCode = map[string]int{ScopeStandard: 0, ScopeNoDefaultAllow: 1}

// Tigris is a client for Tigris's IAM API: it mints, inspects and deletes
// bucket-scoped access keys and attaches IAM policies. Requests are SigV4-signed
// with an existing key that may manage keys (the bucket owner's key, or an
// organization admin key).
type Tigris struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	HTTP            *http.Client
}

// BucketRole grants one role on one bucket.
type BucketRole struct {
	Bucket string `json:"bucket"`
	Role   string `json:"role"`
}

// TigrisKey describes an access key (never its secret).
type TigrisKey struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Status    string       `json:"status"`
	CreatedAt time.Time    `json:"createdAt"`
	Roles     []BucketRole `json:"roles"`
	Scope     string       `json:"scope"`
}

// NewKey is a freshly minted key; Secret is shown exactly once.
type NewKey struct {
	TigrisKey
	Secret string `json:"-"`
}

// TigrisError is an IAM API failure.
type TigrisError struct {
	Status  int
	Message string
}

func (e *TigrisError) Error() string {
	return fmt.Sprintf("tigris IAM: HTTP %d: %s", e.Status, e.Message)
}

// IsForbidden reports an authorization failure (the key may not manage keys).
func IsForbidden(err error) bool {
	var te *TigrisError
	return errors.As(err, &te) && (te.Status == http.StatusForbidden || te.Status == http.StatusUnauthorized)
}

func (t *Tigris) endpoint() string {
	if t.Endpoint != "" {
		return strings.TrimRight(t.Endpoint, "/")
	}
	return DefaultTigrisIAMEndpoint
}

func (t *Tigris) client() *http.Client {
	if t.HTTP != nil {
		return t.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// call POSTs a form to path (which carries the Action query) and decodes JSON.
func (t *Tigris) call(ctx context.Context, path string, form url.Values, out any) error {
	if t.AccessKeyID == "" || t.SecretAccessKey == "" {
		return errors.New("tigris IAM: no credentials")
	}
	body := []byte(form.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint()+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	sum := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(sum[:])
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	creds := aws.Credentials{AccessKeyID: t.AccessKeyID, SecretAccessKey: t.SecretAccessKey}
	if err := v4.NewSigner().SignHTTP(ctx, creds, req, payloadHash, "s3", "auto", time.Now().UTC()); err != nil {
		return err
	}
	res, err := t.client().Do(req)
	if err != nil {
		return fmt.Errorf("tigris IAM: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		return &TigrisError{Status: res.StatusCode, Message: errorMessage(raw, res.Status)}
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("tigris IAM: unexpected response (%d bytes): %w", len(raw), err)
	}
	return nil
}

func errorMessage(raw []byte, status string) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		for _, k := range []string{"Message", "message", "error"} {
			if s, ok := m[k].(string); ok && s != "" {
				return s
			}
		}
		if e, ok := m["Error"].(map[string]any); ok {
			if s, ok := e["Message"].(string); ok {
				return s
			}
		}
	}
	s := strings.TrimSpace(string(raw))
	if i := strings.Index(s, "<Message>"); i >= 0 {
		if j := strings.Index(s[i:], "</Message>"); j > 0 {
			return s[i+9 : i+j]
		}
	}
	if len(s) > 300 {
		s = s[:300]
	}
	if s == "" {
		return status
	}
	return s
}

type statusResp struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

func (s statusResp) err(what string) error {
	if s.Status != "" && s.Status != "success" {
		return &TigrisError{Status: 400, Message: what + ": " + firstNonEmpty(s.Message, s.Status)}
	}
	return nil
}

// CreateKey mints a named key with per-bucket roles and a scope.
func (t *Tigris) CreateKey(ctx context.Context, name string, roles []BucketRole, scope string) (*NewKey, error) {
	if scope == "" {
		scope = ScopeStandard
	}
	code, ok := scopeCode[scope]
	if !ok {
		return nil, fmt.Errorf("unknown key scope %q", scope)
	}
	if roles == nil {
		roles = []BucketRole{}
	}
	req, _ := json.Marshal(map[string]any{"req_uuid": uuid(), "name": name, "buckets_role": roles, "access_key_scope": code})
	var out struct {
		statusResp
		CreateAccessKeyResult struct {
			AccessKey struct {
				AccessKeyID     string `json:"AccessKeyId"`
				SecretAccessKey string `json:"SecretAccessKey"`
				UserName        string `json:"UserName"`
				CreateDate      string `json:"CreateDate"`
			} `json:"AccessKey"`
		} `json:"CreateAccessKeyResult"`
	}
	if err := t.call(ctx, "/?Action=CreateAccessKeyWithBucketsRole", url.Values{"Req": {string(req)}}, &out); err != nil {
		return nil, err
	}
	if err := out.err("create access key"); err != nil {
		return nil, err
	}
	k := out.CreateAccessKeyResult.AccessKey
	if k.AccessKeyID == "" || k.SecretAccessKey == "" {
		return nil, errors.New("tigris IAM: create access key returned no credentials")
	}
	created, _ := time.Parse(time.RFC3339, k.CreateDate)
	if created.IsZero() {
		created = time.Now().UTC()
	}
	return &NewKey{TigrisKey: TigrisKey{ID: k.AccessKeyID, Name: firstNonEmpty(k.UserName, name), Status: "active", CreatedAt: created, Roles: roles, Scope: scope}, Secret: k.SecretAccessKey}, nil
}

type listedKey struct {
	ID        string          `json:"access_key_id"`
	Name      string          `json:"username"`
	CreatedAt json.RawMessage `json:"created_at"` // epoch milliseconds (or an RFC 3339 string)
	Status    string          `json:"status"`
	Roles     []BucketRole    `json:"buckets_role"`
	Scope     *int            `json:"access_key_scope"`
}

func (k listedKey) key() TigrisKey {
	out := TigrisKey{ID: k.ID, Name: k.Name, Status: k.Status, Roles: k.Roles, Scope: ScopeStandard}
	if k.Scope != nil && *k.Scope == 1 {
		out.Scope = ScopeNoDefaultAllow
	}
	var ms int64
	var s string
	if json.Unmarshal(k.CreatedAt, &ms) == nil && ms > 0 {
		out.CreatedAt = time.UnixMilli(ms).UTC()
	} else if json.Unmarshal(k.CreatedAt, &s) == nil {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			out.CreatedAt = t
		}
	}
	if out.Roles == nil {
		out.Roles = []BucketRole{}
	}
	return out
}

type listResp struct {
	Keys        []listedKey `json:"Keys"`
	Marker      string      `json:"Marker"`
	IsTruncated bool        `json:"IsTruncated"`
}

// GetKey returns one key's details (name, roles, scope), or ErrKeyNotFound.
func (t *Tigris) GetKey(ctx context.Context, id string) (*TigrisKey, error) {
	var out listResp
	if err := t.call(ctx, "/?Detailed", url.Values{"Action": {"ListAccessKeys"}, "KeyId": {id}}, &out); err != nil {
		return nil, err
	}
	for _, k := range out.Keys {
		if k.ID == id {
			kk := k.key()
			return &kk, nil
		}
	}
	return nil, ErrKeyNotFound
}

// ErrKeyNotFound means the key does not exist (or is not visible to the caller).
var ErrKeyNotFound = errors.New("access key not found")

// ListKeys lists the keys the caller may see: all keys for an organization
// admin, otherwise the keys the caller created.
func (t *Tigris) ListKeys(ctx context.Context) ([]TigrisKey, error) {
	var keys []TigrisKey
	marker := ""
	for page := 0; page < 50; page++ {
		form := url.Values{"Action": {"ListAccessKeys"}, "MaxItems": {"1000"}}
		if marker != "" {
			form.Set("Marker", marker)
		}
		var out listResp
		if err := t.call(ctx, "/?Detailed", form, &out); err != nil {
			return nil, err
		}
		for _, k := range out.Keys {
			keys = append(keys, k.key())
		}
		if !out.IsTruncated || out.Marker == "" {
			break
		}
		marker = out.Marker
	}
	return keys, nil
}

// SetRoles replaces a key's bucket roles (an empty list removes all of them).
func (t *Tigris) SetRoles(ctx context.Context, id string, roles []BucketRole) error {
	if roles == nil {
		roles = []BucketRole{}
	}
	req, _ := json.Marshal(map[string]any{"id": id, "buckets_role": roles})
	var out statusResp
	if err := t.call(ctx, "/?Action=UpdateAccessKeyWithBucketsRole", url.Values{"Req": {string(req)}}, &out); err != nil {
		return err
	}
	return out.err("update key roles")
}

// DeleteKey permanently deletes a key; access stops immediately.
func (t *Tigris) DeleteKey(ctx context.Context, id string) error {
	return t.call(ctx, "/?Action=DeleteAccessKey", url.Values{"Action": {"DeleteAccessKey"}, "Version": {"2010-05-08"}, "AccessKeyId": {id}}, nil)
}

// CreatePolicy creates an IAM policy and returns its ARN.
func (t *Tigris) CreatePolicy(ctx context.Context, name, description string, doc any) (string, error) {
	b, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	form := url.Values{"Action": {"CreatePolicy"}, "Version": {"2010-05-08"}, "PolicyName": {name}, "PolicyDocument": {string(b)}}
	if description != "" {
		form.Set("Description", description)
	}
	var out struct {
		CreatePolicyResult struct {
			Policy struct {
				Arn string `json:"Arn"`
			} `json:"Policy"`
		} `json:"CreatePolicyResult"`
		Policy struct {
			Arn string `json:"Arn"`
		} `json:"Policy"`
	}
	if err := t.call(ctx, "/?Action=CreatePolicy", form, &out); err != nil {
		return "", err
	}
	arn := firstNonEmpty(out.CreatePolicyResult.Policy.Arn, out.Policy.Arn)
	if arn == "" {
		return "", errors.New("tigris IAM: create policy returned no ARN")
	}
	return arn, nil
}

// AttachPolicy attaches a policy to an access key.
func (t *Tigris) AttachPolicy(ctx context.Context, keyID, policyArn string) error {
	return t.call(ctx, "/?Action=AttachUserPolicy", url.Values{"Action": {"AttachUserPolicy"}, "Version": {"2010-05-08"}, "UserName": {keyID}, "PolicyArn": {policyArn}}, nil)
}

// DeletePolicy deletes a policy, detaching it from any key first.
func (t *Tigris) DeletePolicy(ctx context.Context, policyArn string) error {
	return t.call(ctx, "/?Action=ForceDeletePolicy", url.Values{"Action": {"ForceDeletePolicy"}, "Version": {"2010-05-08"}, "PolicyArn": {policyArn}}, nil)
}

// Policy is an IAM policy's name and ARN.
type Policy struct {
	Name string `json:"name"`
	Arn  string `json:"arn"`
}

// ListPolicies lists the policies the caller can see.
func (t *Tigris) ListPolicies(ctx context.Context) ([]Policy, error) {
	var out struct {
		ListPoliciesResult struct {
			Policies []struct {
				PolicyName string `json:"PolicyName"`
				Arn        string `json:"Arn"`
			} `json:"Policies"`
		} `json:"ListPoliciesResult"`
	}
	if err := t.call(ctx, "/?Action=ListPolicies", url.Values{"Action": {"ListPolicies"}, "Version": {"2010-05-08"}}, &out); err != nil {
		return nil, err
	}
	var ps []Policy
	for _, p := range out.ListPoliciesResult.Policies {
		ps = append(ps, Policy{Name: p.PolicyName, Arn: p.Arn})
	}
	return ps, nil
}

// ListKeyPolicies lists the names of the policies attached to a key.
func (t *Tigris) ListKeyPolicies(ctx context.Context, keyID string) ([]string, error) {
	var out struct {
		ListUserPoliciesResult struct {
			PolicyNames []string `json:"PolicyNames"`
		} `json:"ListUserPoliciesResult"`
	}
	if err := t.call(ctx, "/?Action=ListUserPolicies", url.Values{"Action": {"ListUserPolicies"}, "Version": {"2010-05-08"}, "UserName": {keyID}}, &out); err != nil {
		return nil, err
	}
	return out.ListUserPoliciesResult.PolicyNames, nil
}

// PrefixPolicy grants object access under bucket/prefix/* only (plus listing
// in-progress uploads, which multipart resume needs).
func PrefixPolicy(bucket, prefix string) map[string]any {
	prefix = strings.Trim(prefix, "/")
	return map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{"Sid": "ObjectsUnderPrefix", "Effect": "Allow", "Action": []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"},
				"Resource": []string{"arn:aws:s3:::" + bucket + "/" + prefix + "/*"}},
			{"Sid": "BucketListing", "Effect": "Allow", "Action": []string{"s3:ListBucket", "s3:ListBucketMultipartUploads"},
				"Resource": []string{"arn:aws:s3:::" + bucket}},
		},
	}
}

func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// MaskKeyID shortens an access key ID for display (it is an identifier, not a
// secret, but there is no reason to print it whole).
func MaskKeyID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:8] + "…" + id[len(id)-4:]
}
