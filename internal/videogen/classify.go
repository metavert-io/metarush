package videogen

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// googleError is the error body of Google APIs. Its code is usually the HTTP
// status as a number, but not always: the free tier's 429 (seen 2026-10-20)
// said "too_many_requests" and had no status or details. Every part is read
// on its own, so one in an unexpected form never hides the rest.
type googleError struct {
	Error struct {
		Code    looseText `json:"code"`
		Message string    `json:"message"`
		Status  string    `json:"status"`
		Details []struct {
			Type       string            `json:"@type"`
			Reason     string            `json:"reason"`
			RetryDelay string            `json:"retryDelay"`
			Metadata   map[string]string `json:"metadata"`
			Violations []struct {
				QuotaMetric string    `json:"quotaMetric"`
				QuotaID     string    `json:"quotaId"`
				QuotaValue  looseText `json:"quotaValue"`
				Subject     string    `json:"subject"`
				Description string    `json:"description"`
			} `json:"violations"`
		} `json:"details"`
	} `json:"error"`
}

// looseText is a value Google sends as a word or a number ("too_many_requests",
// 429, "0"); anything else (a list, an object) reads as nothing.
type looseText string

func (t *looseText) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*t = looseText(s)
		return nil
	}
	var n json.Number
	if json.Unmarshal(b, &n) == nil {
		*t = looseText(n.String())
	}
	return nil
}

// rateCodes are the words Google uses for "too many requests" where a number would be.
var rateCodes = map[string]bool{"too_many_requests": true, "resource_exhausted": true, "rate_limit_exceeded": true, "429": true}

// classifyHTTP turns an error answer into a plain-words Error. phase says
// what was being done: submit, poll, upload, download, cancel, file or key.
func classifyHTTP(status int, body []byte, header http.Header, phase string) *Error {
	var ge googleError
	_ = json.Unmarshal(body, &ge)
	msg := strings.TrimSpace(ge.Error.Message)
	whole := msg
	if msg == "" {
		msg = firstLine(string(body), 300)
		// Not Google's usual form ({"error": "…"}, or plain text): all of it is read for what it says.
		whole = string(body)
		if len(whole) > 8<<10 {
			whole = whole[:8<<10]
		}
	}
	var reasons, quota []string
	var retry time.Duration
	zero := false
	for _, d := range ge.Error.Details {
		if d.Reason != "" {
			reasons = append(reasons, d.Reason)
		}
		for _, v := range d.Violations {
			quota = append(quota, v.QuotaMetric+" "+v.QuotaID+" "+v.Description)
			zero = zero || strings.TrimSpace(string(v.QuotaValue)) == "0"
		}
		for k, v := range d.Metadata {
			if strings.Contains(strings.ToLower(k), "quota") {
				quota = append(quota, v)
			}
			zero = zero || (strings.EqualFold(k, "quota_limit_value") && strings.TrimSpace(v) == "0")
		}
		if d.RetryDelay != "" {
			if dd, err := time.ParseDuration(d.RetryDelay); err == nil {
				retry = dd
			}
		}
	}
	if retry == 0 {
		if s, err := strconv.Atoi(header.Get("Retry-After")); err == nil && s > 0 {
			retry = time.Duration(s) * time.Second
		}
	}
	code := strings.ToLower(strings.TrimSpace(string(ge.Error.Code)))
	parts := []string{strconv.Itoa(status)}
	if code != "" && code != parts[0] {
		parts = append(parts, code) // a word where the number usually is
	}
	var said []string
	for _, p := range append(append(parts, ge.Error.Status, msg), reasons...) {
		if p = strings.TrimSpace(p); p != "" {
			said = append(said, p)
		}
	}
	detail := strings.Join(said, " ")
	e := &Error{Provider: GeminiName, HTTPStatus: status, Detail: firstLine(detail, 600), RetryAfter: retry}
	low := strings.ToLower(whole + " " + strings.Join(reasons, " ") + " " + strings.Join(quota, " "))
	rateLike := status == http.StatusTooManyRequests || ge.Error.Status == "RESOURCE_EXHAUSTED" || rateCodes[code]
	switch {
	case status == http.StatusUnauthorized || strings.Contains(low, "api_key_invalid") || strings.Contains(low, "api key not valid") || strings.Contains(low, "api key expired"):
		e.Kind = KindKey
		e.Message = "Google did not accept the Gemini key. Check that it was copied completely and hasn't been deleted."
		e.Hint = "Add the key again in Settings, Integrations."
	case status < 500 && status != http.StatusNotFound && noVideoTier(e, low, rateLike, zero):
		// The free tier (or a limit of zero): no wait makes it possible, so it is never retried (D178).
	case rateLike:
		if strings.Contains(low, "spend") || strings.Contains(low, "cost") || strings.Contains(low, "budget") || strings.Contains(low, "billing") {
			e.Kind = KindSpend
			e.Message = "Google's spending limit for this key was reached: it may only spend so much in 10 minutes. MetaRush tries again in 10 minutes."
			e.Hint = "Your Google usage tier sets the limit ($10, $50 or $200 in 10 minutes)."
			if e.RetryAfter < 10*time.Minute {
				e.RetryAfter = 10 * time.Minute
			}
		} else {
			e.Kind = KindRate
			e.Message = "Google is getting too many requests from this key right now. MetaRush tries again in a minute."
			if e.RetryAfter == 0 {
				e.RetryAfter = time.Minute
			}
		}
	case (status == http.StatusForbidden || ge.Error.Status == "PERMISSION_DENIED") && strings.Contains(low, "denied access"):
		// Seen live (2026-10-20): "Your project has been denied access. Please contact support."
		e.Kind = KindPermission
		e.Message = "Google has blocked this key's project from the Gemini API. Only Google can unblock it: contact Google support, or use a key from another Google project."
		e.Hint = "Then add the new key in Settings, Integrations. Nothing was made or charged."
	case status == http.StatusForbidden || ge.Error.Status == "PERMISSION_DENIED":
		e.Kind = KindPermission
		e.Message = "This Google key isn't allowed to do that. Making video needs a project with billing turned on (the paid tier), in a country where Gemini Omni is offered."
		e.Hint = "Check the key's project in Google AI Studio, then check the connection in Settings, Integrations."
	case status == http.StatusNotFound && (phase == "submit" || strings.Contains(low, "model")):
		e.Kind = KindRetired
		e.Message = "Google no longer offers this model, or this key can't use it."
		e.Hint = "Choose another model in Settings, Integrations."
	case status == http.StatusNotFound:
		e.Kind = KindNotFound
		e.Message = "Google doesn't have that any more."
	case status < 500 && looksLikeSafety(low):
		e.Kind = KindSafety
		e.Safety = safetyFrom(msg, "input")
		e.Safety.Blocked = true
		e.Message = "Google Gemini declined this request: its safety filters blocked the prompt or a picture."
		e.Hint = "Change the prompt or the pictures. Google doesn't make videos of certain recognizable people."
	case status >= 500:
		e.Kind = KindServer
		e.Message = "Google had a problem on its side. MetaRush tries again by itself."
	default:
		e.Kind = KindInvalid
		e.Message = "Google couldn't use these settings."
		if msg != "" {
			e.Message = fmt.Sprintf("Google couldn't use these settings: %s", firstLine(msg, 200))
		}
	}
	return e
}

// TierLink is Google's page with a key's project tier and its limits (linked from its own free-tier answers).
const TierLink = "https://ai.dev/rate-limit"

var (
	// freeTierRe: what Google says about a project without billing, in its messages ("on Free Tier"), quota names
	// (generate_content_free_tier_requests, …PerMinute-FreeTier) and reasons (BILLING_DISABLED).
	freeTierRe = regexp.MustCompile(`free[\s_-]?tier|billing_disabled|billed (?:users|accounts|projects)|requires billing|billing to be enabled|enable billing|without (?:a )?billing|no billing account`)
	// zeroLimitRe: a limit of zero ("limit: 0 input tokens per minute", "limit 0", "limit of 0"), never 0.5 or 05.
	zeroLimitRe = regexp.MustCompile(`\blimit\b\s*(?:of|is|=|:)?\s*0(?:$|[^0-9.]|\.(?:$|[^0-9]))`)
)

// noVideoTier recognizes an answer that says the key's project can't make
// video at all: its free tier, or a limit of zero (the 429 only counts as a
// limit of zero when it is about too many requests). It fills in e and
// reports whether it did. Both are said plainly and never retried: Google
// answered every status check this way for a free-tier project, so waiting
// kept a shot at "being made" for ever (2026-10-20, D178).
func noVideoTier(e *Error, low string, rateLike, zero bool) bool {
	free := freeTierRe.MatchString(low)
	if !free && !(rateLike && (zero || zeroLimitRe.MatchString(low))) {
		return false
	}
	e.Kind, e.RetryAfter = KindTier, 0
	e.Message = "This Google project is on the free tier, which can't make video. Turn on billing for the project in Google AI Studio, then try again."
	if !free {
		e.Message = "Google gives this key's project no allowance for making video (its limit is zero). Turn on billing or raise the project's tier in Google AI Studio, then try again."
	}
	e.Hint = "Google AI Studio shows the project's tier and its limits: " + TierLink + ". Nothing was made or charged."
	return true
}

// tierFailure is noVideoTier for a request Google reports as failed in its
// status (not as an error answer): the same words, the same handling.
func tierFailure(text string) *Error {
	e := &Error{Provider: GeminiName, Detail: firstLine(text, 600)}
	if !noVideoTier(e, strings.ToLower(text), true, false) {
		return nil
	}
	return e
}

var safetyWords = []string{"safety", "blocked", "content-safety", "content policy", "content_policy", "usage polic", "prohibited", "responsible ai",
	"recognizable", "not allowed to generate", "can't generate", "cannot generate", "unable to generate", "violat", "celebrit", "public figure", "minor"}

// looksLikeSafety reports whether a message is about the safety filters.
func looksLikeSafety(low string) bool {
	low = strings.ToLower(low)
	for _, w := range safetyWords {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

var (
	supportCodeRe = regexp.MustCompile(`\b\d{8}\b`)
	filteredRe    = regexp.MustCompile(`(?i)(\d+)\s+(?:video|videos|item|items|output|outputs)?\s*(?:were|was)?\s*filtered`)
)

// safetyFrom reads what a block says: support codes, how many were filtered, what kind.
func safetyFrom(text, side string) *Safety {
	s := &Safety{Side: side, Message: firstLine(text, 400)}
	seen := map[string]bool{}
	for _, c := range supportCodeRe.FindAllString(text, -1) {
		if !seen[c] {
			seen[c] = true
			s.SupportCodes = append(s.SupportCodes, c)
		}
	}
	if m := filteredRe.FindStringSubmatch(text); m != nil {
		s.FilteredCount, _ = strconv.Atoi(m[1])
	}
	low := strings.ToLower(text)
	switch {
	case strings.Contains(low, "child") || strings.Contains(low, "minor"):
		s.Category = "child"
	case strings.Contains(low, "celebrit") || strings.Contains(low, "public figure") || strings.Contains(low, "prominent") || strings.Contains(low, "politic"):
		s.Category = "public-figure"
	case strings.Contains(low, "recognizable") || strings.Contains(low, "person") || strings.Contains(low, "people") || strings.Contains(low, "face") || strings.Contains(low, "likeness"):
		s.Category = "person"
	case strings.Contains(low, "sexual"):
		s.Category = "sexual"
	case strings.Contains(low, "violen") || strings.Contains(low, "gore"):
		s.Category = "violence"
	default:
		s.Category = "other"
	}
	return s
}
