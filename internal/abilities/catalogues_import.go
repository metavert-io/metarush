package abilities

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// Bringing an ability in from a community catalogue (D485): MetaRush fetches
// exactly what the listing pins — a repository's commit and folder through
// the hardened git of D104 (only that commit, no hooks or prompts, blobs read
// raw, nothing checked out or run), or an ability file whose SHA-256 must be
// the one listed — builds the canonical archive, and refuses unless its
// SHA-256 is the listing's archive and its manifest says what the listing
// says (name, places, keys, network, what leaves, what it brings back and
// what AI does to it, cost, licence, programs). It lands as an ability file's
// import does: imported, never approved or set up; a person checks it on a
// computer here, then an owner approves it. A person brings it in (D486):
// HumanOnly, so the panel's Claude proposes it as a card and AI apps only
// browse.

// fetchTimeout bounds fetching an ability's files.
const fetchTimeout = 5 * time.Minute

// AbilityCatalogueImportIn brings an ability in from a trusted catalogue.
type AbilityCatalogueImportIn struct {
	// From is catalogue:name, or catalogue:name@version to bring in exactly
	// that version (8 or more characters of its archive's SHA-256).
	From string `json:"from" jsonschema:"minLength=3,maxLength=600,description=catalogue:name or catalogue:name@version."`
	// AsVersion brings it in as a new version of an ability of the same name
	// here that didn't come from this catalogue.
	AsVersion bool `json:"asVersion,omitempty"`
	// DryRun fetches and checks it and says what would come in; nothing is kept.
	DryRun bool `json:"dryRun,omitempty"`
}

func init() {
	ops.Def("ability.catalogues.import", "Bring an ability in from a trusted community catalogue (people only): MetaRush fetches exactly the version listed, refuses it unless its files are that version and its manifest says what the listing says, and keeps it as a draft — never approved or set up here: check it on this computer, then an owner approves it", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *AbilityCatalogueImportIn) (*AbilityImportOut, error) {
			return importListed(ctx, a, in)
		})
}

func importListed(ctx context.Context, a *app.App, in *AbilityCatalogueImportIn) (*AbilityImportOut, error) {
	key, name, version, err := ParseListedRef(in.From)
	if err != nil {
		return nil, err
	}
	if _, err := findCatalogue(ctx, a, key); err != nil {
		return nil, err
	}
	cs, err := refreshed(ctx, a, key, false)
	if err != nil {
		return nil, err
	}
	c := &cs[0]
	if c.Index == nil {
		return nil, apperr.New(apperr.Provider, "MetaRush hasn't read “%s” yet: %s", c.Key, firstNonEmpty(c.Problem, "try again in a moment.")).WithCommand("metarush ability browse --catalogue " + c.Key + " --refresh")
	}
	e, err := listingOf(c, name)
	if errors.Is(err, errNotListed) {
		return nil, apperr.New(apperr.NotFound, "“%s” doesn't list an ability called %s.", c.Index.Title, name).WithCommand("metarush ability browse --catalogue " + c.Key)
	}
	if err != nil {
		return nil, err
	}
	if version != "" && !strings.HasPrefix(e.Archive, version) {
		return nil, apperr.New(apperr.Conflict, "“%s” lists another version of %s now (%s, not %s): nothing came in.", c.Index.Title, name, Short(e.Archive), Short(version)).
			WithHint("Look at it again, then bring in the version it lists.").WithCommand("metarush ability browse --catalogue " + c.Key + " --name " + name)
	}
	for _, r := range c.Index.Revoked {
		if r.Archive == e.Archive {
			return nil, apperr.New(apperr.Validation, "“%s” took this version of %s back%s: nothing came in.", c.Index.Title, name, whyWords(r.Why))
		}
	}
	from := &models.AbilityImport{Made: dayOf(c.Index.Date), Decisions: []models.AbilityFileDecision{}, Lineage: []string{e.Archive}, At: now(), By: a.Actor,
		Catalogue: &models.AbilityFromCatalogue{Key: c.Key, URL: c.URL, Title: c.Index.Title, Version: c.Index.Version, Date: c.Index.Date, Publisher: publisherWords(&e.Publisher)}}
	if g := e.Source.Git; g != nil {
		from.Catalogue.Repository, from.Catalogue.Commit, from.Catalogue.Path = g.Repository, g.Commit, g.Path
	} else if f := e.Source.File; f != nil {
		from.Catalogue.FileURL, from.Catalogue.FileSHA256 = f.URL, f.SHA256
	}
	// This very version here already: nothing new (nothing is fetched).
	if v, err := getVersion(ctx, a, e.Archive); err == nil {
		out := &AbilityImportOut{Name: v.Name, Title: v.Title, Version: v.Hash, Number: v.Number, State: v.State, DryRun: in.DryRun, From: from, Facts: v.Facts, Link: links.Ability(v.Name, v.Hash),
			Hosts: v.Facts.Hosts, Leaves: sendsWords(v.Facts), Files: v.Files}
		if out.Hosts == nil {
			out.Hosts = []string{}
		}
		out.Message = fmt.Sprintf("This workspace already has that version: version %d of “%s” (%s). Nothing new came in.", v.Number, v.Title, StateWords(v.State))
		return out, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	// Another ability of its name here: the next version when it came from
	// this catalogue, else only when a person says it is the same one.
	here, err := versionsOf(ctx, a, name)
	if err != nil {
		return nil, err
	}
	of := false
	if len(here) > 0 {
		fromHere := slices.ContainsFunc(here, func(v models.AbilityVersion) bool {
			return v.Imported != nil && v.Imported.Catalogue != nil && v.Imported.Catalogue.URL == c.URL
		})
		if !fromHere && !in.AsVersion {
			return nil, apperr.New(apperr.Conflict, "This workspace already has an ability called “%s” (“%s”) that didn't come from “%s”.", name, here[0].Title, c.Index.Title).
				WithHint("Bring it in as a new version of that ability only if it is the same one; otherwise rename one of them.").
				WithCommand("metarush ability import --from-catalogue " + c.Key + ":" + name + "@" + Short(e.Archive) + " --as-version")
		}
		of = true
	}
	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	arc, err := fetchListed(fctx, a, e)
	if err != nil {
		return nil, err
	}
	if arc.Hash != e.Archive {
		return nil, apperr.New(apperr.Integrity, "The files MetaRush fetched for %s aren't the version “%s” names (it names %s; the files are %s): nothing came in.", name, c.Index.Title, Short(e.Archive), Short(arc.Hash)).
			WithDetails(map[string]any{"listed": e.Archive, "fetched": arc.Hash})
	}
	m, err := arc.Manifest()
	if err != nil {
		return nil, apperr.New(apperr.Validation, "The ability %s lists can't be read: %s", name, err.Error())
	}
	if m.Name != e.Name {
		return nil, apperr.New(apperr.Integrity, "“%s” lists %s, but the ability's own manifest names %s: nothing came in.", c.Index.Title, e.Name, clean(m.Name, 40))
	}
	if probs := m.Validate(arc.Programs()); len(probs) > 0 {
		var words []string
		for _, p := range probs {
			words = append(words, p.String())
		}
		return nil, apperr.New(apperr.Validation, "The ability “%s” lists has problems MetaRush won't bring in: %s", c.Index.Title, strings.Join(words, "; ")).WithDetails(map[string]any{"problems": words})
	}
	manifest, _ := arc.File(ManifestFile)
	if diffs := listingDiffers(e, m, manifest); len(diffs) > 0 {
		return nil, apperr.New(apperr.Integrity, "“%s” describes %s differently from what it is: %s. Nothing came in.", c.Index.Title, name, strings.Join(diffs, "; ")).
			WithHint("Tell whoever keeps the catalogue; what an ability may do is what its own manifest says.").WithDetails(map[string]any{"differences": diffs})
	}
	f := factsOf(m)
	out := &AbilityImportOut{Name: m.Name, Title: m.Title, Version: arc.Hash, Facts: f, DryRun: in.DryRun, Hosts: f.Hosts, Leaves: sendsWords(f), Files: []models.AbilityFile{}, From: from, New: true, Of: of}
	if out.Hosts == nil {
		out.Hosts = []string{}
	}
	for _, x := range arc.Files {
		b := x.Bytes()
		out.Files = append(out.Files, models.AbilityFile{Path: x.Path, Size: int64(len(b)), SHA256: sum(b), Program: x.Exec})
	}
	if m.Title != e.Title {
		out.Lineage = fmt.Sprintf("The catalogue calls it “%s”; it calls itself “%s”.", e.Title, m.Title)
	}
	if in.DryRun {
		out.State = models.AbilityImported
		out.Message = fmt.Sprintf("“%s” would come in from “%s” as %s, never approved here: check it on this computer, then an owner approves it.", m.Title, c.Index.Title,
			map[bool]string{true: "a new version of the ability here", false: "a new ability"}[of])
		return out, nil
	}
	v, err := recordImported(ctx, a, arc, m, from)
	if err != nil {
		return nil, err
	}
	out.Number, out.State, out.Link = v.Number, v.State, links.Ability(v.Name, v.Hash)
	a.Audit(context.WithoutCancel(ctx), "ability.catalogues.import", "ability", v.ID, "", map[string]any{"name": v.Name, "version": v.Hash, "number": v.Number,
		"catalogue": c.Key, "url": c.URL, "catalogueVersion": c.Index.Version, "source": sourceOf(from.Catalogue), "of": of})
	out.Message = fmt.Sprintf("Version %d of “%s” came in from “%s” as a draft: it isn't approved or set up here.", v.Number, v.Title, c.Index.Title)
	out.Next = "Check it on this computer (metarush ability check --version " + v.Name + "@" + Short(v.Hash) + "); then an owner reads what it may do under Library → Abilities and approves it."
	return out, nil
}

func whyWords(why string) string {
	if why == "" {
		return ""
	}
	return " (" + strings.TrimSuffix(why, ".") + ")"
}

// dayOf reads a catalogue's date (zero when it isn't one).
func dayOf(d string) time.Time {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return time.Time{}
	}
	return t
}

// sourceOf names what was fetched, for the audit.
func sourceOf(c *models.AbilityFromCatalogue) string {
	if c.Repository != "" {
		return c.Repository + "@" + c.Commit + ":" + c.Path
	}
	return c.FileURL + "#" + c.FileSHA256
}

// fetchListed fetches what a listing pins and builds its canonical archive.
func fetchListed(ctx context.Context, a *app.App, e models.CatalogueEntry) (*Archive, error) {
	switch g, f := e.Source.Git, e.Source.File; {
	case g != nil:
		if err := os.MkdirAll(a.Cfg.Scratch.Dir, 0o755); err != nil {
			return nil, err
		}
		work, err := os.MkdirTemp(a.Cfg.Scratch.Dir, "ability-catalogue-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(work)
		files, err := resources.FolderAt(ctx, a.Tools, work, g.Repository, g.Commit, g.Path, folderLimits())
		if err != nil {
			return nil, err
		}
		return archiveOfFolder(files)
	case f != nil:
		got, err := readHTTPS(ctx, "the ability file", f.URL, "", fileMaxBytes, fileTimeout)
		if err != nil {
			return nil, err
		}
		if sum(got.Body) != f.SHA256 {
			host := ""
			if u, err := url.Parse(f.URL); err == nil {
				host = u.Host
			}
			return nil, apperr.New(apperr.Integrity, "The ability file on %s isn't the one the catalogue names (its fingerprint differs): nothing came in.", host).
				WithDetails(map[string]any{"listed": f.SHA256, "fetched": sum(got.Body)})
		}
		_, arc, err := readFile(bytes.NewReader(got.Body), int64(len(got.Body)))
		return arc, err
	}
	return nil, apperr.New(apperr.Validation, "The catalogue doesn't say where the ability's files are.")
}

// folderLimits are an ability folder's limits for reading it from a
// repository, hidden files left out first as Build leaves them out.
func folderLimits() resources.FolderLimits {
	return resources.FolderLimits{Files: MaxFiles, FileBytes: MaxFileBytes, TotalBytes: MaxTotalBytes, Skip: hiddenInFolder}
}

// hiddenInFolder: Build leaves out names starting with a dot, and
// __pycache__, at every level.
func hiddenInFolder(rel string) bool {
	for _, s := range strings.Split(rel, "/") {
		if strings.HasPrefix(s, ".") || s == "__pycache__" {
			return true
		}
	}
	return false
}

// archiveOfFolder builds the canonical archive of a folder read from a
// repository exactly as Build builds it from a folder on this computer: the
// same files give the same version.
func archiveOfFolder(files []resources.FolderFile) (*Archive, error) {
	in := map[string]FileIn{}
	for _, f := range files {
		switch {
		case f.Link():
			return nil, problem("%s is a link to somewhere else; an ability's files are its own.", f.Path)
		case f.Submodule():
			return nil, problem("%s is another repository inside the folder; an ability's files are its own.", f.Path)
		}
		in[f.Path] = FileIn{Bytes: f.Data, Exec: f.Program()}
	}
	return FromFiles(in)
}

// declaredAI is what a manifest says AI does to each kind of output (its
// `ai:` part, read as written: D460's manifests have it), nil when it says
// nothing.
func declaredAI(manifest []byte) map[string]string {
	var raw struct {
		AI map[string]string `yaml:"ai"`
	}
	if err := yaml.Unmarshal(manifest, &raw); err != nil || len(raw.AI) == 0 {
		return nil
	}
	return raw.AI
}

// listingDiffers says how a listing differs from the manifest of what it
// lists, in what a person decides by: the places it reaches, its keys, its
// network, what leaves, what it brings back and what AI does to it, what it
// provides, its cost, licence, programs and systems, the service it reaches
// on the computer and whether it changes things outside.
func listingDiffers(e models.CatalogueEntry, m *Manifest, manifest []byte) []string {
	f := factsOf(m)
	var out []string
	differ := func(what string, listed, is []string) {
		if !sameSet(listed, is) {
			out = append(out, fmt.Sprintf("%s: the listing says %s, the ability %s", what, listWords(listed), listWords(is)))
		}
	}
	differ("the places it reaches", e.Hosts, f.Hosts)
	if e.Network != f.Network {
		out = append(out, fmt.Sprintf("its network: the listing says %s, the ability %s", e.Network, f.Network))
	}
	listedKeys := map[string][]string{}
	for _, k := range e.Keys {
		hosts := k.Hosts
		if len(hosts) == 0 {
			hosts = e.Hosts
		}
		listedKeys[k.Name] = hosts
	}
	isKeys := map[string][]string{}
	for _, k := range f.Keys {
		isKeys[k.Name] = k.Hosts
	}
	differ("its keys", keysOf(listedKeys), keysOf(isKeys))
	for name, hosts := range isKeys {
		if lh, ok := listedKeys[name]; ok && !sameSet(lh, hosts) {
			out = append(out, fmt.Sprintf("where its %s key goes: the listing says %s, the ability %s", name, listWords(lh), listWords(hosts)))
		}
	}
	differ("what leaves the computer", withoutNothing(e.Sends), withoutNothing(f.Sends))
	differ("what it brings back", e.Outputs, f.Outputs)
	differ("the AI work it provides", e.Provides, f.Provides)
	differ("the programs it needs", e.Programs, f.Programs)
	differ("the systems it runs on", e.OS, f.OS)
	if listed, is := aiPairs(e.AI), aiPairs(declaredAI(manifest)); !sameSet(listed, is) {
		out = append(out, fmt.Sprintf("what AI does to what it brings back: the listing says %s, the ability %s", listWords(listed), listWords(is)))
	}
	if e.Cost != costClass(f) {
		out = append(out, fmt.Sprintf("its cost: the listing says %s, the ability %s", e.Cost, costClass(f)))
	}
	if !strings.EqualFold(strings.TrimSpace(e.Licence), strings.TrimSpace(f.Licence)) {
		out = append(out, fmt.Sprintf("its licence: the listing says “%s”, the ability “%s”", e.Licence, f.Licence))
	}
	if e.ChangesOutside != f.Outside {
		out = append(out, "whether it changes things outside MetaRush")
	}
	switch l, is := e.Local, f.Local; {
	case l == nil && is != nil:
		out = append(out, fmt.Sprintf("the listing doesn't say it reaches a service on the computer; the ability reaches port %d", is.Port))
	case l != nil && is == nil:
		out = append(out, "the listing says it reaches a service on the computer; the ability doesn't")
	case l != nil && l.Port != is.Port:
		out = append(out, fmt.Sprintf("the service on the computer: the listing says port %d, the ability port %d", l.Port, is.Port))
	}
	return out
}

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	sort.Strings(x)
	sort.Strings(y)
	return slices.Equal(slices.Compact(x), slices.Compact(y))
}

func listWords(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	ys := slices.Clone(xs)
	sort.Strings(ys)
	return strings.Join(ys, ", ")
}

func keysOf(m map[string][]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func withoutNothing(xs []string) []string {
	return slices.DeleteFunc(slices.Clone(xs), func(s string) bool { return s == SendsNothing })
}

func aiPairs(m map[string]string) []string {
	var out []string
	for k, v := range m {
		out = append(out, k+" "+v)
	}
	return out
}
