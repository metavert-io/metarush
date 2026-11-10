package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// takesAPrice reports an input with a maxUsd field (embedded structs too).
func takesAPrice(t reflect.Type) bool {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous && takesAPrice(f.Type) {
			return true
		}
		if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name == "maxUsd" {
			return true
		}
	}
	return false
}

// Every change an app may call that can spend money — one MetaRush counts as
// paid, or one that takes a price — says when a call spends (spendRules,
// pricedBy) or why only a person's call does (byAPerson): a new one fails here
// until it does, so it can't reach a connection that wasn't allowed to spend.
func TestEveryPaidChangeSaysWhenItSpends(t *testing.T) {
	paid := ops.PaidOps()
	seen := map[string]bool{}
	for _, op := range ops.All() {
		if !op.Mutating || op.HumanOnly {
			continue
		}
		if !slices.Contains(paid, op.Name) && !takesAPrice(op.InType) {
			continue
		}
		seen[op.Name] = true
		_, rule := spendRules[op.Name]
		_, priced := pricedBy[op.Name]
		_, person := byAPerson[op.Name]
		switch n := btoi(rule) + btoi(priced) + btoi(person); {
		case n > 1:
			t.Errorf("%s is in more than one of spendRules, pricedBy and byAPerson", op.Name)
		case n == 0:
			t.Errorf("%s can spend money: give it a rule in spendRules or pricedBy (when a call spends), or a reason in byAPerson", op.Name)
		}
	}
	for _, names := range [][]string{slices.Collect(maps.Keys(spendRules)), slices.Collect(maps.Keys(pricedBy)), slices.Collect(maps.Keys(byAPerson))} {
		for _, name := range names {
			if !seen[name] {
				t.Errorf("%s isn't a change an app may call that can spend", name)
			}
		}
	}
	for name := range claudeRuns {
		if op, ok := ops.Get(name); !ok || !op.Mutating || op.HumanOnly {
			t.Errorf("claudeRuns names %s, which isn't a change an app may call", name)
		}
	}
	for name, ask := range pricedBy {
		if p, ok := ask(map[string]any{}); ok {
			if op, found := ops.Get(p.op); !found || (op.Mutating && p.op != name) {
				t.Errorf("%s is priced by %s, which isn't a read (or the call's own estimate)", name, p.op)
			}
		}
	}
	if len(seen) < 8 {
		t.Fatalf("only %d paid changes found: %v", len(seen), seen)
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// What spends is decided per call: pricing and work on this computer never
// do; for the changes priced first, the price of this very call decides.
func TestWhichCallsSpend(t *testing.T) {
	s, err := New(viaMCP(testapp.New(t)), Options{AllowChanges: true})
	if err != nil {
		t.Fatal(err)
	}
	var asked []string
	prices := map[string]any{}
	saved := priceCall
	defer func() { priceCall = saved }()
	priceCall = func(_ context.Context, _ *app.App, op string, in map[string]any) (any, error) {
		b, _ := json.Marshal(in)
		asked = append(asked, op+" "+string(b))
		if p, ok := prices[op]; ok {
			return p, nil
		}
		return nil, errors.New("no price")
	}
	for _, tc := range []struct {
		op, args string
		price    any // the estimate's answer, for those priced first
		spend    bool
		asks     string // the pricing asked for, if any
	}{
		{op: "generate.shot", args: `{"prompt":"a dog"}`, spend: true},
		{op: "generate.shot", args: `{"prompt":"a dog","dryRun":true}`},
		{op: "generate.again", args: `{}`, spend: true},
		{op: "cut.background", args: `{"clip":"1"}`},
		{op: "cut.background", args: `{"clip":"1","model":"local/modnet","maxUsd":0.5}`},
		{op: "cut.background", args: `{"clip":"1","model":"Local/modnet"}`},
		{op: "cut.background", args: `{"clip":"1","model":"elsewhere/x"}`, spend: true},
		{op: "cut.background", args: `{"clip":"1","maxUsd":0.5}`, spend: true},
		{op: "cut.background", args: `{"clip":"1","maxUsd":0.5,"dryRun":true}`},
		{op: "footage.cutout", args: `{"assetId":"x"}`},
		{op: "ai.make", args: `{"kind":"voice","dryRun":true}`},
		{op: "ai.make", args: `{"kind":"upscale"}`, price: map[string]any{"usd": 0}, asks: `ai.estimate {"kind":"upscale"}`},
		{op: "ai.make", args: `{"kind":"music","maxUsd":2,"confirmToken":"mcp1.x"}`, price: map[string]any{"usd": 0.4}, spend: true, asks: `ai.estimate {"kind":"music"}`},
		{op: "voiceover.tts", args: `{"revisionId":"r","apply":true}`, price: map[string]any{"credits": 0}, asks: `voiceover.estimate {"revisionId":"r"}`},
		{op: "voiceover.tts", args: `{"revisionId":"r"}`, price: map[string]any{"credits": 120}, spend: true, asks: `voiceover.estimate {"revisionId":"r"}`},
		{op: "sound.enhance", args: `{"target":"Maya","estimate":true}`},
		{op: "sound.enhance", args: `{"target":"Maya"}`, price: map[string]any{"estimate": map[string]any{"credits": 1000}}, spend: true, asks: `sound.enhance {"estimate":true,"target":"Maya"}`},
		{op: "sound.enhance", args: `{"target":"Maya","mode":"clean","apply":true}`, price: map[string]any{"estimate": map[string]any{}}, asks: `sound.enhance {"estimate":true,"mode":"clean","off":false,"target":"Maya"}`},
		{op: "sound.enhance", args: `{"target":"Maya","mode":"cloud","off":true}`, price: map[string]any{"estimate": map[string]any{"credits": 0}}, asks: `sound.enhance {"estimate":true,"mode":"cloud","off":true,"target":"Maya"}`},
		{op: "sound.enhance", args: `{"target":"Maya"}`, price: map[string]any{"estimate": "unreadable"}, spend: true, asks: `sound.enhance {"estimate":true,"target":"Maya"}`},
		{op: "assistant.ask", args: `{"text":"hi"}`, spend: true},
		{op: "project.create", args: `{"name":"x"}`},
	} {
		op, ok := ops.Get(tc.op)
		if !ok {
			t.Fatalf("no %s", tc.op)
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.args), &args); err != nil {
			t.Fatal(err)
		}
		asked, prices = nil, map[string]any{}
		if p, ok := pricedBy[tc.op]; ok && tc.price != nil {
			q, _ := p(args)
			prices[q.op] = tc.price
		}
		got, err := s.spends(context.Background(), op, args)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.op, tc.args, err)
		}
		if got != tc.spend {
			t.Errorf("%s %s: spends = %v, want %v", tc.op, tc.args, got, tc.spend)
		}
		if want := tc.asks; (want == "" && len(asked) > 0) || (want != "" && (len(asked) != 1 || asked[0] != want)) {
			t.Errorf("%s %s: priced with %v, want %q", tc.op, tc.args, asked, want)
		}
	}
	// A pricing that fails says why (the call would meet it too).
	prices = map[string]any{}
	op, _ := ops.Get("ai.make")
	if _, err := s.spends(context.Background(), op, map[string]any{"kind": "music"}); err == nil {
		t.Error("a failed pricing let the call go on")
	}
}

// A connection that may make changes doesn't spend unless the person allowed
// that too: a paid call is refused before any token, a priced dry run or work
// on this computer goes on to its confirmation, and with spending allowed the
// paid call does too. A pricing that fails is the call's answer.
func TestSpendingNeedsItsOwnAllowance(t *testing.T) {
	if _, err := New(viaMCP(testapp.New(t)), Options{AllowSpending: true}); err == nil {
		t.Fatal("spending was allowed on a read-only connection")
	}
	saved := priceCall
	defer func() { priceCall = saved }()
	priceCall = func(_ context.Context, _ *app.App, op string, in map[string]any) (any, error) {
		if in["mode"] == "fail" {
			return nil, apperr.New(apperr.NotFound, "There's no such version.")
		}
		if op == "sound.enhance" && in["mode"] == "cloud" {
			return map[string]any{"estimate": map[string]any{"credits": 1000}}, nil
		}
		return map[string]any{"estimate": map[string]any{"credits": 0}}, nil
	}
	a := viaMCP(testapp.New(t))
	for _, allowed := range []bool{false, true} {
		c := start(t, a, Options{AllowChanges: true, AllowSpending: allowed, TokenTTL: time.Minute})
		c.request("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
		for _, tc := range []struct {
			name  string
			input map[string]any
			paid  bool
		}{
			{"generate.shot", map[string]any{"prompt": "a dog on a beach"}, true},
			{"sound.enhance", map[string]any{"revisionId": "rev_00000000000000000000000000", "target": "everyone", "mode": "cloud"}, true},
			{"assistant.ask", map[string]any{"text": "tighten the intro"}, true},
			{"generate.shot", map[string]any{"prompt": "a dog on a beach", "dryRun": true}, false},
			{"sound.enhance", map[string]any{"revisionId": "rev_00000000000000000000000000", "target": "everyone", "mode": "clean"}, false},
		} {
			r := c.call("run_change", map[string]any{"name": tc.name, "input": tc.input})
			refused := strings.Contains(fmt.Sprint(r["structuredContent"]), "mcp_no_spending")
			if want := tc.paid && !allowed; refused != want {
				t.Errorf("spending allowed %v, %s %v: refused = %v, want %v: %v", allowed, tc.name, tc.input, refused, want, r)
			}
			if refused && errCode(r) != "E_UNAUTHORIZED" {
				t.Errorf("%s: refused with %s", tc.name, errCode(r))
			}
			if !refused && errCode(r) != "E_CONFIRM_REQUIRED" {
				t.Errorf("spending allowed %v, %s: not on to its confirmation: %v", allowed, tc.name, r)
			}
		}
		if !allowed {
			r := c.call("run_change", map[string]any{"name": "sound.enhance", "input": map[string]any{"revisionId": "rev_00000000000000000000000000", "target": "everyone", "mode": "fail"}})
			if errCode(r) != "E_NOT_FOUND" {
				t.Errorf("a pricing that fails: %v", r)
			}
		}
	}
}
