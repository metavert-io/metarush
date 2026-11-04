package c2pa

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Report is what c2patool read back from a file: whether it carries Content
// Credentials, whether they check out, and who signed them.
type Report struct {
	// None: the file carries no Content Credentials.
	None bool `json:"none,omitempty"`
	// State is c2patool's verdict: Valid (well formed, the signature and the
	// video's hashes match), Trusted (and the signer is on a trust list it was
	// given), Invalid.
	State string `json:"state,omitempty"`
	// Failures are what didn't check out (an unknown signer included: no trust
	// list is given, so every signer is unknown to it).
	Failures []Status `json:"failures,omitempty"`
	Label    string   `json:"label,omitempty"`
	Title    string   `json:"title,omitempty"`
	// Generator is the claim generator's name and version ("MetaRush 0.16.0").
	Generator string `json:"generator,omitempty"`
	// Signer is the signature's certificate as c2patool reports it.
	Signer SignatureInfo `json:"signer"`
	// SourceTypes are the digital source types the actions name.
	SourceTypes []string `json:"sourceTypes,omitempty"`
	Ingredients int      `json:"ingredients"`
}

// Status is one validation code with c2patool's explanation.
type Status struct {
	Code        string `json:"code"`
	Explanation string `json:"explanation,omitempty"`
}

// SignatureInfo is the active manifest's signature_info.
type SignatureInfo struct {
	Alg        string `json:"alg,omitempty"`
	Issuer     string `json:"issuer,omitempty"`
	CommonName string `json:"common_name,omitempty"`
	Serial     string `json:"cert_serial_number,omitempty"`
	// Time is when a timestamp service says it was signed ("" without one).
	Time string `json:"time,omitempty"`
}

// codeUntrusted is the signer being unknown to the verifier (no trust list).
const codeUntrusted = "signingCredential.untrusted"

// ParseReport reads c2patool's JSON report of a file.
func ParseReport(b []byte) (*Report, error) {
	var raw struct {
		Active    string `json:"active_manifest"`
		Manifests map[string]struct {
			Title     string `json:"title"`
			Generator []struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"claim_generator_info"`
			Ingredients []json.RawMessage `json:"ingredients"`
			Assertions  []struct {
				Label string          `json:"label"`
				Data  json.RawMessage `json:"data"`
			} `json:"assertions"`
			Signature SignatureInfo `json:"signature_info"`
		} `json:"manifests"`
		Status []Status `json:"validation_status"`
		State  string   `json:"validation_state"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("c2pa: c2patool's report can't be read: %w", err)
	}
	r := &Report{State: raw.State, Failures: raw.Status, Label: raw.Active}
	m, ok := raw.Manifests[raw.Active]
	if !ok {
		r.None = true
		return r, nil
	}
	r.Title, r.Signer, r.Ingredients = m.Title, m.Signature, len(m.Ingredients)
	if len(m.Generator) > 0 {
		r.Generator = strings.TrimSpace(m.Generator[0].Name + " " + m.Generator[0].Version)
	}
	for _, a := range m.Assertions {
		if !strings.HasPrefix(a.Label, "c2pa.actions") {
			continue
		}
		var acts struct {
			Actions []struct {
				DigitalSourceType string `json:"digitalSourceType"`
			} `json:"actions"`
		}
		_ = json.Unmarshal(a.Data, &acts)
		for _, x := range acts.Actions {
			if x.DigitalSourceType != "" && !contains(r.SourceTypes, x.DigitalSourceType) {
				r.SourceTypes = append(r.SourceTypes, x.DigitalSourceType)
			}
		}
	}
	return r, nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// Check says whether the credentials check out and were signed with c's
// certificate: ok, else why not in plain words. An unknown signer is not a
// failure (no trust list is given; verifiers decide whom they trust).
func (r *Report) Check(c *Credential) (bool, string) {
	if r == nil || r.None {
		return false, "the delivered copy carries no Content Credentials"
	}
	var bad []string
	for _, f := range r.Failures {
		if f.Code != codeUntrusted {
			bad = append(bad, failureWords(f.Code))
		}
	}
	switch {
	case len(bad) > 0:
		return false, "they don't check out: " + strings.Join(dedupe(bad), "; ")
	case r.State != "Valid" && r.State != "Trusted":
		return false, "c2patool didn't find them valid"
	}
	if c != nil && r.Signer.Serial != c.Leaf().SerialNumber.String() {
		return false, "they were signed with another certificate than this computer's"
	}
	return true, ""
}

// failureWords says a validation code in plain words.
func failureWords(code string) string {
	switch {
	case strings.Contains(code, "bmffHash") || strings.Contains(code, "dataHash") || strings.Contains(code, "boxesHash"):
		return "the video's bytes don't match what was signed"
	case strings.HasPrefix(code, "claimSignature"):
		return "the signature doesn't match the credentials"
	case code == "signingCredential.expired":
		return "the signing certificate had run out"
	case code == "signingCredential.revoked":
		return "the signing certificate was revoked"
	case strings.HasPrefix(code, "signingCredential"):
		return "the signing certificate isn't valid for Content Credentials"
	case strings.HasPrefix(code, "assertion.hashedURI"):
		return "part of the credentials was changed after signing"
	case strings.HasPrefix(code, "timeStamp"):
		return "the timestamp doesn't check out"
	}
	return "c2patool reports “" + code + "”"
}

func dedupe(xs []string) []string {
	var out []string
	for _, x := range xs {
		if !contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}
