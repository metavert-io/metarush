package assistant

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/conversation"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/sound"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// Sound in the Claude Code panel (D88–D91, D111): a person's level, the music
// getting quieter under voices and the other levels, a clearer voice, music
// added or fitted to the video, and setting clearer voices up. The card is
// built here from Claude's command and previewed with nothing saved, made or
// spent (sound.Preview*): a clearer voice in the cloud shows its price first
// and can't be confirmed over the project's budget or the plan's credits;
// studio quality shows its one-time setup (about 1.8 GB), which the card runs
// first when it isn't set up. The person confirms it; the dashboard runs the
// same op as the CLI, with apply, on the human channel.

// soundCommands are the commands describeSound turns into cards.
var soundCommands = map[string]bool{"sound levels": true, "sound enhance": true, "sound music add": true, "sound music set": true, "sound setup": true}

func (m *Manager) describeSound(ctx context.Context, call *cliCall, a *AssistantAction) {
	if call.Path == "sound setup" {
		m.describeSoundSetup(ctx, call, a)
		return
	}
	r := m.revisionFor(ctx, call)
	if r == nil {
		incomplete(a, "Change the sound", "Claude did not name the cut (--revision, or --project for its latest version).")
		return
	}
	switch call.Path {
	case "sound levels":
		m.describeLevels(ctx, call, r, a)
	case "sound enhance":
		m.describeEnhance(ctx, call, r, a)
	case "sound music add":
		m.describeMusicAdd(ctx, call, r, a)
	case "sound music set":
		m.describeMusicSet(ctx, call, r, a)
	}
}

// numFlag reads a number flag; ok is false when it is there but isn't a number.
func numFlag(call *cliCall, name string) (*float64, bool) {
	v, has := call.Flags[name]
	if !has {
		return nil, true
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, false
	}
	return &f, true
}

// onOff reads an on/off flag (nil when it isn't there); ok is false for anything else.
func onOff(call *cliCall, name string) (*bool, bool) {
	v, has := call.Flags[name]
	if !has {
		return nil, true
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "yes", "true", "":
		t := true
		return &t, true
	case "off", "no", "false":
		f := false
		return &f, true
	}
	return nil, false
}

// speakersIn are the people who speak in a cut, as the Sound tab names them.
func (m *Manager) speakersIn(ctx context.Context, r *models.Revision) []string {
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	out, err := callOp[sound.ShowOut](lctx, m, "sound.show", voiceover.RevisionIn{RevisionID: r.ID})
	if err != nil {
		return nil
	}
	var names []string
	for _, p := range out.People {
		names = append(names, p.Name)
	}
	return names
}

// personNamed is the speaker a command names, as the cut names them ("" when
// nobody in the cut is called that).
func personNamed(names []string, ref string) string {
	ref = strings.TrimSpace(ref)
	for _, n := range names {
		if strings.EqualFold(n, ref) {
			return n
		}
	}
	if l := voiceLabel(ref); l != quoted(ref) { // speaker_01, 2 → "Speaker 2"
		for _, n := range names {
			if strings.EqualFold(n, l) {
				return n
			}
		}
	}
	return ""
}

func (m *Manager) describeLevels(ctx context.Context, call *cliCall, r *models.Revision, a *AssistantAction) {
	const title = "Change how loud things are"
	if hasBool(call, "preview") || hasBool(call, "original") {
		incomplete(a, "Hear a change to the sound", "A preview only shows how a change would sound; ask without --preview to change it.")
		return
	}
	in := sound.LevelsIn{RevisionID: r.ID}
	gain, ok := numFlag(call, "gain")
	if !ok {
		incomplete(a, title, "Claude gave a level that isn't a number (dB).")
		return
	}
	part, speaker, track := strings.TrimSpace(call.Flags["part"]), strings.TrimSpace(call.Flags["speaker"]), strings.TrimSpace(call.Flags["track"])
	targets := 0
	for _, s := range []string{part, speaker, track} {
		if s != "" {
			targets++
		}
	}
	if gain != nil && targets != 1 {
		incomplete(a, title, "Claude did not say whose level: one part of the sound, one person or one music track.")
		return
	}
	var person string
	if part != "" {
		bus, known := sound.Part(part)
		if !known {
			incomplete(a, title, "There is no part of the sound called "+quoted(part)+": camera, narration, music or effects.")
			return
		}
		bc := voiceover.BusChange{Bus: bus, GainDB: gain}
		switch {
		case hasBool(call, "mute"):
			bc.Mute = flag(true)
		case hasBool(call, "unmute"):
			bc.Mute = flag(false)
		}
		switch {
		case hasBool(call, "solo"):
			bc.Solo = flag(true)
		case hasBool(call, "unsolo"):
			bc.Solo = flag(false)
		}
		in.Parts = []voiceover.BusChange{bc}
	}
	if speaker != "" {
		if gain == nil {
			incomplete(a, title, "Claude did not say how much louder or quieter (dB).")
			return
		}
		if person = personNamed(m.speakersIn(ctx, r), speaker); person == "" {
			incomplete(a, title, "Nobody called "+quoted(speaker)+" speaks in this cut.")
			return
		}
		in.People = []sound.PersonLevel{{Name: person, GainDB: math.Max(-30, math.Min(12, *gain))}}
	}
	if track != "" {
		ml := sound.MusicLevel{Track: track, GainDB: gain}
		if v, has := call.Flags["track-duck"]; has {
			switch d := strings.ToLower(strings.TrimSpace(v)); d {
			case "on", "off", "auto":
				ml.Duck = d
			default:
				incomplete(a, title, "Music gets quieter under voices on, off or as the mix decides (auto).")
				return
			}
		}
		in.Music = []sound.MusicLevel{ml}
	}
	cs, ok1 := onOff(call, "camera-sound")
	duck, ok2 := onOff(call, "duck")
	level, ok3 := onOff(call, "level-speech")
	smooth, ok4 := onOff(call, "smooth-cuts")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		incomplete(a, title, "Claude gave a setting that isn't on or off.")
		return
	}
	switch clipRef := strings.TrimSpace(call.Flags["clip"]); {
	case cs != nil && clipRef != "":
		in.Clips = []voiceover.ClipSound{{Clip: clip(clipRef, 64), Muted: !*cs}}
	case cs != nil:
		muted := !*cs
		in.MuteSource = &muted
	}
	in.Ducking, in.LevelSpeech, in.SmoothCuts = duck, level, smooth
	if d, ok := numFlag(call, "depth"); !ok {
		incomplete(a, title, "Claude gave a depth that isn't a number (dB).")
		return
	} else if d != nil {
		v := math.Abs(*d)
		in.Depth = &v
	}
	in.Loudness = clip(strings.TrimSpace(call.Flags["loudness"]), 8)
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	po, err := sound.PreviewLevels(lctx, m.App, in)
	if err != nil {
		incomplete(a, title, apperr.From(err).Message)
		return
	}
	// Short changes are the title together; a longer list is titled by its
	// first change and the rest follow (amounts kept below).
	a.Title, a.Details = mixTitle(po.Changes), mixDetails(po.Changes)
	if len(a.Title) > 90 && len(po.Changes) > 1 {
		a.Title, a.Details = mixTitle(po.Changes[:1]), mixDetails(po.Changes[:1])
		for _, w := range po.Changes[1:] {
			a.Details = append(a.Details, upperFirst(w))
		}
	}
	if person != "" {
		a.Details = append(a.Details, "Only while "+person+" speaks; nobody else changes")
	}
	if po.Result != nil {
		checkDetails(a, po.Result.Check)
	}
	a.Details = append(a.Details, "Puts it in the cut as a new version")
	in.Apply = true
	setInput(a, "sound.levels", in)
}

// enhanceWho names who a clearer voice is for, mid-sentence: "Maya's
// voice", "the narration", "everyone's voice", "the sound of “Garage interview”".
func enhanceWho(target string) string {
	switch {
	case target == "the narration":
		return "the narration"
	case target == "everyone on camera":
		return "everyone's voice on camera"
	case strings.HasPrefix(target, "“"):
		return "the sound of " + target
	}
	return possessiveName(target) + " voice"
}

func (m *Manager) describeEnhance(ctx context.Context, call *cliCall, r *models.Revision, a *AssistantAction) {
	const title = "Make a voice clearer"
	if hasBool(call, "estimate") {
		incomplete(a, "Price a clearer voice", "The price shows on the card when Claude asks for the clearer voice itself.")
		return
	}
	who := strings.TrimSpace(strings.Join(call.Args, " "))
	if who == "" {
		incomplete(a, title, "Claude did not say whose voice: a person, narration, everyone or a recording.")
		return
	}
	mode := strings.ToLower(strings.TrimSpace(call.Flags["mode"]))
	switch mode {
	case "", "clean", "studio", "cloud":
	default:
		incomplete(a, title, "A voice is made clearer with clean, studio or the cloud.")
		return
	}
	in := sound.EnhanceIn{RevisionID: r.ID, Target: clip(who, 200), Mode: mode, Remove: hasBool(call, "remove")}
	if s, ok := numFlag(call, "strength"); !ok {
		incomplete(a, title, "Claude gave a strength that isn't a number.")
		return
	} else if s != nil {
		v := *s
		if v > 1 && v <= 100 { // "80" means 80%
			v /= 100
		}
		if v < 0 || v > 1 {
			incomplete(a, title, "The strength is how much of the clearer voice is heard, from 0 to 100%.")
			return
		}
		in.Strength = &v
	}
	switch {
	case hasBool(call, "off") && hasBool(call, "on"):
		incomplete(a, title, "Claude asked for the clearer voice both off and on.")
		return
	case hasBool(call, "off"):
		in.Off = flag(true)
	case hasBool(call, "on"):
		in.Off = flag(false)
	}
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	est, err := sound.PreviewEnhance(lctx, m.App, in)
	if err != nil {
		incomplete(a, title, apperr.From(err).Message)
		return
	}
	whose := enhanceWho(est.Target)
	e := est.Estimate
	switch {
	case in.Remove:
		a.Title = upperFirst("stop making " + whose + " clearer")
		a.Details = []string{"The recording plays as it was recorded; a level change stays"}
	case in.Off != nil && *in.Off:
		a.Title = upperFirst("play " + whose + " as recorded")
		a.Details = []string{"The clearer version is kept, so switching back costs nothing"}
	case in.Off != nil:
		a.Title = upperFirst("play the clearer version of " + whose + " again")
	default:
		m.clearerVoiceCard(ctx, a, whose, est, mode)
		if a.Estimate != nil {
			a.SpendsIn = r.ProjectID // the version's project pays (D241)
		}
		if a.State != ActionPending {
			return
		}
		if in.Strength != nil && *in.Strength < 1 {
			a.Details = append(a.Details, fmt.Sprintf("You hear %d%% of the clearer voice, the rest is the room", int(math.Round(*in.Strength*100))))
		}
	}
	if a.State != ActionPending {
		return
	}
	if !e.Allowed && len(a.SetUp) == 0 {
		why := e.Problem
		if why == "" {
			why = "MetaRush can't make it here right now."
		}
		unavailable(a, a.Title, why)
		return
	}
	a.Details = append(a.Details, "Puts it in the cut as a new version")
	in.Apply = true
	setInput(a, "sound.enhance", in)
}

// clearerVoiceCard words a card that makes a voice clearer, by how: the cloud
// priced first, studio with its one-time setup when it isn't set up, clean on
// this computer.
func (m *Manager) clearerVoiceCard(ctx context.Context, a *AssistantAction, whose string, est *sound.EnhanceOut, asked string) {
	e := est.Estimate
	mode := est.Mode
	if mode == "" {
		mode = asked
	}
	work := upperFirst(sound.WorkWords(e)) // "20 seconds of sound to process, about 9 seconds"
	switch mode {
	case "cloud":
		switch {
		case e.Credits > 0 && e.Cost != nil:
			// The estimate in dollars (D208); the credits are in its parts.
			a.Title = upperFirst(fmt.Sprintf("make %s clearer with ElevenLabs (%s)", whose, e.Cost.About))
			cost := *e.Cost
			a.Estimate = &cost
		case e.Credits > 0:
			a.Title = upperFirst(fmt.Sprintf("make %s clearer with ElevenLabs (about %s credits)", whose, thousands(e.Credits)))
		default:
			a.Title = upperFirst("make " + whose + " clearer with ElevenLabs (made before: free)")
			if e.Cost != nil {
				cost := *e.Cost // every stretch made before: "nothing (made before)"
				a.Estimate = &cost
			}
		}
		if work != "" {
			a.Details = append(a.Details, work)
		}
		if b := e.Budget; b != nil && b.LimitCredits != nil {
			a.Details = append(a.Details, fmt.Sprintf("This project has %s of %s credits left", thousands(math.Max(0, *b.LimitCredits-b.UsedCredits)), thousands(*b.LimitCredits)))
		}
		if e.AccountRemaining != nil {
			a.Details = append(a.Details, "Your ElevenLabs plan has "+providers.Commas(*e.AccountRemaining)+" credits left this period")
		}
		a.Details = append(a.Details, "The voice is sent to ElevenLabs; the recording stays as it is and a clearer version plays instead")
		if !e.Allowed {
			if _, err := providers.ElevenClientFor(ctx, m.App); apperr.Is(err, apperr.NotConfigured) {
				unavailable(a, a.Title, "ElevenLabs isn't set up on this computer yet. Add your key in Settings → Integrations, then ask again.")
			}
		}
	case "studio":
		a.Title = upperFirst("make " + whose + " clearer in studio quality")
		if !sound.StudioReady(m.App) {
			if sound.SetupRunning(ctx, m.App) {
				unavailable(a, a.Title, "Studio quality is being set up on this computer right now. Ask again when Activity shows it's done.")
				return
			}
			// Shown first: it is what the person's confirmation says yes to.
			a.Details = append(a.Details, "First sets up studio quality on this computer: a one-time download of about 1.8 GB (its own tools and a voice model)")
			a.HumanOnly, a.SetUp = true, []string{sound.ComponentStudio}
		}
		if work != "" {
			a.Details = append(a.Details, work)
		}
		a.Details = append(a.Details, "On this computer and slower: it rebuilds the voice, and every word is checked so nothing anyone said changes")
	default:
		a.Title = upperFirst("make " + whose + " clearer (on this computer)")
		if work != "" {
			a.Details = append(a.Details, work)
		}
		if e.SetsUp == sound.ComponentClean {
			a.Details = append(a.Details, "First the voice cleaner is set up on this computer (one time, about 30 MB)")
		}
		a.Details = append(a.Details, "Free, and nothing leaves this computer: less hiss, hum and room echo; the recording stays as it is")
	}
	a.Title = clip(a.Title, 160)
}

// audioFile looks like a file on this computer rather than a clip's title.
var audioFile = regexp.MustCompile(`(?i)\.(m4a|mp3|wav|aac|flac|aif|aiff|ogg|opus)$`)

func (m *Manager) describeMusicAdd(ctx context.Context, call *cliCall, r *models.Revision, a *AssistantAction) {
	const title = "Put music under the cut"
	ref := strings.TrimSpace(strings.Join(call.Args, " "))
	if ref == "" {
		incomplete(a, title, "Claude did not name the music.")
		return
	}
	const addFirst = "Add the music from the dashboard first: drop the file on the Sound tab. Then ask Claude to put it under the cut."
	if strings.ContainsAny(ref, `/\`) || strings.HasPrefix(ref, "~") || filepath.IsAbs(ref) {
		unavailable(a, title, addFirst)
		return
	}
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	as, err := sound.FindMusic(lctx, m.App, r.ProjectID, ref)
	cancel()
	if err != nil {
		if audioFile.MatchString(ref) || apperr.Is(err, apperr.NotFound) {
			unavailable(a, title, addFirst)
		} else {
			incomplete(a, title, apperr.From(err).Message)
		}
		return
	}
	if !as.Media.HasAudio {
		incomplete(a, title, quoted(clipName(as))+" has no sound.")
		return
	}
	in := sound.MusicAddIn{RevisionID: r.ID, Asset: as.ID, Fit: strings.ToLower(strings.TrimSpace(call.Flags["fit"])), Loop: hasBool(call, "loop"), Apply: true}
	var oks [3]bool
	in.From, oks[0] = clockFlag(call, "from")
	in.To, oks[1] = clockFlag(call, "to")
	in.At, oks[2] = clockFlag(call, "at")
	gain, okG := numFlag(call, "level")
	depth, okD := numFlag(call, "duck-depth")
	duck, okDuck := onOff(call, "duck")
	if !oks[0] || !oks[1] || !oks[2] || !okG || !okD || !okDuck {
		incomplete(a, title, "Claude gave a time or a level that isn't one.")
		return
	}
	in.GainDB, in.Duck = gain, duck
	if depth != nil {
		d := -math.Abs(*depth)
		in.DuckDB = &d
	}
	if id := strings.TrimSpace(call.Flags["id"]); id != "" {
		if !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`).MatchString(id) {
			incomplete(a, title, "The track's short name "+quoted(id)+" isn't one MetaRush can use (lowercase letters, digits, - and _).")
			return
		}
		in.ID = id
	}
	switch in.Fit {
	case "", "auto", "manual":
	default:
		incomplete(a, title, "Music either fits the video (auto) or is placed by hand (manual).")
		return
	}
	byHand := in.Fit == "manual" || (in.Fit == "" && (in.From != nil || in.To != nil || in.At != nil))
	if byHand && in.Loop {
		incomplete(a, title, "Music loops only when it fits the video.")
		return
	}
	a.Title = clip("Put "+quoted(clipName(as))+" under the video", 160)
	if byHand {
		from, to, at := 0.0, as.Media.Duration, 0.0
		if in.From != nil {
			from = *in.From
		}
		if in.To != nil {
			to = math.Min(*in.To, as.Media.Duration)
		}
		if in.At != nil {
			at = *in.At
		}
		a.Details = []string{"From " + editspec.Clock(from) + " to " + editspec.Clock(to) + " of the music, starting at " + editspec.Clock(at) + " in the video"}
	} else {
		a.Details = []string{"Starts with the video and ends with it on a beat, with a smooth fade"}
		if in.Loop {
			a.Details = append(a.Details, "Repeats until the video ends")
		}
	}
	switch {
	case in.GainDB != nil:
		a.Details = append(a.Details, "Its level: "+signedDBWords(*in.GainDB)+" (quiet under voices is about -12 dB)")
	default:
		a.Details = append(a.Details, "Quietly under the voices")
	}
	switch {
	case in.Duck != nil && !*in.Duck:
		a.Details = append(a.Details, "Stays at its level while people talk")
	case in.DuckDB != nil:
		a.Details = append(a.Details, fmt.Sprintf("Dips by %.0f dB while someone talks", math.Abs(*in.DuckDB)))
	default:
		a.Details = append(a.Details, "Gets quieter while someone talks")
	}
	if as.Rights == nil || as.Rights.License == "" {
		a.Details = append(a.Details, "To-dos asks where it comes from once it's in the cut")
	}
	a.Details = append(a.Details, "Puts it in the cut as a new version")
	setInput(a, "sound.music.add", in)
}

// flag points at a yes or no.
func flag(v bool) *bool { return &v }

// signedDBWords is a level change: "+3 dB", "-16 dB".
func signedDBWords(g float64) string {
	s := strconv.FormatFloat(math.Abs(math.Round(g*10)/10), 'f', -1, 64) + " dB"
	switch {
	case g > 0:
		return "+" + s
	case g < 0:
		return "-" + s
	}
	return s
}

func (m *Manager) describeMusicSet(ctx context.Context, call *cliCall, r *models.Revision, a *AssistantAction) {
	const title = "Change the music"
	track := strings.TrimSpace(strings.Join(call.Args, " "))
	if track == "" {
		incomplete(a, title, "Claude did not name the music track.")
		return
	}
	in := sound.MusicSetIn{RevisionID: r.ID, Track: clip(track, 200), Fit: strings.ToLower(strings.TrimSpace(call.Flags["fit"])), Remove: hasBool(call, "remove"), Apply: true}
	var oks [3]bool
	in.From, oks[0] = clockFlag(call, "from")
	in.To, oks[1] = clockFlag(call, "to")
	in.At, oks[2] = clockFlag(call, "at")
	gain, okG := numFlag(call, "level")
	depth, okD := numFlag(call, "duck-depth")
	if !oks[0] || !oks[1] || !oks[2] || !okG || !okD {
		incomplete(a, title, "Claude gave a time or a level that isn't one.")
		return
	}
	in.GainDB = gain
	if depth != nil {
		d := -math.Abs(*depth)
		in.DuckDB = &d
	}
	if v, has := call.Flags["duck"]; has {
		switch d := strings.ToLower(strings.TrimSpace(v)); d {
		case "on", "off", "auto":
			in.Duck = d
		default:
			incomplete(a, title, "Music gets quieter under voices on, off or as the mix decides (auto).")
			return
		}
	}
	switch {
	case hasBool(call, "loop") && hasBool(call, "no-loop"):
		incomplete(a, title, "Claude asked to loop the music and not to.")
		return
	case hasBool(call, "loop"):
		in.Loop = flag(true)
	case hasBool(call, "no-loop"):
		in.Loop = flag(false)
	}
	switch in.Fit {
	case "", "auto", "manual":
	default:
		incomplete(a, title, "Music either fits the video (auto) or is placed by hand (manual).")
		return
	}
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	po, err := sound.PreviewMusicSet(lctx, m.App, in)
	if err != nil {
		incomplete(a, title, apperr.From(err).Message)
		return
	}
	name := in.Track
	for _, t := range r.Spec.Audio {
		if t.ID == po.Track.ID {
			if n := m.lookupName(ctx, t.AssetID); n != "" {
				name = n
			}
		}
	}
	if in.Remove {
		a.Title = "Take " + quoted(name) + " out of the cut"
	} else {
		what, where, _ := strings.Cut(po.Summary, ": it ")
		a.Title = clip("Music "+quoted(name)+": "+lowerFirst(what), 160)
		if where != "" {
			a.Details = append(a.Details, upperFirst("it "+strings.TrimSuffix(firstLine(where), ".")))
		}
	}
	if po.Result != nil {
		checkDetails(a, po.Result.Check)
	}
	a.Details = append(a.Details, "Puts it in the cut as a new version")
	setInput(a, "sound.music.set", in)
}

// describeSoundSetup is setting clearer voices up on this computer: people
// only, so the card is the person's decision (a styled confirmation).
func (m *Manager) describeSoundSetup(ctx context.Context, call *cliCall, a *AssistantAction) {
	const title = "Set up clearer voices on this computer"
	comps := []string{}
	for _, x := range call.Args {
		x = strings.ToLower(strings.TrimSpace(x))
		if x != sound.ComponentClean && x != sound.ComponentStudio {
			incomplete(a, title, "There are two things to set up: clean and studio.")
			return
		}
		comps = append(comps, x)
	}
	if len(comps) == 0 {
		comps = []string{sound.ComponentClean}
	}
	studio := false
	for _, c := range comps {
		studio = studio || c == sound.ComponentStudio
	}
	if studio && sound.StudioReady(m.App) {
		incomplete(a, title, "Studio quality is set up on this computer already.")
		return
	}
	if sound.SetupRunning(ctx, m.App) {
		unavailable(a, title, "Clearer voices are being set up on this computer right now. Activity shows how far along it is.")
		return
	}
	a.HumanOnly = true
	if studio {
		a.Title = "Set up studio quality on this computer (one time, about 1.8 GB)"
		a.Details = []string{"Downloads its own tools and a voice model, about 1.8 GB. Voices are then made clearer on this computer; nothing is sent anywhere."}
	} else {
		a.Title = "Set up the voice cleaner on this computer (about 30 MB)"
		a.Details = []string{"A small download, checked before it is used. Voices are then made clearer on this computer; nothing is sent anywhere."}
	}
	a.Details = append(a.Details, "Only people set this up: Claude suggested it, the decision is yours")
	setInput(a, "sound.setup", sound.SetupIn{Components: comps})
}

// setUpFor sets up what a card needs on this computer before its change runs
// (studio quality: a people-only, one-time download), unless it is set up by
// now. The person confirmed the card, so it runs as them (caller).
func setUpFor(ctx context.Context, a *app.App, caller ops.Caller, comps []string) error {
	var need []string
	for _, c := range comps {
		if c == sound.ComponentStudio && sound.StudioReady(a) {
			continue
		}
		need = append(need, c)
	}
	if len(need) == 0 {
		return nil
	}
	if sound.SetupRunning(ctx, a) {
		return apperr.New(apperr.Conflict, "Clearer voices are being set up on this computer right now. Try again when Activity shows it's done.")
	}
	_, err := ops.CallTyped[sound.SetupOut](ctx, a, "sound.setup", sound.SetupIn{Components: need}, caller)
	return err
}

// soundResult is what a sound card did, in plain words, and where to see it.
func soundResult(res any) (result, link, forClaude string, ok bool) {
	switch o := res.(type) {
	case *sound.LevelsOut:
		result, link, forClaude = appliedResult(o.Summary, o.Applied)
	case *sound.EnhanceOut:
		result, link, forClaude = appliedResult(o.Summary, o.Applied)
	case *sound.MusicOut:
		result, link, forClaude = appliedResult(o.Summary, o.Applied)
	case *sound.SetupOut:
		result = plainSummary(o.Summary)
	default:
		return "", "", "", false
	}
	return result, link, forClaude, true
}

// appliedProblem is why a card's change, which ran, did not land in the cut
// (its check failed when it was saved): the card did not do what it said.
func appliedProblem(res any) string {
	var ap *voiceover.Applied
	switch o := res.(type) {
	case *sound.LevelsOut:
		ap = o.Applied
	case *sound.EnhanceOut:
		ap = o.Applied
	case *sound.MusicOut:
		ap = o.Applied
	case *conversation.CallPlanOut:
		ap = o.Applied
	case *conversation.CallDecideOut:
		ap = o.Applied
	}
	if ap == nil {
		return ""
	}
	return ap.Problem
}
