package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const m = Module + "internal/"

// Two test binaries' profiles of the same blocks: each block once, covered
// when either covered it.
func TestMergeKeepsEachBlockOnceCoveredWhenAnyCovers(t *testing.T) {
	a := "mode: set\n" + m + "api/a.go:1.1,2.2 3 1\n" + m + "api/a.go:3.1,4.2 2 0\n" + m + "ops/o.go:1.1,9.2 5 0\n"
	b := "mode: atomic\n" + m + "api/a.go:1.1,2.2 3 0\n" + m + "api/a.go:3.1,4.2 2 7\n" + m + "ops/o.go:1.1,9.2 5 0\n" + Module + "cmd/metarush/main.go:1.1,2.2 1 1\n"
	p := &Profile{}
	for _, s := range []string{a, b} {
		if err := p.Read(strings.NewReader(s)); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.Blocks) != 4 || !p.Blocks[m+"api/a.go:1.1,2.2"].Covered || !p.Blocks[m+"api/a.go:3.1,4.2"].Covered || p.Blocks[m+"ops/o.go:1.1,9.2"].Covered {
		t.Fatalf("merged: %+v", p.Blocks)
	}
	var out bytes.Buffer
	if err := p.Write(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "mode: set\n") || !strings.Contains(out.String(), m+"api/a.go:3.1,4.2 2 1\n") {
		t.Fatalf("written:\n%s", out.String())
	}
	pk, total := p.Packages("internal/")
	if len(pk) != 2 || pk["internal/api"].Statements != 5 || pk["internal/api"].Percent != 100 || pk["internal/ops"].Percent != 0 {
		t.Fatalf("packages: %+v", pk)
	}
	if total.Statements != 10 || total.Covered != 5 || total.Percent != 50 {
		t.Fatalf("total: %+v (cmd/ is not under internal/)", total)
	}
	var table bytes.Buffer
	Table(&table, pk, total)
	if !strings.Contains(table.String(), "internal/api") || !strings.Contains(table.String(), "1 at 80% or more") {
		t.Fatalf("table:\n%s", table.String())
	}
	if err := p.Read(strings.NewReader("not a profile line\n")); err == nil {
		t.Fatal("a broken line was read")
	}
}

// The ratchet: a drop of more than a point fails, a smaller one or a rise
// doesn't, and a package that is no longer measured is named.
func TestTheRatchetFailsOnlyOnDropsOfMoreThanAPoint(t *testing.T) {
	base := Baseline{Packages: map[string]Package{
		"internal/api":  {Statements: 1000, Covered: 600, Percent: 60},
		"internal/ops":  {Statements: 500, Covered: 400, Percent: 80},
		"internal/gone": {Statements: 200, Covered: 100, Percent: 50},
	}}
	now := map[string]Package{
		"internal/api": {Statements: 1000, Covered: 591, Percent: 59.1}, // −0.9: fine
		"internal/ops": {Statements: 500, Covered: 390, Percent: 78},    // −2: fails
		"internal/new": {Statements: 300, Covered: 0, Percent: 0},       // not in the baseline
	}
	d := Compare(base, now, 1)
	if len(d) != 2 || d[0].Package != "internal/gone" || !d[0].Gone || d[1].Package != "internal/ops" || d[1].Was != 80 || d[1].Now != 78 {
		t.Fatalf("drops: %+v", d)
	}
}

func TestBaselineCheckAndUpdateFromFiles(t *testing.T) {
	dir := t.TempDir()
	prof := filepath.Join(dir, "p.cov")
	var b strings.Builder
	b.WriteString("mode: set\n")
	for i := 0; i < 150; i++ { // internal/big: 150 statements, 100 covered
		c := 0
		if i < 100 {
			c = 1
		}
		b.WriteString(m + "big/b.go:" + itoa(i) + ".1," + itoa(i) + ".9 1 " + itoa(c) + "\n")
	}
	b.WriteString(m + "small/s.go:1.1,2.2 50 0\n") // under 100 statements: not in the baseline
	if err := os.WriteFile(prof, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(dir, "baseline.json")
	if err := cmdBaseline([]string{"-o", base, prof}); err != nil {
		t.Fatal(err)
	}
	var got Baseline
	data, _ := os.ReadFile(base)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Packages) != 1 || got.Packages["internal/big"].Percent != 66.7 || got.Total.Statements != 200 {
		t.Fatalf("baseline: %+v", got)
	}
	var out bytes.Buffer
	if err := cmdCheck([]string{"-baseline", base, prof}, &out); err != nil || !strings.Contains(out.String(), "no package dropped") {
		t.Fatalf("the same profile: %v\n%s", err, out.String())
	}
	worse := strings.Replace(b.String(), m+"big/b.go:0.1,0.9 1 1", m+"big/b.go:0.1,0.9 1 0", 1)
	worse = strings.Replace(worse, m+"big/b.go:1.1,1.9 1 1", m+"big/b.go:1.1,1.9 1 0", 1)
	if err := os.WriteFile(prof, []byte(worse), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := cmdCheck([]string{"-baseline", base, prof}, &out); err == nil || !strings.Contains(out.String(), "internal/big") {
		t.Fatalf("two statements fewer (−1.3 points) must fail: %v\n%s", err, out.String())
	}
}

// From two runs the baseline keeps each package at its lowest, and only
// packages both runs measured.
func TestBaselineFromSeveralRunsKeepsTheLowest(t *testing.T) {
	dir := t.TempDir()
	run := func(name string, coveredA, coveredB int, extra bool) string {
		var b strings.Builder
		b.WriteString("mode: set\n")
		for i := 0; i < 200; i++ {
			b.WriteString(m + "a/a.go:" + itoa(i) + ".1," + itoa(i) + ".9 1 " + itoa(boolInt(i < coveredA)) + "\n")
			b.WriteString(m + "b/b.go:" + itoa(i) + ".1," + itoa(i) + ".9 1 " + itoa(boolInt(i < coveredB)) + "\n")
			if extra {
				b.WriteString(m + "c/c.go:" + itoa(i) + ".1," + itoa(i) + ".9 1 1\n")
			}
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	one, two := run("one.cov", 150, 100, true), run("two.cov", 140, 120, false)
	out := filepath.Join(dir, "baseline.json")
	if err := cmdBaseline([]string{"-o", out, one, two}); err != nil {
		t.Fatal(err)
	}
	var got Baseline
	data, _ := os.ReadFile(out)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Packages) != 2 || got.Packages["internal/a"].Percent != 70 || got.Packages["internal/b"].Percent != 50 {
		t.Fatalf("each at its lowest, internal/c (one run only) left out: %+v", got.Packages)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func itoa(i int) string { return strconv.Itoa(i) }
