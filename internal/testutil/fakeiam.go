package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// FakeIAM emulates the subset of Tigris's IAM API that `metarush machine` uses.
// It records keys and policies so tests can assert what was created and deleted.
type FakeIAM struct {
	URL      string
	mu       sync.Mutex
	Keys     map[string]*FakeKey
	Policies map[string]*FakePolicy // by ARN
	Calls    []string
	n        int
}

// FakeKey is a minted key.
type FakeKey struct {
	ID, Secret, Name string
	Roles            []map[string]string
	Scope            int
	Policies         []string // ARNs
	Created          time.Time
}

// FakePolicy is a created policy.
type FakePolicy struct {
	Name, Arn, Document string
}

// FakeTigrisIAM starts the fake server. Requests must carry a SigV4
// Authorization header (the signature itself is not verified).
func FakeTigrisIAM(t testing.TB) *FakeIAM {
	t.Helper()
	f := &FakeIAM{Keys: map[string]*FakeKey{}, Policies: map[string]*FakePolicy{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

// Live returns the IDs of keys that still exist.
func (f *FakeIAM) Live() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id := range f.Keys {
		out = append(out, id)
	}
	return out
}

// Key returns a key by ID (nil when deleted).
func (f *FakeIAM) Key(id string) *FakeKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Keys[id]
}

// PolicyCount reports how many policies exist.
func (f *FakeIAM) PolicyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Policies)
}

func (f *FakeIAM) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") || r.Header.Get("X-Amz-Content-Sha256") == "" {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"Message":"missing signature"}`))
		return
	}
	_ = r.ParseForm()
	action := r.URL.Query().Get("Action")
	if action == "" {
		action = r.PostForm.Get("Action")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, action)
	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	switch action {
	case "CreateAccessKeyWithBucketsRole":
		var req struct {
			Name  string              `json:"name"`
			Roles []map[string]string `json:"buckets_role"`
			Scope int                 `json:"access_key_scope"`
		}
		_ = json.Unmarshal([]byte(r.PostForm.Get("Req")), &req)
		f.n++
		k := &FakeKey{ID: fmt.Sprintf("tid_fake%06dkey", f.n), Secret: fmt.Sprintf("tsec_fake%06dsecret", f.n), Name: req.Name, Roles: req.Roles, Scope: req.Scope, Created: time.Now().UTC()}
		f.Keys[k.ID] = k
		reply(map[string]any{"CreateAccessKeyResult": map[string]any{"AccessKey": map[string]any{"AccessKeyId": k.ID, "SecretAccessKey": k.Secret, "UserName": k.Name, "Status": "active"}}})
	case "ListAccessKeys":
		want := r.PostForm.Get("KeyId")
		keys := []map[string]any{}
		for _, k := range f.Keys {
			if want != "" && k.ID != want {
				continue
			}
			keys = append(keys, map[string]any{"access_key_id": k.ID, "username": k.Name, "created_at": k.Created.UnixMilli(), "status": "active", "buckets_role": k.Roles, "access_key_scope": k.Scope})
		}
		reply(map[string]any{"Keys": keys, "Marker": "", "IsTruncated": false})
	case "DeleteAccessKey":
		id := r.PostForm.Get("AccessKeyId")
		if _, ok := f.Keys[id]; !ok {
			w.WriteHeader(http.StatusNotFound)
			reply(map[string]any{"Message": "access key not found"})
			return
		}
		delete(f.Keys, id)
		reply(map[string]any{})
	case "CreatePolicy":
		name := r.PostForm.Get("PolicyName")
		arn := "arn:aws:iam::fake:policy/" + name
		if _, dup := f.Policies[arn]; dup {
			w.WriteHeader(http.StatusConflict)
			reply(map[string]any{"Message": "policy exists"})
			return
		}
		f.Policies[arn] = &FakePolicy{Name: name, Arn: arn, Document: r.PostForm.Get("PolicyDocument")}
		reply(map[string]any{"CreatePolicyResult": map[string]any{"Policy": map[string]any{"Arn": arn, "PolicyName": name}}})
	case "AttachUserPolicy":
		k, ok := f.Keys[r.PostForm.Get("UserName")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			reply(map[string]any{"Message": "key not found"})
			return
		}
		k.Policies = append(k.Policies, r.PostForm.Get("PolicyArn"))
		reply(map[string]any{})
	case "ListUserPolicies":
		var names []string
		if k, ok := f.Keys[r.PostForm.Get("UserName")]; ok {
			for _, arn := range k.Policies {
				if p, ok := f.Policies[arn]; ok {
					names = append(names, p.Name)
				}
			}
		}
		reply(map[string]any{"ListUserPoliciesResult": map[string]any{"PolicyNames": names}})
	case "ListPolicies":
		var ps []map[string]string
		for _, p := range f.Policies {
			ps = append(ps, map[string]string{"PolicyName": p.Name, "Arn": p.Arn})
		}
		reply(map[string]any{"ListPoliciesResult": map[string]any{"Policies": ps}})
	case "ForceDeletePolicy":
		delete(f.Policies, r.PostForm.Get("PolicyArn"))
		reply(map[string]any{})
	default:
		w.WriteHeader(http.StatusBadRequest)
		reply(map[string]any{"Message": "unsupported action " + action})
	}
}
