package sound

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// ShowOut is a cut's sound in plain words: the Sound tab and `metarush sound show`.
type ShowOut struct {
	RevisionID   string            `json:"revisionId"`
	Parts        []PartView        `json:"parts" jsonschema:"description=Camera sound\\, narration\\, music and effects (the parts this cut has)."`
	People       []PersonView      `json:"people" jsonschema:"description=Everyone who speaks on camera\\, in the order they first speak."`
	Narration    *NarrationView    `json:"narration,omitempty"`
	Auto         AutoView          `json:"auto"`
	Music        []MusicView       `json:"music"`
	Modes        []ModeView        `json:"modes" jsonschema:"description=The ways to make a voice clearer and whether each works on this computer."`
	Mix          *voiceover.MixOut `json:"mix" jsonschema:"description=The resolved mix (as mix.show)."`
	Explanations []string          `json:"explanations"`
	Warnings     []editspec.Issue  `json:"warnings"`
	Summary      string            `json:"summary"`
}

// PartView is one part of the mix.
type PartView struct {
	Part    string  `json:"part" jsonschema:"enum=source,enum=voiceover,enum=music,enum=sfx"`
	Label   string  `json:"label"`
	GainDB  float64 `json:"gainDb"`
	Mute    bool    `json:"mute"`
	Solo    bool    `json:"solo"`
	Muted   bool    `json:"muted" jsonschema:"description=Silent: muted\\, or another part plays alone."`
	Level   string  `json:"level"`
	Present bool    `json:"present" jsonschema:"description=The cut has this part."`
}

// PersonView is one person's voice.
type PersonView struct {
	Name    string       `json:"name"`
	Seconds float64      `json:"seconds" jsonschema:"description=How long they speak in the cut."`
	GainDB  float64      `json:"gainDb"`
	Level   string       `json:"level"`
	Enhance *EnhanceView `json:"enhance,omitempty"`
}

// EnhanceView is a voice's clearer version.
type EnhanceView struct {
	Mode     string       `json:"mode" jsonschema:"enum=clean,enum=studio,enum=cloud"`
	Strength float64      `json:"strength"`
	Off      bool         `json:"off" jsonschema:"description=The original plays (A/B); the settings and the clearer version are kept."`
	State    string       `json:"state" jsonschema:"enum=ready,enum=outdated,enum=missing,description=ready: what plays matches the settings; outdated: the settings changed since; missing: part of what plays has no clearer version yet."`
	Score    *PersonScore `json:"score,omitempty" jsonschema:"description=Before and after."`
	Words    string       `json:"words"`
}

// NarrationView is the narration's clearer version.
type NarrationView struct {
	Recorded int          `json:"recorded" jsonschema:"description=Lines with a recording (AI voices are clear already)."`
	Enhance  *EnhanceView `json:"enhance,omitempty"`
	GainDB   float64      `json:"gainDb"`
}

// AutoView is the automatic help.
type AutoView struct {
	Ducking     bool    `json:"ducking" jsonschema:"description=Music gets quieter when someone talks."`
	Depth       float64 `json:"depth" jsonschema:"description=How much quieter (dB)."`
	LevelSpeech bool    `json:"levelSpeech" jsonschema:"description=Voices are evened out from clip to clip."`
	SmoothCuts  bool    `json:"smoothCuts" jsonschema:"description=The sound blends for a moment at each cut."`
	Loudness    string  `json:"loudness" jsonschema:"description=auto or the target (e.g. -14)."`
	Target      float64 `json:"target" jsonschema:"description=The overall loudness target in effect."`
	Words       string  `json:"words"`
}

// MusicView is one music track.
type MusicView struct {
	ID         string  `json:"id"`
	AssetID    string  `json:"assetId"`
	Title      string  `json:"title"`
	Fit        string  `json:"fit" jsonschema:"enum=auto,enum=manual"`
	Loop       bool    `json:"loop"`
	In         float64 `json:"in"`
	Out        float64 `json:"out"`
	At         float64 `json:"at"`
	From       float64 `json:"from" jsonschema:"description=Where it starts playing in the video (seconds)."`
	To         float64 `json:"to" jsonschema:"description=Where it stops (seconds)."`
	Repeats    int     `json:"repeats"`
	EndsOnBeat bool    `json:"endsOnBeat"`
	Ending     string  `json:"ending,omitempty" jsonschema:"enum=phrase,enum=beat,description=Where a fitted track ends (absent: on a beat\\, as music fitted before)."`
	EndsOn     string  `json:"endsOn,omitempty" jsonschema:"enum=phrase,enum=bar,enum=beat,description=What it ends on now."`
	Music      string  `json:"music,omitempty" jsonschema:"description=What was found in the music (tempo and how it changes\\, bars\\, phrases)\\, in words."`
	EndsEarly  bool    `json:"endsEarly"`
	Duration   float64 `json:"duration" jsonschema:"description=The file's length."`
	GainDB     float64 `json:"gainDb"`
	Duck       string  `json:"duck" jsonschema:"enum=auto,enum=on,enum=off"`
	DuckDB     float64 `json:"duckDb,omitempty"`
	Tempo      float64 `json:"tempo,omitempty"`
	Rights     bool    `json:"rights" jsonschema:"description=Someone said where the music comes from."`
	Placement  string  `json:"placement"`
}

// ModeView says whether a way of making voices clearer works here.
type ModeView struct {
	Mode    string `json:"mode" jsonschema:"enum=clean,enum=studio,enum=cloud"`
	Label   string `json:"label"`
	Ready   bool   `json:"ready"`
	Problem string `json:"problem,omitempty"`
	Speed   string `json:"speed" jsonschema:"description=How fast\\, in words."`
	Cost    string `json:"cost"`
	Size    string `json:"size,omitempty" jsonschema:"description=What setting it up downloads."`
}

var partLabels = map[string]string{editspec.BusSource: "Camera sound", editspec.BusVoiceover: "Narration", editspec.BusMusic: "Music", editspec.BusSFX: "Effects"}

func soundShow(ctx context.Context, a *app.App, revisionID string) (*ShowOut, error) {
	c, err := loadCut(ctx, a, revisionID)
	if err != nil {
		return nil, err
	}
	mv := voiceover.MixView(c.rev.ID, c.spec, c.plan)
	out := &ShowOut{RevisionID: c.rev.ID, Parts: []PartView{}, People: []PersonView{}, Music: []MusicView{}, Mix: mv, Explanations: append([]string{}, mv.Explanations...), Warnings: []editspec.Issue{}}
	present := map[string]bool{}
	for _, pc := range c.plan.Clips {
		if pc.HasAudio {
			present[editspec.BusSource] = true
		}
	}
	if v := c.spec.Voiceover; v != nil {
		for _, b := range v.Beats {
			if b.Take != nil {
				present[editspec.BusVoiceover] = true
			}
		}
	}
	for _, t := range c.spec.Audio {
		switch t.Role {
		case editspec.RoleMusic:
			present[editspec.BusMusic] = true
		case editspec.RoleSFX:
			present[editspec.BusSFX] = true
		case editspec.RoleVoiceover:
			present[editspec.BusVoiceover] = true
		case editspec.RoleDialogue:
			present[editspec.BusSource] = true
		}
	}
	for _, b := range mv.Buses {
		out.Parts = append(out.Parts, PartView{Part: b.Name, Label: partLabels[b.Name], GainDB: b.GainDB, Mute: b.Mute, Solo: b.Solo, Muted: b.Muted, Level: b.Level, Present: present[b.Name]})
	}
	m := c.spec.Mix
	// People: their level and clearer voice.
	names, secs := c.speakers()
	copies := map[string]*record{} // variant → its record
	if m != nil {
		for _, e := range m.Enhanced {
			if r := copyRecordByVariant(ctx, a, e.Variant); r != nil {
				copies[e.Variant] = r
			}
		}
	}
	issues := editspec.Validate(editspec.Clone(c.spec), c.facts, nil, false)
	for _, is := range issues {
		if strings.HasPrefix(is.Path, "/mix") || strings.HasPrefix(is.Path, "/audio") || is.Code == "enhance_missing" {
			out.Warnings = append(out.Warnings, is)
		}
	}
	// A setting for a whole recording ("everyone") covers the people heard in it.
	recordingOf := map[string]*editspec.Enhance{}
	for _, u := range c.uses() {
		rv := m.RecordingVoice(u.asset)
		if rv == nil || rv.Enhance == nil {
			continue
		}
		for _, sp := range editspec.SpeakersIn(c.transcript(u.asset), u.from, u.to) {
			if recordingOf[sp] == nil {
				recordingOf[sp] = rv.Enhance
			}
		}
	}
	for _, n := range names {
		pv := PersonView{Name: n, Seconds: math.Round(secs[n]*10) / 10, Level: "As recorded"}
		v := m.VoiceFor(n)
		if v != nil && v.GainDB != nil {
			pv.GainDB = *v.GainDB
			pv.Level = levelWords(pv.GainDB)
			out.Explanations = append(out.Explanations, fmt.Sprintf("%s is %s over their speech.", n, lowerFirst(pv.Level)))
		}
		var e *editspec.Enhance
		if v != nil && v.Enhance != nil {
			e = v.Enhance
		} else if v == nil || v.Enhance == nil {
			e = recordingOf[n]
		}
		if e != nil {
			ev := &EnhanceView{Mode: e.Mode, Strength: e.StrengthOf(), Off: e.Off, State: "ready"}
			var parts []CopyView
			for _, en := range m.Enhanced {
				if r := copies[en.Variant]; r != nil {
					parts = append(parts, CopyView{Scores: r.Scores})
				}
			}
			for _, s := range mergeScores(parts) {
				if s.Speaker == n {
					sc := s
					ev.Score = &sc
				}
			}
			ev.State = c.enhanceState(issues, n)
			ev.Words = enhanceViewWords(n, ev)
			out.Explanations = append(out.Explanations, ev.Words)
			pv.Enhance = ev
		}
		out.People = append(out.People, pv)
	}
	// Narration.
	if v := c.spec.Voiceover; v != nil {
		nv := &NarrationView{}
		if m != nil && m.Voiceover != nil && m.Voiceover.GainDB != nil {
			nv.GainDB = *m.Voiceover.GainDB
		}
		var parts []CopyView
		for _, b := range v.Beats {
			t := b.Take
			if t == nil || (t.Source != models.TakeRecorded && t.Source != models.TakeImported) {
				continue
			}
			nv.Recorded++
			if t.Processed == nil {
				continue
			}
			md, st, ok := ParsePreset(t.Processed.Preset)
			if !ok {
				continue
			}
			if nv.Enhance == nil {
				nv.Enhance = &EnhanceView{Mode: md, Strength: st, Off: !t.UseProcessed, State: "ready"}
			}
			if r := copyRecordByVariant(ctx, a, t.Processed.AssetID); r != nil {
				parts = append(parts, CopyView{Scores: r.Scores})
			}
		}
		if nv.Enhance != nil {
			if ms := mergeScores(parts); len(ms) > 0 {
				nv.Enhance.Score = &ms[0]
			}
			nv.Enhance.Words = enhanceViewWords("The narration", nv.Enhance)
			out.Explanations = append(out.Explanations, nv.Enhance.Words)
		}
		if nv.Recorded > 0 || nv.Enhance != nil {
			out.Narration = nv
		}
	}
	// Automatic help.
	eff := c.plan.Mix
	out.Auto = AutoView{Ducking: eff.Ducking.Auto, Depth: math.Abs(eff.Ducking.MusicUnderVoiceoverDB), LevelSpeech: eff.LevelSpeech,
		SmoothCuts: m != nil && m.CutBlend != nil && *m.CutBlend > 0, Loudness: "auto", Target: eff.TargetLUFS}
	if m != nil && m.Loudness != nil && m.Loudness.TargetLUFS != nil {
		out.Auto.Loudness = fmt.Sprintf("%g", *m.Loudness.TargetLUFS)
	}
	out.Auto.Words = autoWords(out.Auto)
	// Music.
	var musicIDs []string
	for _, t := range c.spec.Audio {
		if t.Role == editspec.RoleMusic {
			musicIDs = append(musicIDs, t.AssetID)
		}
	}
	beats := evidence.LoadBeats(ctx, a.DB, a.WorkspaceID(), musicIDs)
	grids := evidence.LoadGrids(ctx, a.DB, a.WorkspaceID(), musicIDs)
	for _, t := range c.spec.Audio {
		if t.Role != editspec.RoleMusic {
			continue
		}
		mv := MusicView{ID: t.ID, AssetID: t.AssetID, Title: t.ID, Fit: "manual", Loop: t.Loop, In: t.In, Out: t.Out, At: t.At, Duck: "auto"}
		if t.Fit == editspec.TrackFitAuto {
			mv.Fit = "auto"
		}
		as := c.asset(t.AssetID)
		if as != nil {
			mv.Title, mv.Duration = as.DisplayTitle(), as.Media.Duration
			mv.Rights = as.Rights != nil && as.Rights.License != ""
		}
		if t.Rights != nil && t.Rights.License != "" {
			mv.Rights = true
		}
		if t.GainDB != nil {
			mv.GainDB = *t.GainDB
		}
		if t.Duck != nil {
			mv.Duck = map[bool]string{true: "on", false: "off"}[*t.Duck]
		} else if t.DuckUnderDialogue {
			mv.Duck = "on"
		}
		if t.DuckDB != nil {
			mv.DuckDB = *t.DuckDB
		}
		if fi, ok := editspec.MusicFit(c.spec, t, c.facts); ok {
			mv.From, mv.To, mv.Repeats, mv.EndsOnBeat, mv.EndsEarly, mv.EndsOn = fi.From, fi.To, fi.Repeats, fi.EndsOnBeat, fi.EndsEarly, fi.EndsOn
		}
		mv.Ending = t.Ending
		mv.Tempo = beats[t.AssetID].Tempo
		if g, ok := grids[t.AssetID]; ok && (t.Ending != "" || mv.Tempo == 0) {
			mv.Tempo, mv.Music = g.Tempo, gridWords(g)
		}
		mv.Placement = placementWords(c.spec, t, c.facts, mv.Tempo > 0)
		out.Music = append(out.Music, mv)
	}
	// Ways to make voices clearer, on this computer.
	comp := &Components{Home: a.Cfg.Home(), Tools: a.Tools}
	for _, st := range comp.Status() {
		mv := ModeView{Mode: st.Name, Ready: st.Installed, Problem: st.Problem, Size: st.Size, Cost: "Free"}
		switch st.Name {
		case ComponentClean:
			mv.Label, mv.Speed = "Clean", "Fast: a minute of speech takes a few seconds."
		case ComponentStudio:
			mv.Label, mv.Speed = "Studio", "Slower and stronger: it rebuilds the voice."
			if st.Speed > 0 {
				where := "on this computer's processor"
				switch st.Device {
				case "mps":
					where = "on this Mac's graphics chip"
				case "cuda":
					where = "on this computer's NVIDIA graphics card"
				}
				mv.Speed = fmt.Sprintf("About %.0f seconds of work per minute of speech %s.", 60*(2*st.Speed+0.8), where)
				if st.DeviceNote != "" {
					mv.Speed += " " + st.DeviceNote
				}
			}
		}
		out.Modes = append(out.Modes, mv)
	}
	cloud := ModeView{Mode: "cloud", Label: "Cloud (ElevenLabs)", Speed: "Depends on the connection.", Cost: "1,000 ElevenLabs credits a minute of speech; you see the price first."}
	if key, _, _ := providers.Key(a.Cfg, providers.ElevenLabsName); key != "" {
		cloud.Ready = true
	} else {
		cloud.Problem = "Add your ElevenLabs key on this computer (Settings → Integrations) to use it."
	}
	out.Modes = append(out.Modes, cloud)
	out.Summary = showSummary(out)
	return out, nil
}

// enhanceState says whether what plays for a person matches their clearer
// voice: an issue about a copy made with other settings, or about a clip that
// still plays their own voice. Issues are matched by code and place (what the
// copy or clip covers), so their wording can change freely.
func (c *cut) enhanceState(issues []editspec.Issue, name string) string {
	for _, is := range issues {
		if is.Code == "enhanced_outdated" && (c.copyCovers(is.Path, name) || strings.Contains(is.Message, name+":")) {
			return "outdated"
		}
		if is.Code == "enhance_missing" && (c.clipWants(is.Path, name) || strings.Contains(is.Message, name+"'s")) {
			return "missing"
		}
	}
	return "ready"
}

// copyCovers reports whether the clearer copy at path (/mix/enhanced/N) was
// made, or should now be made, with settings for this person.
func (c *cut) copyCovers(path, name string) bool {
	var i int
	m := c.spec.Mix
	if _, err := fmt.Sscanf(path, "/mix/enhanced/%d", &i); err != nil || m == nil || i < 0 || i >= len(m.Enhanced) {
		return false
	}
	e := m.Enhanced[i]
	if strings.Contains(e.Settings, name+":") {
		return true
	}
	v, ok := c.facts.Asset(e.Variant)
	if !ok {
		return false
	}
	speakers := editspec.SpeakersIn(c.facts.Transcript(e.AssetID), e.Start, e.Start+v.Duration)
	return strings.Contains(m.VoiceSettingsFor(e.AssetID, speakers), name+":")
}

// clipWants reports whether the clip at path (/clips/N) plays this person,
// set to sound clearer, without a clearer copy.
func (c *cut) clipWants(path, name string) bool {
	var i int
	m := c.spec.Mix
	if _, err := fmt.Sscanf(path, "/clips/%d", &i); err != nil || m == nil || i < 0 || i >= len(c.plan.Clips) {
		return false
	}
	pc := c.plan.Clips[i]
	if rv := m.RecordingVoice(pc.AssetID); rv != nil && rv.Enhance.Active() {
		return false // everyone in the recording: said for the whole recording, not per person
	}
	for _, sp := range editspec.SpeakersIn(c.facts.Transcript(pc.AssetID), pc.SourceIn, pc.SourceOut) {
		if strings.TrimSpace(sp) == name {
			if v := m.VoiceFor(sp); v != nil && v.Enhance.Active() {
				return true
			}
		}
	}
	return false
}

// lowerFirst lowercases a phrase's first letter ("Louder (6 dB)" → "louder (6 dB)").
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func levelWords(db float64) string {
	amt := strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.1f", math.Abs(db)), "0"), ".") + " dB"
	switch {
	case math.Abs(db) < 0.25:
		return "As recorded"
	case db <= -18:
		return "Much quieter (" + amt + ")"
	case db < -6:
		return "Quieter (" + amt + ")"
	case db < 0:
		return "A little quieter (" + amt + ")"
	case db <= 4:
		return "A little louder (" + amt + ")"
	}
	return "Louder (" + amt + ")"
}

func enhanceViewWords(who string, ev *EnhanceView) string {
	if ev.Off {
		return fmt.Sprintf("%s plays as recorded for now (a clearer version is kept: %s, %d%%).", who, modeWords(ev.Mode), int(math.Round(ev.Strength*100)))
	}
	s := fmt.Sprintf("%s sounds clearer (%s, %d%%)", who, modeWords(ev.Mode), int(math.Round(ev.Strength*100)))
	switch ev.State {
	case "outdated":
		s += ", but the settings changed since it was made: make it again to hear them"
	case "missing":
		s += ", but some clips still play the original: make it again to cover them"
	}
	return s + "."
}

func autoWords(av AutoView) string {
	var parts []string
	if av.Ducking {
		parts = append(parts, fmt.Sprintf("music gets quieter when someone talks (by %.0f dB)", av.Depth))
	}
	if av.LevelSpeech {
		parts = append(parts, "voices are evened out from clip to clip")
	}
	if av.SmoothCuts {
		parts = append(parts, "cuts blend smoothly")
	}
	if len(parts) == 0 {
		return "No automatic help is on."
	}
	return upper(strings.Join(parts, ", ")) + "."
}

func showSummary(o *ShowOut) string {
	var parts []string
	for _, p := range o.People {
		if p.Enhance != nil && !p.Enhance.Off {
			parts = append(parts, p.Name+" sounds clearer")
		} else if p.GainDB != 0 {
			parts = append(parts, fmt.Sprintf("%s %s", p.Name, lowerFirst(p.Level)))
		}
	}
	if o.Narration != nil && o.Narration.Enhance != nil && !o.Narration.Enhance.Off {
		parts = append(parts, "the narration sounds clearer")
	}
	if n := len(o.Music); n > 0 {
		parts = append(parts, map[bool]string{true: "music under the video", false: fmt.Sprintf("%d music tracks", n)}[n == 1])
	}
	if len(parts) == 0 {
		return "Everything plays as it was recorded."
	}
	return upper(strings.Join(parts, "; ")) + "."
}
