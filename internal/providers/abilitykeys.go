package providers

import (
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/apperr"
)

// An ability may name a key of one of MetaRush's own integrations (D235): the
// key then goes only where that integration's key goes. Each integration says
// so itself (KeyHosts), so a new one can't be forgotten: an integration that
// doesn't say where its key may go never lends it, and one whose key is a
// private key (a service account's, D282) never does.

// KeyHosts is an integration whose key an ability's requests may carry, to
// these hosts only.
type KeyHosts interface {
	KeyHosts() []string
}

// AbilityKeyHosts says where an ability may send a key it names: for a key
// of MetaRush's own integrations, only the integration's hosts (integration
// true); for any other name, wherever the manifest says (nil, false). A key
// that is a private key, or of an integration that names no hosts, is
// refused.
func AbilityKeyHosts(name string) (hosts []string, integration bool, err error) {
	p, gerr := Get(strings.ToLower(strings.TrimSpace(name)))
	if gerr != nil {
		return nil, false, nil
	}
	if err := NotLent(p, "an ability's requests"); err != nil {
		return nil, true, err
	}
	kh, ok := p.(KeyHosts)
	if !ok || len(kh.KeyHosts()) == 0 {
		return nil, true, apperr.New(apperr.InvalidInput, "MetaRush doesn't know where its %s key may go, so it doesn't lend it to abilities.", p.Title())
	}
	return kh.KeyHosts(), true, nil
}

// NotLent refuses an integration whose key is a private key (a service
// account's, D282): MetaRush signs in with it itself, and it never goes into
// code that isn't MetaRush's (to: "an ability's requests", "a refresh's
// steps").
func NotLent(p Provider, to string) error {
	if _, private := p.(KeyShaper); private {
		return apperr.New(apperr.InvalidInput, "MetaRush's %s key is a private key that never leaves MetaRush, so it can't go into %s.", p.Title(), to)
	}
	return nil
}

// ServiceHosts is an integration whose service lives at hosts its key never
// goes to for an ability (a private key's, D282): Content Settings that rule
// it out still rule out an ability that calls those hosts. A host starting
// with "*" stands for any host ending in the rest.
type ServiceHosts interface {
	ServiceHosts() []string
}

// IntegrationsFor are the integrations whose service a manifest's host
// reaches or is part of: one of their hosts, a name under one, a domain they
// are under ("elevenlabs.io"), or, for "*.googleapis.com", every integration
// with a host under it (Gemini and Vertex AI). A port is ignored.
func IntegrationsFor(host string) []string {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	var out []string
	for _, p := range All() {
		var hs []string
		if kh, ok := p.(KeyHosts); ok {
			hs = append(hs, kh.KeyHosts()...)
		}
		if sh, ok := p.(ServiceHosts); ok {
			hs = append(hs, sh.ServiceHosts()...)
		}
		for _, h := range hs {
			if sameService(host, h) {
				out = append(out, p.Name())
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// sameService: a manifest's host m (maybe "*.d") and an integration's host
// i (maybe "*X": any host ending in X) share a service when one is under
// the other.
func sameService(m, i string) bool {
	d := strings.TrimPrefix(m, "*.")
	if d == "" {
		return false
	}
	if rest, ok := strings.CutPrefix(i, "*"); ok {
		return (strings.HasSuffix(d, rest) && len(d) > len(rest)) || strings.HasSuffix(rest, "."+d)
	}
	return d == i || strings.HasSuffix(i, "."+d) || strings.HasSuffix(d, "."+i)
}

// KeyHosts are ElevenLabs' API hosts, every region's.
func (ElevenLabs) KeyHosts() []string {
	var out []string
	for _, base := range elevenHosts {
		if u, err := url.Parse(base); err == nil {
			out = append(out, u.Hostname())
		}
	}
	sort.Strings(out)
	return out
}
