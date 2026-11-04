package abilities

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/tools"
)

// Making an index for a catalogue (D487): a maintainer points MetaRush at a
// folder of ability folders (or of repositories holding them); each folder's
// archive is built as `ability check` builds it, its manifest read and
// checked, its repository's commit pinned — only when that commit holds
// exactly the folder's files, read from git raw as an import will — and the
// listing written from the manifest itself, so the index can't say other than
// what it lists. Nothing is fetched or published: the maintainer pushes the
// commits and publishes the index.

// MakeIn says how to make an index.
type MakeIn struct {
	// Folder holds ability folders (each with an ability.yaml), or
	// repositories that do, at any depth up to six folders.
	Folder string `json:"folder"`
	// Out is the index to write (read first, for its name, title and version).
	Out string `json:"out,omitempty"`
	// Name, Title and About are the catalogue's (default: Out's).
	Name  string `json:"name,omitempty"`
	Title string `json:"title,omitempty"`
	About string `json:"about,omitempty"`
	// Publisher is who publishes the abilities: a GitHub account or an https
	// address (default: each repository's owner on GitHub). Maintainer is who
	// keeps the catalogue, in the same form.
	Publisher  string `json:"publisher,omitempty"`
	Maintainer string `json:"maintainer,omitempty"`
	// Repository is the repositories' address on GitHub (default: each one's
	// remote called origin).
	Repository string `json:"repository,omitempty"`
	// Commit pins every ability to this commit (default: each repository's
	// current commit); the folder must be exactly the same there.
	Commit string `json:"commit,omitempty"`
	// Version and Date are the index's (default: one more than Out's when
	// anything changed, and today).
	Version int    `json:"version,omitempty"`
	Date    string `json:"date,omitempty"`
	// Force writes over an Out that isn't an index; DryRun writes nothing.
	Force  bool `json:"force,omitempty"`
	DryRun bool `json:"dryRun,omitempty"`
}

// MadeListing is one ability an index lists, and where it was pinned.
type MadeListing struct {
	Name    string `json:"name"`
	Folder  string `json:"folder"`
	Source  string `json:"source"`
	Archive string `json:"archive"`
}

// MakeOut says what was made.
type MakeOut struct {
	File     string                    `json:"file,omitempty"`
	Written  bool                      `json:"written"`
	Changed  bool                      `json:"changed" jsonschema:"description=It differs from the index that was there."`
	Version  int                       `json:"version"`
	Listed   []MadeListing             `json:"listed"`
	LeftOut  []models.CatalogueLeftOut `json:"leftOut"`
	Warnings []string                  `json:"warnings"`
	Index    CatalogueIndex            `json:"index"`
	Message  string                    `json:"message"`
}

// MakeIndex makes a catalogue's index from a folder (D487).
func MakeIndex(ctx context.Context, ts *tools.Set, in MakeIn) (*MakeOut, error) {
	root, err := filepath.Abs(strings.TrimSpace(in.Folder))
	if err != nil || strings.TrimSpace(in.Folder) == "" {
		return nil, apperr.New(apperr.InvalidInput, "Say which folder holds the abilities.")
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return nil, apperr.New(apperr.InvalidInput, "There's no folder called %s.", filepath.Base(root))
	}
	if !ts.Has(tools.Git) {
		return nil, apperr.New(apperr.ToolMissing, "Making a catalogue needs git, which isn't on this computer.")
	}
	out := &MakeOut{Listed: []MadeListing{}, LeftOut: []models.CatalogueLeftOut{}, Warnings: []string{}}
	var before *CatalogueIndex
	if p := strings.TrimSpace(in.Out); p != "" {
		out.File, _ = filepath.Abs(p)
		if b, err := os.ReadFile(out.File); err == nil {
			var prev CatalogueIndex
			if _, perr := ParseIndex(b); perr == nil && json.Unmarshal(b, &prev) == nil {
				before = &prev
			} else if !in.Force {
				return nil, apperr.New(apperr.Conflict, "%s is already there and isn't an ability catalogue: nothing was written.", filepath.Base(out.File)).
					WithHint("Give another file, or write over it with force.")
			}
		}
	}
	ix := CatalogueIndex{Format: IndexFormat, Name: in.Name, Title: in.Title, About: in.About, Abilities: []IndexEntry{}}
	if before != nil {
		ix.Name, ix.Title, ix.About, ix.Maintainer, ix.Revoked = firstNonEmpty(in.Name, before.Name), firstNonEmpty(in.Title, before.Title), firstNonEmpty(in.About, before.About), before.Maintainer, before.Revoked
	}
	if !ValidName(ix.Name) || strings.TrimSpace(ix.Title) == "" {
		return nil, apperr.New(apperr.InvalidInput, "Give the catalogue a short name (lowercase letters, digits and dashes) and a title.")
	}
	if in.Maintainer != "" {
		p, err := publisherFrom(in.Maintainer)
		if err != nil {
			return nil, err
		}
		ix.Maintainer = &p
	}
	if in.Commit != "" && !sha40Re.MatchString(in.Commit) {
		return nil, apperr.New(apperr.InvalidInput, "Name the commit in full (40 lowercase hex characters).")
	}
	folders, err := abilityFolders(root)
	if err != nil {
		return nil, err
	}
	if len(folders) == 0 {
		return nil, apperr.New(apperr.NotFound, "There's no ability folder (one with an %s) in %s.", ManifestFile, filepath.Base(root))
	}
	seen := map[string]string{}
	for _, dir := range folders {
		rel, _ := filepath.Rel(root, dir)
		e, listing, why, warn := makeEntry(ctx, ts, dir, in)
		out.Warnings = append(out.Warnings, warn...)
		switch {
		case why != "":
			out.LeftOut = append(out.LeftOut, models.CatalogueLeftOut{Name: filepath.ToSlash(rel), Why: why})
			continue
		case seen[e.Name] != "":
			out.LeftOut = append(out.LeftOut, models.CatalogueLeftOut{Name: filepath.ToSlash(rel), Why: fmt.Sprintf("another folder (%s) has an ability called %s", seen[e.Name], e.Name)})
			continue
		}
		seen[e.Name] = filepath.ToSlash(rel)
		listing.Folder = filepath.ToSlash(rel)
		ix.Abilities = append(ix.Abilities, IndexEntry{CatalogueEntry: e})
		out.Listed = append(out.Listed, listing)
	}
	sort.Slice(ix.Abilities, func(i, j int) bool { return ix.Abilities[i].Name < ix.Abilities[j].Name })
	sort.Slice(out.Listed, func(i, j int) bool { return out.Listed[i].Name < out.Listed[j].Name })
	// The version: one more than the index there when anything changed.
	out.Changed = before == nil || !sameIndex(*before, ix)
	switch {
	case in.Version > 0:
		ix.Version = in.Version
	case before != nil && !out.Changed:
		ix.Version = before.Version
	case before != nil:
		ix.Version = before.Version + 1
	default:
		ix.Version = 1
	}
	switch {
	case in.Date != "":
		if !validDate(in.Date) {
			return nil, apperr.New(apperr.InvalidInput, "Give the date as a day, like 2026-11-03.")
		}
		ix.Date = in.Date
	case before != nil && !out.Changed:
		ix.Date = before.Date
	default:
		ix.Date = time.Now().UTC().Format("2006-01-02")
	}
	out.Version = ix.Version
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // people read the index: <clip> stays <clip>
	enc.SetIndent("", "  ")
	if err := enc.Encode(ix); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	// What MetaRush writes, MetaRush reads whole.
	cp, err := ParseIndex(b)
	if err != nil {
		return nil, err
	}
	if len(cp.LeftOut) > 0 {
		return nil, fmt.Errorf("the index made would leave %s out: %s", cp.LeftOut[0].Name, cp.LeftOut[0].Why)
	}
	out.Index = ix
	if out.File != "" && !in.DryRun {
		if err := writeAtomically(out.File, b); err != nil {
			return nil, apperr.Wrap(apperr.InvalidInput, err, "MetaRush couldn't write the index there.")
		}
		out.Written = true
	}
	out.Message = fmt.Sprintf("“%s” version %d lists %s.", ix.Title, ix.Version, plural(len(ix.Abilities), "ability", "abilities"))
	if n := len(out.LeftOut); n > 0 {
		out.Message += fmt.Sprintf(" %s left out (each says why).", upperFirst(plural(n, "folder was", "folders were")))
	}
	if before != nil && !out.Changed {
		out.Message += " Nothing changed since the index that was there."
	}
	return out, nil
}

// abilityFolders finds the folders with an ability.yaml under root (not
// inside another ability's folder; hidden folders and installed packages
// left out), up to six folders deep.
func abilityFolders(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if p != root && (strings.HasPrefix(d.Name(), ".") || installedDirs[d.Name()]) {
			return filepath.SkipDir
		}
		if rel != "." && strings.Count(filepath.ToSlash(rel), "/") >= 6 {
			return filepath.SkipDir
		}
		if _, err := os.Lstat(filepath.Join(p, ManifestFile)); err == nil {
			out = append(out, p)
			return filepath.SkipDir
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// makeEntry lists one ability folder: its archive, its manifest, its
// repository's commit (only when the commit holds exactly these files).
func makeEntry(ctx context.Context, ts *tools.Set, dir string, in MakeIn) (models.CatalogueEntry, MadeListing, string, []string) {
	var warn []string
	arc, err := Build(dir)
	if err != nil {
		return models.CatalogueEntry{}, MadeListing{}, lowerFirst(strings.TrimSuffix(apperr.From(err).Message, ".")), nil
	}
	m, err := arc.Manifest()
	if err != nil {
		return models.CatalogueEntry{}, MadeListing{}, "its manifest can't be read: " + err.Error(), nil
	}
	if probs := m.Validate(arc.Programs()); len(probs) > 0 {
		var words []string
		for _, p := range probs {
			words = append(words, p.String())
		}
		return models.CatalogueEntry{}, MadeListing{}, "its manifest has problems: " + strings.Join(words, "; "), nil
	}
	topDir, err := gitOut(ctx, ts, dir, "rev-parse", "--show-toplevel")
	if err != nil || topDir == "" {
		return models.CatalogueEntry{}, MadeListing{}, "it isn't in a git repository: commit it to one first", nil
	}
	prefix, _ := gitOut(ctx, ts, dir, "rev-parse", "--show-prefix")
	prefix = strings.TrimSuffix(prefix, "/")
	commit := in.Commit
	if commit == "" {
		if commit, err = gitOut(ctx, ts, topDir, "rev-parse", "HEAD"); err != nil || !sha40Re.MatchString(commit) {
			return models.CatalogueEntry{}, MadeListing{}, "its repository has no commit yet", nil
		}
	}
	files, err := resources.FolderAtHere(ctx, ts, topDir, commit, prefix, folderLimits())
	if err != nil {
		return models.CatalogueEntry{}, MadeListing{}, fmt.Sprintf("it isn't in commit %s: commit it first", commit[:12]), nil
	}
	if at, err := archiveOfFolder(files); err != nil || at.Hash != arc.Hash {
		return models.CatalogueEntry{}, MadeListing{}, fmt.Sprintf("its files differ from commit %s: commit them first", commit[:12]), nil
	}
	repo := strings.TrimSpace(in.Repository)
	if repo == "" {
		repo, _ = gitOut(ctx, ts, topDir, "config", "--get", "remote.origin.url")
	}
	r, ok := resources.ParseRepoURL(githubAddress(repo))
	if !ok || r.Ref != "" || r.Path != "" {
		return models.CatalogueEntry{}, MadeListing{}, "its repository isn't one on GitHub that MetaRush can fetch from: give the repository's address", nil
	}
	if on, _ := gitOut(ctx, ts, topDir, "branch", "-r", "--contains", commit); strings.TrimSpace(on) == "" {
		warn = append(warn, fmt.Sprintf("%s: commit %s isn't on any branch of %s known here yet; push it before publishing the index", m.Name, commit[:12], r.Owner+"/"+r.Name))
	}
	pub := models.CataloguePublisher{GitHub: r.Owner}
	if in.Publisher != "" {
		if pub, err = publisherFrom(in.Publisher); err != nil {
			return models.CatalogueEntry{}, MadeListing{}, apperr.From(err).Message, nil
		}
	}
	f := factsOf(m)
	e := models.CatalogueEntry{Name: m.Name, Title: m.Title, Does: m.Does, Publisher: pub, Archive: arc.Hash, Licence: f.Licence, Provides: f.Provides, Outputs: f.Outputs,
		Network: f.Network, Hosts: f.Hosts, Sends: withoutNothing(f.Sends), Cost: costClass(f), ChangesOutside: f.Outside, Programs: f.Programs, OS: f.OS, Tags: m.Search,
		Source: models.CatalogueSource{Git: &models.CatalogueGitSource{Repository: r.URL(), Commit: commit, Path: prefix}}}
	e.Title, _ = indexLine(e.Title, 80)
	e.Does, _ = indexLine(e.Does, 300)
	if n, ok := arc.File(NotesFile); ok {
		e.Notes = notesExcerpt(string(n))
	}
	for _, k := range f.Keys {
		e.Keys = append(e.Keys, models.CatalogueKey{Name: k.Name, Hosts: k.Hosts})
	}
	if ai := declaredAI(mustManifest(arc)); len(ai) > 0 {
		e.AI = ai
	}
	if l := f.Local; l != nil {
		e.Local = &models.CatalogueLocal{Port: l.Port, Service: l.Service}
	}
	if len(e.Sends) == 0 {
		e.Sends = nil
	}
	if len(e.Hosts) == 0 {
		e.Hosts = nil
	}
	if len(e.Outputs) == 0 {
		e.Outputs = nil
	}
	if len(e.Programs) == 0 {
		e.Programs = nil
	}
	e, why := cleanEntry(e)
	if why != "" {
		return models.CatalogueEntry{}, MadeListing{}, why, warn
	}
	return e, MadeListing{Name: e.Name, Source: r.Owner + "/" + r.Name + "@" + commit[:12] + ":" + firstNonEmpty(prefix, "."), Archive: arc.Hash}, "", warn
}

func mustManifest(arc *Archive) []byte {
	b, _ := arc.File(ManifestFile)
	return b
}

// gitOut runs git (the fetching rules) and returns what it printed.
func gitOut(ctx context.Context, ts *tools.Set, dir string, args ...string) (string, error) {
	var buf bytes.Buffer
	if _, err := resources.Git(ctx, ts, dir, nil, &buf, args...); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

// githubAddress turns a remote's address (git@github.com:owner/name.git,
// ssh://git@github.com/owner/name) into its https form.
func githubAddress(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "git@github.com:"):
		s = "https://github.com/" + strings.TrimPrefix(s, "git@github.com:")
	case strings.HasPrefix(s, "ssh://git@github.com/"):
		s = "https://github.com/" + strings.TrimPrefix(s, "ssh://git@github.com/")
	}
	return strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")
}

// publisherFrom reads a publisher given on the command line: a GitHub
// account (or github.com/<account>), or an https address.
func publisherFrom(s string) (models.CataloguePublisher, error) {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "https://github.com/") && ghAccountRe.MatchString(strings.Trim(strings.TrimPrefix(s, "https://github.com/"), "/")):
		return models.CataloguePublisher{GitHub: strings.Trim(strings.TrimPrefix(s, "https://github.com/"), "/")}, nil
	case strings.HasPrefix(s, "github.com/") && ghAccountRe.MatchString(strings.TrimPrefix(s, "github.com/")):
		return models.CataloguePublisher{GitHub: strings.TrimPrefix(s, "github.com/")}, nil
	case ghAccountRe.MatchString(s):
		return models.CataloguePublisher{GitHub: s}, nil
	case webAddressProblem(s, false) == "":
		return models.CataloguePublisher{URL: s}, nil
	}
	return models.CataloguePublisher{}, apperr.New(apperr.InvalidInput, "A publisher is a GitHub account's name or an https address.")
}

// notesExcerpt is notes.md for a listing: whole paragraphs, up to the
// listing's limit.
func notesExcerpt(s string) string {
	s, ok := indexText(s, EntryNotesMax)
	if ok {
		return s
	}
	var b strings.Builder
	for _, p := range strings.Split(s, "\n\n") {
		if b.Len()+len(p)+2 > EntryNotesMax-1 {
			break
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(p)
	}
	if b.Len() == 0 {
		r := []rune(s)
		return string(r[:EntryNotesMax-1]) + "…"
	}
	return b.String() + "\n\n…"
}

// sameIndex: nothing a reader sees changed (the version and date aside).
func sameIndex(a, b CatalogueIndex) bool {
	a.Version, b.Version, a.Date, b.Date = 0, 0, "", ""
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	var x, y any
	_ = json.Unmarshal(ab, &x)
	_ = json.Unmarshal(bb, &y)
	return reflect.DeepEqual(x, y)
}

// writeAtomically writes a file through a temporary one beside it.
func writeAtomically(p string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), ".catalogue-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return errors.Join(err, os.Remove(tmp.Name()))
	}
	return nil
}
