package aiwork

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiservices/local"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/localmodels"
	"github.com/metavert-io/metarush/internal/ops"
)

// Models on this computer (D278–D280): which there are, whether each can run
// here, and setting up the pinned files one needs — only when a person asks.

// LocalIn lists the models that run on this computer.
type LocalIn struct {
	Kind string `json:"kind,omitempty" jsonschema:"enum=,enum=voice,enum=transcription,enum=background-removal,enum=upscale"`
}

// LocalDownload is one pinned download a model needs.
type LocalDownload struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	What       string `json:"what"`
	Bytes      int64  `json:"bytes"`
	Size       string `json:"size" jsonschema:"description=Its size in words: 334 MB."`
	Ready      bool   `json:"ready" jsonschema:"description=It is set up on this computer and unchanged."`
	Licence    string `json:"licence"`
	LicenceURL string `json:"licenceUrl"`
	URL        string `json:"url"`
	SHA256     string `json:"sha256"`
}

// LocalModelView is one model that runs on this computer.
type LocalModelView struct {
	Kind      string `json:"kind"`
	KindTitle string `json:"kindTitle"`
	Ref       string `json:"ref" jsonschema:"description=provider/model\\, for --model."`
	Title     string `json:"title"`
	Ready     bool   `json:"ready" jsonschema:"description=It can run on this computer now."`
	// NotHere says why not, and how to set it up.
	NotHere   string             `json:"notHere,omitempty"`
	Downloads []LocalDownload    `json:"downloads"`
	Licence   aiservices.Licence `json:"licence"`
	Notes     []string           `json:"notes,omitempty"`
}

// LocalOut lists the models that run on this computer.
type LocalOut struct {
	Models  []LocalModelView `json:"models"`
	Summary string           `json:"summary"`
}

// LocalSetupIn sets a model on this computer up.
type LocalSetupIn struct {
	Model string `json:"model" jsonschema:"minLength=1,maxLength=120,description=The model (local/kokoro-v1\\, kokoro-v1) or one of its downloads (voices-program)."`
}

// LocalSetupOut is a model after setting it up.
type LocalSetupOut struct {
	Model   LocalModelView `json:"model"`
	Fetched []string       `json:"fetched,omitempty" jsonschema:"description=The downloads made now (the rest were set up before)."`
	Summary string         `json:"summary"`
}

func init() {
	ops.Def("ai.local", "List the AI models that run on this computer — free, no key, nothing leaves it — with whether each is set up and what setting it up downloads", ops.Flags{},
		func(ctx context.Context, a *app.App, in *LocalIn) (*LocalOut, error) { return localOut(a, in.Kind) })
	ops.Def("ai.local.setup", "Set up an AI model that runs on this computer: download its pinned files once, checked by their SHA-256 (people only)", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *LocalSetupIn) (*LocalSetupOut, error) {
			return localSetup(ctx, a, in)
		})
}

func localEnv(a *app.App) aiservices.Env {
	return aiservices.Env{Tools: a.Tools, Cfg: a.Cfg, Home: a.Cfg.Home()}
}

func localView(a *app.App, m aiservices.Model) LocalModelView {
	e := localEnv(a)
	k, _ := aiservices.KindOf(m.Kind)
	v := LocalModelView{Kind: m.Kind, KindTitle: k.Title, Ref: m.Ref(), Title: m.Title, Licence: m.Licence, Notes: m.Notes, Downloads: []LocalDownload{}}
	s := local.Stores(e)
	for _, id := range local.Downloads(m.Kind, m.ID) {
		p, ok := s.Find(id)
		if !ok {
			continue
		}
		v.Downloads = append(v.Downloads, LocalDownload{ID: p.ID, Title: p.Title, What: p.What, Bytes: p.Bytes, Size: localmodels.Size(p.Bytes), Ready: s.Ready(p) == nil,
			Licence: p.Licence, LicenceURL: p.LicenceURL, URL: p.URL, SHA256: p.SHA256})
	}
	if err := aiservices.ReadyHere(e, m.Provider, m.Kind, m.ID); err != nil {
		v.NotHere = apperrMessage(err)
	} else {
		v.Ready = true
	}
	return v
}

func localModels(kind string) []aiservices.Model {
	var out []aiservices.Model
	for _, k := range aiservices.Kinds() {
		if kind != "" && k.Kind != kind {
			continue
		}
		for _, m := range aiservices.ModelsOf(k.Kind) {
			if aiservices.IsLocal(m.Provider) {
				out = append(out, m)
			}
		}
	}
	return out
}

func localOut(a *app.App, kind string) (*LocalOut, error) {
	out := &LocalOut{Models: []LocalModelView{}}
	ready := 0
	for _, m := range localModels(kind) {
		v := localView(a, m)
		if v.Ready {
			ready++
		}
		out.Models = append(out.Models, v)
	}
	if kind != "" && len(out.Models) == 0 {
		return nil, apperr.New(apperr.NotFound, "No AI models of that kind run on this computer yet.")
	}
	out.Summary = fmt.Sprintf("%d of %s on this computer can run now: free, no key, and nothing leaves it.", ready, count(len(out.Models), "model", "models"))
	return out, nil
}

// localSetup downloads what a model needs, one pinned file at a time.
func localSetup(ctx context.Context, a *app.App, in *LocalSetupIn) (*LocalSetupOut, error) {
	want := strings.TrimSpace(in.Model)
	var model *aiservices.Model
	for _, m := range localModels("") {
		m := m
		if m.Ref() == want || m.ID == want || strings.EqualFold(m.Title, want) {
			model = &m
			break
		}
	}
	e := localEnv(a)
	s := local.Stores(e)
	var ids []string
	switch {
	case model != nil:
		ids = local.Downloads(model.Kind, model.ID)
	default:
		if _, ok := s.Find(want); !ok {
			return nil, apperr.New(apperr.NotFound, "No model called “%s” runs on this computer.", want).WithCommand("metarush ai local")
		}
		ids = []string{want}
	}
	out := &LocalSetupOut{}
	for _, id := range ids {
		p, ok := s.Find(id)
		if !ok {
			return nil, apperr.New(apperr.NotConfigured, "%s doesn't run on this kind of computer yet.", firstNonEmpty(titleOf(model), want)).
				WithDetails(map[string]any{"platform": runtime.GOOS + "/" + runtime.GOARCH})
		}
		if s.Ready(p) == nil {
			continue
		}
		if err := s.Setup(ctx, p, func(string) {}); err != nil {
			return nil, err
		}
		out.Fetched = append(out.Fetched, p.ID)
		a.Audit(ctx, "ai.local.setup", "local-model", p.ID, "", map[string]any{"sha256": p.SHA256, "bytes": p.Bytes, "url": p.URL})
	}
	if model == nil {
		out.Summary = fmt.Sprintf("“%s” is set up on this computer.", want)
		return out, nil
	}
	out.Model = localView(a, *model)
	switch {
	case out.Model.Ready && len(out.Fetched) == 0:
		out.Summary = model.Title + " was already set up on this computer."
	case out.Model.Ready:
		out.Summary = model.Title + " is set up on this computer: free, no key, and nothing leaves it."
	default:
		out.Summary = model.Title + "'s files are here, but it can't run yet: " + out.Model.NotHere
	}
	return out, nil
}

func titleOf(m *aiservices.Model) string {
	if m == nil {
		return ""
	}
	return m.Title
}
