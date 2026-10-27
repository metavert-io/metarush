// Command covreport merges Go coverage profiles and reads them per package
// (D195): the work behind scripts/coverage.sh. It is a development helper (go
// run ./scripts/covreport), not part of the metarush binary.
//
//	covreport merge -o out.cov a.cov [b.cov …]   one profile, each block once (covered when any input covers it)
//	covreport table [-prefix internal/] p.cov    statements, covered and percent per package, and the total
//	covreport check -baseline coverage-baseline.json [-drop 1] p.cov
//	                                             fails when a package of the baseline drops more than -drop points
//	covreport baseline -o coverage-baseline.json [-min 100] p.cov [p2.cov …]
//	                                             writes the baseline: every package with at least -min statements,
//	                                             at its lowest across the runs given
//
// A profile from `go test -coverpkg=./internal/... ./...` holds every
// instrumented block once per test binary; merging keeps each block once,
// covered when any test covered it — how the coverage numbers in
// docs/testing.md are measured.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Module is the import path prefix package names are shown without.
const Module = "github.com/metavert-io/metarush/"

// Block is one block of a profile: its statements and whether it ran.
type Block struct {
	Statements int
	Covered    bool
}

// Profile is a merged profile: blocks by "file:start,end".
type Profile struct {
	Blocks map[string]Block
}

// Read adds a profile's blocks (a block once, covered when any input covers it).
func (p *Profile) Read(r io.Reader) error {
	if p.Blocks == nil {
		p.Blocks = map[string]Block{}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for sc.Scan() {
		line := sc.Text()
		n++
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		// file:start.col,end.col statements count
		sp := strings.LastIndexByte(line, ' ')
		if sp < 0 {
			return fmt.Errorf("line %d: %q is not a profile line", n, line)
		}
		count, err := strconv.ParseInt(line[sp+1:], 10, 64)
		if err != nil {
			return fmt.Errorf("line %d: %w", n, err)
		}
		rest := line[:sp]
		sp = strings.LastIndexByte(rest, ' ')
		if sp < 0 {
			return fmt.Errorf("line %d: %q is not a profile line", n, line)
		}
		stmts, err := strconv.Atoi(rest[sp+1:])
		if err != nil {
			return fmt.Errorf("line %d: %w", n, err)
		}
		key := rest[:sp]
		b := p.Blocks[key]
		b.Statements = stmts
		b.Covered = b.Covered || count > 0
		p.Blocks[key] = b
	}
	return sc.Err()
}

// Write writes the merged profile (mode set: 1 covered, 0 not), blocks sorted.
func (p *Profile) Write(w io.Writer) error {
	keys := make([]string, 0, len(p.Blocks))
	for k := range p.Blocks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	bw := bufio.NewWriter(w)
	fmt.Fprintln(bw, "mode: set")
	for _, k := range keys {
		b := p.Blocks[k]
		c := 0
		if b.Covered {
			c = 1
		}
		fmt.Fprintf(bw, "%s %d %d\n", k, b.Statements, c)
	}
	return bw.Flush()
}

// Package is one package's numbers.
type Package struct {
	Statements int     `json:"statements"`
	Covered    int     `json:"covered"`
	Percent    float64 `json:"percent"`
}

// Packages adds the blocks up per package (the file's folder, shown without
// the module path), keeping only packages under prefix ("" keeps all).
func (p *Profile) Packages(prefix string) (map[string]Package, Package) {
	out := map[string]Package{}
	var total Package
	for k, b := range p.Blocks {
		file := k
		if i := strings.LastIndexByte(k, ':'); i >= 0 {
			file = k[:i]
		}
		pkg := strings.TrimPrefix(path.Dir(file), Module)
		if prefix != "" && !strings.HasPrefix(pkg+"/", prefix) && pkg != strings.TrimSuffix(prefix, "/") {
			continue
		}
		s := out[pkg]
		s.Statements += b.Statements
		total.Statements += b.Statements
		if b.Covered {
			s.Covered += b.Statements
			total.Covered += b.Statements
		}
		out[pkg] = s
	}
	for k, s := range out {
		s.Percent = percent(s.Covered, s.Statements)
		out[k] = s
	}
	total.Percent = percent(total.Covered, total.Statements)
	return out, total
}

func percent(covered, statements int) float64 {
	if statements == 0 {
		return 0
	}
	return math.Round(1000*float64(covered)/float64(statements)) / 10
}

// Baseline is coverage-baseline.json: the coverage the ratchet holds packages to.
type Baseline struct {
	Note     string             `json:"note"`
	Measured string             `json:"measured"`
	Total    Package            `json:"total"`
	Packages map[string]Package `json:"packages"`
}

// Drop is a package whose coverage fell.
type Drop struct {
	Package       string
	Was, Now      float64
	WasStatements int
	Gone          bool // the package isn't measured any more
}

// Compare lists the baseline's packages that dropped more than maxDrop points.
func Compare(base Baseline, now map[string]Package, maxDrop float64) []Drop {
	var out []Drop
	for name, was := range base.Packages {
		cur, ok := now[name]
		switch {
		case !ok:
			out = append(out, Drop{Package: name, Was: was.Percent, WasStatements: was.Statements, Gone: true})
		case was.Percent-cur.Percent > maxDrop+1e-9:
			out = append(out, Drop{Package: name, Was: was.Percent, Now: cur.Percent, WasStatements: was.Statements})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Package < out[j].Package })
	return out
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "merge":
		err = cmdMerge(os.Args[2:])
	case "table":
		err = cmdTable(os.Args[2:], os.Stdout)
	case "check":
		err = cmdCheck(os.Args[2:], os.Stdout)
	case "baseline":
		err = cmdBaseline(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "covreport:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: covreport merge -o out.cov in.cov… | table [-prefix internal/] p.cov | check -baseline f.json [-drop 1] p.cov | baseline -o f.json [-min 100] p.cov")
	os.Exit(2)
}

func readProfiles(files []string) (*Profile, error) {
	if len(files) == 0 {
		return nil, errors.New("no profile given")
	}
	p := &Profile{}
	for _, f := range files {
		r, err := os.Open(f)
		if err != nil {
			return nil, err
		}
		err = p.Read(r)
		r.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
	}
	return p, nil
}

func cmdMerge(args []string) error {
	fs := flag.NewFlagSet("merge", flag.ExitOnError)
	out := fs.String("o", "", "the merged profile")
	_ = fs.Parse(args)
	if *out == "" {
		return errors.New("-o is required")
	}
	p, err := readProfiles(fs.Args())
	if err != nil {
		return err
	}
	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	if err := p.Write(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Table prints the per-package table, largest packages first.
func Table(w io.Writer, pk map[string]Package, total Package) {
	names := make([]string, 0, len(pk))
	for n := range pk {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if pk[names[i]].Statements != pk[names[j]].Statements {
			return pk[names[i]].Statements > pk[names[j]].Statements
		}
		return names[i] < names[j]
	})
	fmt.Fprintf(w, "%-48s %10s %10s %7s\n", "package", "statements", "covered", "%")
	for _, n := range names {
		s := pk[n]
		fmt.Fprintf(w, "%-48s %10d %10d %6.1f%%\n", n, s.Statements, s.Covered, s.Percent)
	}
	over := 0
	for _, s := range pk {
		if s.Percent >= 80 {
			over++
		}
	}
	fmt.Fprintf(w, "%-48s %10d %10d %6.1f%%   (%d packages, %d at 80%% or more)\n", "total", total.Statements, total.Covered, total.Percent, len(pk), over)
}

func cmdTable(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("table", flag.ExitOnError)
	prefix := fs.String("prefix", "internal/", "only packages under this path (\"\" for all)")
	_ = fs.Parse(args)
	p, err := readProfiles(fs.Args())
	if err != nil {
		return err
	}
	pk, total := p.Packages(*prefix)
	Table(w, pk, total)
	return nil
}

func readBaseline(file string) (Baseline, error) {
	var b Baseline
	data, err := os.ReadFile(file)
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return b, fmt.Errorf("%s: %w", file, err)
	}
	return b, nil
}

func cmdCheck(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	baseFile := fs.String("baseline", "coverage-baseline.json", "the baseline")
	maxDrop := fs.Float64("drop", 1, "the most a package may drop, in points")
	prefix := fs.String("prefix", "internal/", "only packages under this path")
	_ = fs.Parse(args)
	base, err := readBaseline(*baseFile)
	if err != nil {
		return err
	}
	p, err := readProfiles(fs.Args())
	if err != nil {
		return err
	}
	now, total := p.Packages(*prefix)
	drops := Compare(base, now, *maxDrop)
	rises := 0
	for name, was := range base.Packages {
		if cur, ok := now[name]; ok && cur.Percent > was.Percent+*maxDrop {
			rises++
		}
	}
	fmt.Fprintf(w, "coverage %.1f%% (baseline %.1f%%, measured %s); %d package(s) of the baseline checked\n", total.Percent, base.Total.Percent, base.Measured, len(base.Packages))
	if rises > 0 {
		fmt.Fprintf(w, "%d package(s) rose more than %.0f point(s): scripts/coverage.sh --update-baseline records them\n", rises, *maxDrop)
	}
	if len(drops) == 0 {
		fmt.Fprintln(w, "no package dropped more than", *maxDrop, "point(s)")
		return nil
	}
	for _, d := range drops {
		if d.Gone {
			fmt.Fprintf(w, "  %-44s was %5.1f%% (%d statements), not measured now\n", d.Package, d.Was, d.WasStatements)
			continue
		}
		fmt.Fprintf(w, "  %-44s %5.1f%% → %5.1f%% (−%.1f)\n", d.Package, d.Was, d.Now, d.Was-d.Now)
	}
	return fmt.Errorf("%d package(s) dropped more than %.0f point(s) below coverage-baseline.json (add tests, or if it is right, scripts/coverage.sh --update-baseline)", len(drops), *maxDrop)
}

// cmdBaseline writes the baseline from one run's profile, or from several
// runs' (each read on its own): each package at its lowest, so a path one run
// happened to reach and another didn't (timing, a Mac-only helper) never
// fails the next check.
func cmdBaseline(args []string) error {
	fs := flag.NewFlagSet("baseline", flag.ExitOnError)
	out := fs.String("o", "coverage-baseline.json", "the baseline to write")
	minStmts := fs.Int("min", 100, "only packages with at least this many statements")
	prefix := fs.String("prefix", "internal/", "only packages under this path")
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		return errors.New("no profile given")
	}
	b := Baseline{
		Note:     "Go test coverage per package (go test -coverpkg=./internal/... ./..., blocks merged; the lowest of the runs it was recorded from): scripts/coverage.sh fails when a package listed here drops more than 1 point; --update-baseline rewrites it.",
		Measured: time.Now().UTC().Format("2006-01-02"),
		Packages: map[string]Package{},
	}
	for i, f := range fs.Args() {
		p, err := readProfiles([]string{f})
		if err != nil {
			return err
		}
		pk, total := p.Packages(*prefix)
		if i == 0 || total.Percent < b.Total.Percent {
			b.Total = total
		}
		for n, s := range pk {
			if s.Statements < *minStmts {
				continue
			}
			if was, ok := b.Packages[n]; i == 0 || (ok && s.Percent < was.Percent) {
				b.Packages[n] = s
			}
		}
		if i > 0 { // a package every run measured
			for n := range b.Packages {
				if _, ok := pk[n]; !ok {
					delete(b.Packages, n)
				}
			}
		}
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(*out, append(data, '\n'), 0o644)
}
