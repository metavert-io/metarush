package mcp_test

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/catalog"
	_ "github.com/metavert-io/metarush/internal/cli" // the whole registry, as the binary has it
	"github.com/metavert-io/metarush/internal/mcp"
	"github.com/metavert-io/metarush/internal/ops"
)

// The guards (D222): a client's model chooses well only from a few tools
// whose descriptions differ, and every conversation carries the list.

// MaxCoreTokens is the most the core may load before a client's first call
// (its tool list and instructions, about four characters a token).
const MaxCoreTokens = 2000

// NearDuplicate is the word-set Jaccard index at which two tools' descriptions
// are too alike to choose between.
const NearDuplicate = 0.6

// alikeInAll are pairs of the one-tool-per-operation list whose descriptions
// are alike; none is offered together outside --toolset all. Don't add to it:
// say what sets a new operation apart in its summary instead.
var alikeInAll = map[string]string{
	"content_rules_remove~content_signoffs_remove": "two parts of Content Settings, removed the same way",
	"content_signoffs_list~cut_signoffs_list":      "house sign-offs and a cut's own",
	"content_style_set~content_style_show":         "the house style read and changed",
	"cut_add~cut_move":                             "cut verbs share how they place a clip and check first",
	"cut_add~cut_split":                            "cut verbs share how they name a time and check first",
	"cut_delete~cut_deleteRange":                   "clips taken out, or a stretch of time",
	"mix_preview~sound_preview":                    "the Voiceover tab's and the Sound tab's previews",
	"remotion_pictures_setup~remotion_words_setup": "the same one-time change, for pictures and for words",
}

var toolNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func TestToolListsStaySmallAndDistinct(t *testing.T) {
	for _, set := range mcp.Toolsets() {
		for _, acc := range mcp.Accesses() {
			name := fmt.Sprintf("%s, %s", set, acc)
			list, err := mcp.ToolListFor(set, acc)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			switch set {
			case mcp.ToolsetAll:
			case mcp.ToolsetCore:
				if len(list) > mcp.MaxCoreTools {
					t.Errorf("%s: %d tools, more than %d: reach the rest through find and run", name, len(list), mcp.MaxCoreTools)
				}
				if sz := mcp.ListSizeFor(list, set, acc); sz.Tokens > MaxCoreTokens {
					t.Errorf("%s: a client loads about %d tokens before its first call, more than %d", name, sz.Tokens, MaxCoreTokens)
				}
			default:
				if len(list) > mcp.MaxToolsetTools {
					t.Errorf("%s: %d tools, more than %d: merge related operations into a tool with modes", name, len(list), mcp.MaxToolsetTools)
				}
			}
			if n := len(strings.Fields(mcp.InstructionsFor(set, acc))); n > mcp.MaxInstructionWords {
				t.Errorf("%s: the instructions take %d words, more than %d", name, n, mcp.MaxInstructionWords)
			}
			for _, tl := range list {
				if !toolNameRE.MatchString(tl.Name) {
					t.Errorf("%s: tool name %q is not one clients accept", name, tl.Name)
				}
				if tl.InputSchema["type"] != "object" || strings.TrimSpace(tl.Description) == "" {
					t.Errorf("%s: %s has no description or no object input", name, tl.Name)
				}
				if _, err := json.Marshal(tl); err != nil {
					t.Errorf("%s: %s: %v", name, tl.Name, err)
				}
				for _, op := range tl.Ops() {
					if o, ok := ops.Get(op); ok && (o.HumanOnly || (o.Mutating && acc == mcp.ReadOnly) || (o.Mutating && acc == mcp.Propose && !ops.Draftable(o))) {
						t.Errorf("%s: %s offers %s", name, tl.Name, op)
					}
				}
			}
			for i := range list {
				for j := i + 1; j < len(list); j++ {
					a, b := list[i], list[j]
					if jc := jaccard(a.About(), b.About()); jc >= NearDuplicate {
						if _, ok := alikeInAll[a.Name+"~"+b.Name]; ok && set == mcp.ToolsetAll {
							continue
						}
						t.Errorf("%s: %s and %s are described too alike (%.2f): say what sets each apart", name, a.Name, b.Name, jc)
					}
				}
			}
		}
	}
	for pair := range alikeInAll {
		a, b, _ := strings.Cut(pair, "~")
		_, okA := ops.Get(strings.ReplaceAll(a, "_", "."))
		_, okB := ops.Get(strings.ReplaceAll(b, "_", "."))
		if !okA || !okB {
			t.Errorf("alikeInAll names %s, which is no longer two operations", pair)
		}
	}
}

// jaccard is how alike two descriptions' words are: shared words over all
// words (stop words left out).
func jaccard(a, b string) float64 {
	x, y := map[string]bool{}, map[string]bool{}
	for _, w := range catalog.Terms(a, false) {
		x[w] = true
	}
	for _, w := range catalog.Terms(b, false) {
		y[w] = true
	}
	shared := 0
	for w := range x {
		if y[w] {
			shared++
		}
	}
	if all := len(x) + len(y) - shared; all > 0 {
		return float64(shared) / float64(all)
	}
	return 0
}

// The toolsets' operations are real, and none is offered twice in one (the
// rest are reached through find and run).
func TestToolsetsNameRealOperations(t *testing.T) {
	for _, set := range mcp.Toolsets() {
		list, err := mcp.ToolList(set, true)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]string{}
		for _, tl := range list {
			for _, op := range tl.Ops() {
				if op == "*" {
					continue
				}
				if _, ok := ops.Get(op); !ok {
					t.Errorf("%s: %s names %s, which is no operation", set, tl.Name, op)
				}
				if prev, ok := seen[op]; ok && set != mcp.ToolsetAll {
					t.Errorf("%s: %s is offered by %s and %s", set, op, prev, tl.Name)
				}
				seen[op] = tl.Name
			}
		}
	}
	var sets []string
	for _, s := range mcp.Toolsets() {
		sets = append(sets, s)
	}
	sort.Strings(sets)
	if strings.Join(sets, ",") != "admin,all,core,edit,library,review" {
		t.Fatalf("toolsets: %v", sets)
	}
	if _, err := mcp.ToolList("everything", false); err == nil {
		t.Fatal("an unknown toolset was taken")
	}
}

// docs/mcp.md's table of toolsets says how many tools each offers, read-only,
// proposing and with changes allowed: exactly for the core and the toolsets,
// within a tenth for all (which grows with every operation).
func TestDocsCountTheTools(t *testing.T) {
	b, err := os.ReadFile("../../docs/mcp.md")
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile(`(?m)^\s*\|\s*(\w+)[^|]*\|[^|]*\|\s*(\d+)\s*\|\s*(\d+)\s*\|\s*(\d+)\s*\|\s*$`)
	seen := map[string]bool{}
	for _, m := range row.FindAllStringSubmatch(string(b), -1) {
		set := m[1]
		for i, acc := range []mcp.Access{mcp.ReadOnly, mcp.Propose, mcp.Changes} {
			changes := acc
			said, _ := strconv.Atoi(m[2+i])
			list, err := mcp.ToolListFor(set, acc)
			if err != nil {
				t.Fatalf("docs/mcp.md names toolset %s: %v", set, err)
			}
			n := len(list)
			ok := said == n
			if set == mcp.ToolsetAll {
				ok = math.Abs(float64(said-n)) <= float64(n)/10
			}
			if !ok {
				t.Errorf("docs/mcp.md says toolset %s offers %d tools (%s); it offers %d: update the table", set, said, changes, n)
			}
		}
		seen[set] = true
	}
	for _, set := range mcp.Toolsets() {
		if !seen[set] {
			t.Errorf("docs/mcp.md's table has no row for toolset %s", set)
		}
	}
}

// No tool a client sees both reads and changes (D222): clients ask a person
// per tool, so a person can allow every reading tool and keep changes on ask.
// A tool's readOnlyHint says which it is, and every operation it runs agrees.
func TestNoToolMixesReadingAndChanging(t *testing.T) {
	for _, set := range mcp.Toolsets() {
		for _, changes := range mcp.Accesses() {
			list, err := mcp.ToolListFor(set, changes)
			if err != nil {
				t.Fatal(err)
			}
			for _, tl := range list {
				ro, ok := tl.Annotations["readOnlyHint"].(bool)
				if !ok {
					t.Errorf("%s: %s has no readOnlyHint", set, tl.Name)
					continue
				}
				if tl.Name == "run_change" && ro || tl.Name == "run" && !ro {
					t.Errorf("%s: %s readOnlyHint %v", set, tl.Name, ro)
				}
				for _, name := range tl.Ops() {
					op, ok := ops.Get(name)
					if !ok {
						continue // find, describe, run's "*"
					}
					if op.Mutating == ro {
						t.Errorf("%s (changes %v): %s says readOnlyHint %v but runs %s, which %s", set, changes, tl.Name, ro, name,
							map[bool]string{true: "changes something", false: "only reads"}[op.Mutating])
					}
				}
				if !ro && !strings.HasSuffix(tl.Name, "_change") && tl.Op() == "" {
					t.Errorf("%s: %s changes things but isn't named …_change", set, tl.Name)
				}
			}
		}
	}
}
