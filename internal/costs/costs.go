// Package costs says what an edit that uses paid outside AI services is
// expected to cost before anything is sent (D208): one total, "about $0.40",
// that opens to what it draws on, part by part: the service, its model, the
// source (the clip, the script lines, the picture), the units and each part's
// estimate.
//
// Generated shots (internal/generate), the AI voice (internal/voiceover) and
// cloud voice clean-up (internal/sound) build their parts from their own dated
// prices; Claude's panel cards carry the same record, a Recipes run adds its
// steps' estimates up with Add, and the dashboard shows every one with a single
// component (web/src/components/CostEstimate.tsx). It is always an estimate:
// what a service charges can differ a little, and budgets, daily limits and
// project caps still decide what may be spent. The package depends on nothing
// in MetaRush, so any package can use it.
package costs

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Estimate is what something is expected to cost, with the parts it draws on.
// Its schema name is CostEstimate.
type Estimate struct {
	USD   float64 `bson:"usd" json:"usd" jsonschema:"description=The estimate in US dollars (to a tenth of a cent; About says a smaller one as less than a cent)."`
	About string  `bson:"about" json:"about" jsonschema:"description=The estimate in words: about $0.40\\, less than a cent\\, nothing (made before)."`
	Parts []Part  `bson:"parts" json:"parts" jsonschema:"description=What it draws on\\, part by part."`
	// Notes are what to know about the estimate, in plain words (whose prices
	// of which day, what a plan's credits cost).
	Notes []string `bson:"notes,omitempty" json:"notes,omitempty" jsonschema:"description=What to know about the estimate\\, in plain words."`
}

// Part is one line of an estimate: what one outside service is asked for.
// Its schema name is CostPart.
type Part struct {
	// Step names the step the part belongs to, when the estimate covers several
	// (a Recipes run: "Step 3: the AI voice").
	Step    string `bson:"step,omitempty" json:"step,omitempty" jsonschema:"description=The step it belongs to\\, when the estimate covers several (a run of several steps)."`
	Service string `bson:"service" json:"service" jsonschema:"description=The outside service: ElevenLabs\\, Google Gemini."`
	What    string `bson:"what" json:"what" jsonschema:"description=What it makes: the AI voice\\, a generated shot\\, a clearer voice."`
	Model   string `bson:"model,omitempty" json:"model,omitempty" jsonschema:"description=The service's model\\, by name."`
	// Source is what the part draws on, in plain words, with people's own
	// words in curly quotes: "Line 3: “Welcome to the garden”", "the frame at
	// 0:04.0 of “Beach”", "“Interview” from 0:12 to 1:24".
	Source string  `bson:"source,omitempty" json:"source,omitempty" jsonschema:"description=What it draws on: the clip\\, the script line\\, the picture."`
	Amount float64 `bson:"amount" json:"amount" jsonschema:"description=How many units."`
	Unit   string  `bson:"unit" json:"unit" jsonschema:"description=What is counted: credits\\, seconds of video\\, tokens."`
	Units  string  `bson:"units" json:"units" jsonschema:"description=The units in words: 86 characters (as many credits)."`
	// Rate is the price the part uses, with the day it was read.
	Rate string  `bson:"rate,omitempty" json:"rate,omitempty" jsonschema:"description=The price it uses\\, with its date: $0.08 per 1\\,000 credits (ElevenLabs' prices of 23 Oct 2026)."`
	USD  float64 `bson:"usd" json:"usd" jsonschema:"description=This part's estimate in US dollars (to a millionth: a few tokens cost less than a cent\\, not nothing)."`
	// Free: made before with the same settings, so nothing is charged.
	Free bool `bson:"free,omitempty" json:"free,omitempty" jsonschema:"description=Made before with the same settings: nothing is charged."`
	// Limits names whose spending limits cover it when they aren't the
	// service's own (D241): AI services share generated video's budget and
	// daily limit (D226), whichever service does the work.
	Limits string `bson:"limits,omitempty" json:"limits,omitempty" jsonschema:"description=Whose spending limits cover it\\, when not the service's own: AI services (generated video's budget and daily limit)."`
}

// LimitsKey is the name a part's spending limits are registered under.
func (p Part) LimitsKey() string {
	if p.Limits != "" {
		return p.Limits
	}
	return p.Service
}

// Of adds parts up into an estimate.
func Of(parts ...Part) Estimate {
	e := Estimate{Parts: []Part{}}
	e.Append(parts...)
	return e
}

// Append puts more parts in and adds them to the total.
func (e *Estimate) Append(parts ...Part) {
	if e.Parts == nil {
		e.Parts = []Part{}
	}
	for _, p := range parts {
		if p.Free {
			p.USD = 0
		}
		p.USD = micro(p.USD) // a few tokens are less than a cent, not nothing
		e.Parts = append(e.Parts, p)
	}
	e.USD = Round(e.sum())
	e.About = e.words()
}

// sum adds the parts up, unrounded.
func (e Estimate) sum() float64 {
	total := 0.0
	for _, p := range e.Parts {
		total += p.USD
	}
	return total
}

func micro(usd float64) float64 { return math.Round(usd*1e6) / 1e6 }

// Add puts another estimate's parts in (a Recipes run adds each step's): under
// step's name when one is given, with its notes.
func (e *Estimate) Add(step string, o Estimate) {
	parts := make([]Part, 0, len(o.Parts))
	for _, p := range o.Parts {
		if step != "" && p.Step == "" {
			p.Step = step
		}
		parts = append(parts, p)
	}
	e.Append(parts...)
	for _, n := range o.Notes {
		e.Note(n)
	}
}

// Note adds a sentence about the estimate (once).
func (e *Estimate) Note(s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	for _, n := range e.Notes {
		if n == s {
			return
		}
	}
	e.Notes = append(e.Notes, s)
}

// Total sets the total a service worked out itself (its own rounding is what a
// person agrees to); the parts stay as they are.
func (e Estimate) Total(usd float64) Estimate {
	e.USD = Round(usd)
	e.About = e.words()
	return e
}

// Empty reports whether nothing is asked of any service.
func (e Estimate) Empty() bool { return len(e.Parts) == 0 }

// AllFree reports whether every part was made before (nothing is charged).
func (e Estimate) AllFree() bool {
	if len(e.Parts) == 0 {
		return false
	}
	for _, p := range e.Parts {
		if !p.Free {
			return false
		}
	}
	return true
}

// Services are the services the estimate draws on, in order of first use.
func (e Estimate) Services() []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range e.Parts {
		if p.Service != "" && !seen[p.Service] {
			seen[p.Service] = true
			out = append(out, p.Service)
		}
	}
	return out
}

// Summary says the estimate in one sentence: "About $0.40 for the AI voice
// (ElevenLabs) and a generated shot (Google Gemini)."
func (e Estimate) Summary() string {
	if len(e.Parts) == 0 {
		return "Nothing is asked of an outside service."
	}
	var parts []string
	seen := map[string]bool{}
	for _, p := range e.Parts {
		k := p.What + " (" + p.Service + ")"
		if p.What == "" || seen[k] {
			continue
		}
		seen[k] = true
		parts = append(parts, k)
	}
	return upperFirst(e.words()) + " for " + joinAnd(parts) + "."
}

func (e Estimate) words() string {
	switch {
	case e.AllFree():
		return "nothing (made before)"
	case e.USD < 0.01 && e.sum() > 0:
		return "less than a cent" // parts that cost something, if very little
	}
	return About(e.USD)
}

// Round rounds money to a tenth of a cent.
func Round(usd float64) float64 { return math.Round(usd*1000) / 1000 }

// Money says an amount the way people read prices: $0.40, $12.50, $120,
// less than a cent, $0.
func Money(usd float64) string {
	switch {
	case usd <= 0:
		return "$0"
	case usd < 0.01:
		return "less than a cent"
	}
	cents := int64(math.Round(usd * 100))
	whole, frac := cents/100, cents%100
	if whole >= 100 && frac == 0 {
		return "$" + Thousands(float64(whole))
	}
	return fmt.Sprintf("$%s.%02d", Thousands(float64(whole)), frac)
}

// About says an estimate: "about $0.40", "less than a cent", "nothing".
func About(usd float64) string {
	switch {
	case usd <= 0:
		return "nothing"
	case usd < 0.01:
		return "less than a cent"
	}
	return "about " + Money(usd)
}

// Thousands writes a whole number with thousands commas: 12,400.
func Thousands(v float64) string {
	s := strconv.FormatInt(int64(math.Round(v)), 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// Count says a number with its unit, singular or plural: "1 credit",
// "1,240 credits", "6 seconds of video" (many given as "seconds of video").
func Count(n float64, one, many string) string {
	if math.Abs(n-1) < 1e-9 {
		return "1 " + one
	}
	if n != math.Trunc(n) && n < 100 {
		s := strconv.FormatFloat(math.Round(n*10)/10, 'f', -1, 64)
		return s + " " + many
	}
	return Thousands(n) + " " + many
}

// Day says a YYYY-MM-DD date the way MetaRush writes dates: 23 Oct 2026 (the
// text as given when it isn't one).
func Day(ymd string) string {
	months := []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	p := strings.Split(ymd, "-")
	if len(p) != 3 {
		return ymd
	}
	y, e1 := strconv.Atoi(p[0])
	m, e2 := strconv.Atoi(p[1])
	d, e3 := strconv.Atoi(p[2])
	if e1 != nil || e2 != nil || e3 != nil || m < 1 || m > 12 || d < 1 || d > 31 {
		return ymd
	}
	return fmt.Sprintf("%d %s %d", d, months[m-1], y)
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return "nothing"
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}
