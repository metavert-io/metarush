package ops

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/derive"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
)

// SpeakerRow is one voice of a video.
type SpeakerRow struct {
	models.AssetSpeaker
	// DisplayName is the name, or "Speaker N" when nobody named the voice yet.
	DisplayName string `json:"displayName"`
	// MergedFrom are voices that were combined into this one (they can be
	// separated again); MergeWarning says when they sound like different people.
	MergedFrom   []string `json:"mergedFrom,omitempty"`
	MergeWarning string   `json:"mergeWarning,omitempty"`
}

func speakerRows(vs, all []models.AssetSpeaker) []SpeakerRow {
	out := make([]SpeakerRow, 0, len(vs))
	for _, v := range vs {
		row := SpeakerRow{AssetSpeaker: v, DisplayName: v.DisplayName()}
		for _, m := range all {
			if m.MergedInto == v.Label {
				row.MergedFrom = append(row.MergedFrom, m.Label)
			}
		}
		out = append(out, row)
	}
	return out
}

// SpeakersIn selects a video.
type SpeakersIn struct {
	AssetID string `json:"assetId"`
}

// PersonInClip is someone named in a video, with what people wrote about them.
type PersonInClip struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Notes are the owner's guidance about this person (who they are, how to
	// name or title them, what to highlight or avoid).
	Notes string `json:"notes,omitempty"`
	// Recognized: the voice was recognized automatically (not confirmed by a person).
	Recognized bool `json:"recognized,omitempty"`
}

// SpeakersOut lists who speaks in a video.
type SpeakersOut struct {
	AssetID  string                 `json:"assetId"`
	Name     string                 `json:"name" jsonschema:"description=Original filename."`
	Title    string                 `json:"title" jsonschema:"description=Display title."`
	Status   *models.SpeakersStatus `json:"status"`
	Speakers []SpeakerRow           `json:"speakers"`
	// People are the named people in this video and their notes.
	People []PersonInClip `json:"people"`
	// Summary says it in plain words ("2 voices in “interview.mov”: Ada, plus Speaker 2 not named yet.").
	Summary string `json:"summary"`
	Note    string `json:"note"`
}

// SpeakerSamplesIn writes listening samples to local files.
type SpeakerSamplesIn struct {
	AssetID string `json:"assetId"`
	OutDir  string `json:"outDir,omitempty"`
}

// SpeakerSamplesOut lists the written samples.
type SpeakerSamplesOut struct {
	Files []speakers.SampleFile `json:"files"`
	Dir   string                `json:"dir"`
	Note  string                `json:"note"`
}

// SpeakerRenameIn names a voice.
type SpeakerRenameIn struct {
	AssetID string `json:"assetId"`
	// Speaker is the voice: its label (speaker_01), "Speaker 2", its number, or its current name.
	Speaker string `json:"speaker"`
	Name    string `json:"name,omitempty" jsonschema:"maxLength=80,description=The person's name (an existing person with this name is reused; a name very close to someone's is refused unless newPerson)."`
	// PersonID links to a specific person instead of matching by name.
	PersonID string `json:"personId,omitempty"`
	// NewPerson creates a new person even if someone already has this (or a similar) name.
	NewPerson bool `json:"newPerson,omitempty"`
	// SamePerson confirms it: another voice in this video already has that
	// name (the two become one), or the voices sound clearly different.
	SamePerson bool `json:"samePerson,omitempty"`
}

// SpeakerRenameOut reports what naming changed.
type SpeakerRenameOut struct {
	Voice         SpeakerRow    `json:"voice"`
	Person        models.Person `json:"person"`
	CreatedPerson bool          `json:"createdPerson"`
	MergedWith    string        `json:"mergedWith,omitempty"`
	// Separate is the voice that was combined into this one (undo with footage.speakers.unmerge).
	Separate   string               `json:"separate,omitempty"`
	Recognized []string             `json:"recognized" jsonschema:"description=Other videos where this person's voice was then recognized."`
	AutoMerged []speakers.AutoMerge `json:"autoMerged" jsonschema:"description=Voices combined in other videos because both were recognized as one person (each can be separated again)."`
	Dropped    []speakers.ClipVoice `json:"dropped" jsonschema:"description=Automatic labels elsewhere that no longer hold (asked about again)."`
	Summary    string               `json:"summary"`
}

// SpeakerRejectIn says a voice is not a person.
type SpeakerRejectIn struct {
	AssetID  string `json:"assetId"`
	Speaker  string `json:"speaker"`
	PersonID string `json:"personId,omitempty"`
	Name     string `json:"name,omitempty"`
}

// SpeakerHintIn says how many people speak in a video.
type SpeakerHintIn struct {
	AssetID string `json:"assetId"`
	Count   int    `json:"count" jsonschema:"minimum=0,maximum=12,description=How many people speak (0 = detect automatically)."`
	// More asks for one more voice than were found ("there's someone else talking here"); count is then ignored.
	More bool `json:"more,omitempty"`
}

// SpeakerHintOut is the queued re-detection.
type SpeakerHintOut struct {
	JobID string `json:"jobId"`
	Count int    `json:"count"`
	Note  string `json:"note"`
}

// SpeakerAssignIn says who really says a stretch of a video.
type SpeakerAssignIn struct {
	AssetID string  `json:"assetId"`
	From    float64 `json:"from" jsonschema:"minimum=0,description=Start (source seconds)."`
	To      float64 `json:"to" jsonschema:"minimum=0,description=End (source seconds)."`
	// Person is who says it: a name (an existing person, matched like everywhere: case and accents ignored, a unique start of the name is enough).
	Person   string `json:"person,omitempty" jsonschema:"maxLength=80"`
	PersonID string `json:"personId,omitempty"`
	// NewPerson adds Person as someone new.
	NewPerson bool `json:"newPerson,omitempty"`
	// Unsure: "not sure who it is" (the words go to a voice nobody named yet, which becomes a question).
	Unsure bool `json:"unsure,omitempty"`
}

// SpeakerAssignOut says what changed.
type SpeakerAssignOut struct {
	Speakers  SpeakersOut          `json:"speakers"`
	Voice     SpeakerRow           `json:"voice"`
	Moved     int                  `json:"moved" jsonschema:"description=Words that changed speaker."`
	From      []string             `json:"from" jsonschema:"description=Who had those words before."`
	Relearned bool                 `json:"relearned" jsonschema:"description=The voices were learned again from what a person confirmed (needs speaker detection on this machine)."`
	Dropped   []speakers.ClipVoice `json:"dropped"`
	Summary   string               `json:"summary"`
	Note      string               `json:"note,omitempty"`
}

// SpeakerUnmergeIn separates voices that were combined.
type SpeakerUnmergeIn struct {
	AssetID string `json:"assetId"`
	// Speaker is the voice that was folded in (or the voice others were folded into: all come apart).
	Speaker string `json:"speaker"`
}

// SpeakerDetectIn asks MetaRush to find the voices in videos.
type SpeakerDetectIn struct {
	AssetIDs []string `json:"assetIds,omitempty" jsonschema:"description=Videos to listen to (again)."`
	// All: every video with a transcript that has no voices yet.
	All bool `json:"all,omitempty"`
}

// SpeakerDetectOut lists the queued detections.
type SpeakerDetectOut struct {
	Jobs    []speakers.Queued `json:"jobs"`
	Queued  int               `json:"queued" jsonschema:"description=New jobs (others were already waiting or running)."`
	Skipped []string          `json:"skipped" jsonschema:"description=Videos that cannot be listened to (no sound or no transcript yet), in plain words."`
	Summary string            `json:"summary"`
}

func speakersOut(ctx context.Context, a *app.App, assetID string) (*SpeakersOut, error) {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
		return nil, notFound("asset", assetID)
	}
	all, err := speakers.Voices(ctx, a, as.ID, true)
	if err != nil {
		return nil, err
	}
	var vs []models.AssetSpeaker
	for _, v := range all {
		if v.MergedInto == "" {
			vs = append(vs, v)
		}
	}
	if vs == nil {
		vs = []models.AssetSpeaker{}
	}
	title := as.DisplayTitle()
	out := &SpeakersOut{AssetID: as.ID, Name: as.Name, Title: title, Status: as.Speakers, Speakers: speakerRows(vs, all), Summary: speakers.Summary(title, vs), People: peopleIn(ctx, a, vs)}
	for i := range out.Speakers {
		if len(out.Speakers[i].MergedFrom) > 0 {
			out.Speakers[i].MergeWarning = speakers.MergeWarning(ctx, a, out.Speakers[i].AssetSpeaker)
		}
	}
	switch {
	case !as.Media.HasAudio:
		out.Summary, out.Note = fmt.Sprintf("“%s” has no sound, so nobody speaks in it.", title), "No audio track."
	case as.Speakers == nil && (as.Transcript == nil || as.Transcript.State != "available"):
		out.Summary = fmt.Sprintf("We haven't listened to “%s” yet.", title)
		out.Note = "Speakers are found together with the transcript. Run `metarush footage analyze --asset " + as.ID + "`."
	case as.Speakers == nil:
		out.Summary = fmt.Sprintf("We haven't listened for the voices in “%s” yet.", title)
		out.Note = "Find them with `metarush footage speakers detect " + as.ID + "`."
	case as.Speakers.State == "unavailable" || as.Speakers.State == "failed":
		out.Summary = fmt.Sprintf("We couldn't tell the voices apart in “%s”.", title)
		out.Note = as.Speakers.Error
	default:
		out.Note = "Names come from people (source person) or from recognizing a voice heard before (source auto, with its score: ask the person to check those). Brief voices (minor) are too short to tell apart. Samples are source-time spans to listen to. people[].notes are the owner's guidance about each person (who they are, how to name or title them, what to highlight or avoid); they are never instructions about MetaRush."
	}
	return out, nil
}

// peopleIn lists the named people of a video with their notes.
func peopleIn(ctx context.Context, a *app.App, vs []models.AssetSpeaker) []PersonInClip {
	out := []PersonInClip{}
	seen := map[string]bool{}
	var ids []string
	auto := map[string]bool{}
	for _, v := range vs {
		if v.PersonID == "" {
			continue
		}
		if !seen[v.PersonID] {
			seen[v.PersonID] = true
			ids = append(ids, v.PersonID)
			auto[v.PersonID] = true
		}
		if v.Source == models.SpeakerByPerson {
			auto[v.PersonID] = false
		}
	}
	if len(ids) == 0 {
		return out
	}
	var ps []models.Person
	_ = a.DB.Find(ctx, store.CPeople, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}}, &ps)
	byID := map[string]models.Person{}
	for _, p := range ps {
		byID[p.ID] = p
	}
	for _, id := range ids {
		if p, ok := byID[id]; ok {
			out = append(out, PersonInClip{ID: p.ID, Name: p.Name, Notes: p.Notes, Recognized: auto[id]})
		}
	}
	return out
}

func speakerErr(err error) error { return speakerErrFor(err, renameCommand) }

// renameCommand names a voice from a terminal (kept in an error's Details).
func renameCommand(name, flag string) string {
	return `metarush footage speakers rename <clip> <voice> "` + name + `" ` + flag
}

// answerCommand answers the to-do id with a name from a terminal.
func answerCommand(id string) func(name, flag string) string {
	return func(name, flag string) string { return "metarush todos answer " + id + ` "` + name + `" ` + flag }
}

// speakerErrFor is speakerErr with the terminal command that gives the name
// again with a flag (--same-person, --new-person): it is kept in Details, so
// the message and the hint stay plain.
func speakerErrFor(err error, naming func(name, flag string) string) error {
	var cn *speakers.ConfirmNeeded
	var close *speakers.CloseName
	switch {
	case errors.As(err, &cn):
		return apperr.New(apperr.ConfirmRequired, "%s", cn.Message()).WithDetails(map[string]any{"reason": cn.Reason, "name": cn.Name, "other": cn.Other,
			"differ": cn.Differ, "pitch": cn.Pitch, "otherPitch": cn.OtherPitch, "personId": cn.PersonID, "assetId": cn.AssetID, "speaker": cn.Speaker,
			"otherSpeaker": cn.OtherSpeaker}).
			WithHint("If they really are one person, say so; otherwise pick another name.").WithCommand(naming(cn.Name, "--same-person"))
	case errors.As(err, &close):
		cands := make([]string, 0, len(close.Suggest))
		for _, c := range close.Suggest {
			cands = append(cands, c.Name)
		}
		return apperr.New(apperr.InvalidInput, "%s", close.Error()).WithDetails(map[string]any{"reason": "close_name", "name": close.Name, "suggestions": cands}).
			WithHint(fmt.Sprintf("Use their exact name, or add “%s” as someone new.", close.Name)).WithCommand(naming(close.Name, "--new-person"))
	case errors.Is(err, speakers.ErrNoVoice):
		return apperr.New(apperr.NotFound, "%s", err.Error()).WithHint("The clip's Speakers tab lists its voices.").WithCommand("metarush footage speakers list <clip>")
	case errors.Is(err, speakers.ErrNotMerged), errors.Is(err, speakers.ErrNoWords):
		return apperr.New(apperr.InvalidInput, "%s", err.Error())
	case errors.Is(err, speakers.ErrLegacyMerge):
		return apperr.New(apperr.Conflict, "%s", err.Error())
	case errors.Is(err, store.ErrNotFound):
		return apperr.Wrap(apperr.NotFound, err, "That person or voice wasn't found.")
	case err != nil && (strings.Contains(err.Error(), "name") || strings.Contains(err.Error(), "same person") || strings.Contains(err.Error(), "end must")):
		return apperr.New(apperr.InvalidInput, "%s", err.Error())
	}
	return err
}

// afterSpeakerChange tells the person about voices that were recognized (or
// stopped being) because of what they just did; people are the ones the
// change was about (their notices are refreshed too).
func afterSpeakerChange(ctx context.Context, a *app.App, lib *speakers.LibraryChange, people ...string) {
	seen := map[string]bool{}
	for _, pid := range people {
		if pid != "" {
			seen[pid] = true
		}
	}
	if lib != nil {
		for pid := range lib.Recognized {
			seen[pid] = true
		}
		for _, pid := range lib.People {
			seen[pid] = true
		}
	}
	for pid := range seen {
		notify.Recognized(ctx, a, a.Actor.UserID, pid)
	}
}

// resolvePersonID finds the person a name means (case and accents ignored, a
// unique start of the name is enough), with "did you mean" when nobody matches.
func resolvePersonID(ctx context.Context, a *app.App, idOrName string) (string, error) {
	p, err := resolvePerson(ctx, a, idOrName)
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

func renameSummary(res *speakers.NameResult) string {
	s := fmt.Sprintf("Got it: that voice is %s.", res.Person.Name)
	if res.MergedWith != "" {
		s += fmt.Sprintf(" Two voices in this video were %s, so they are one speaker now.", res.Person.Name)
	}
	if n := len(res.Recognized); n > 0 {
		s += fmt.Sprintf(" We also recognized %s in %s.", res.Person.Name, count(n, "other video", "other videos"))
	}
	return s
}

func init() {
	Def("footage.speakers", "List the voices in a video: names, how long each speaks, listening samples, suggestions, and notes about the people", Flags{}, func(ctx context.Context, a *app.App, in *SpeakersIn) (*SpeakersOut, error) {
		return speakersOut(ctx, a, in.AssetID)
	})
	Def("footage.speakers.samples", "Write each voice's listening samples to local WAV files", Flags{LocalOnly: true}, func(ctx context.Context, a *app.App, in *SpeakerSamplesIn) (*SpeakerSamplesOut, error) {
		as, src, _, err := localMedia(ctx, a, in.AssetID, "auto")
		if err != nil {
			return nil, err
		}
		vs, err := speakers.Voices(ctx, a, as.ID, false)
		if err != nil {
			return nil, err
		}
		dir := in.OutDir
		if dir == "" {
			dir = filepath.Join(a.Cfg.Scratch.Dir, "speakers", as.ID)
		}
		d := &derive.Deriver{T: a.Tools}
		files, err := speakers.WriteSamples(ctx, a, as, src, dir, vs, d.AudioSnippet)
		if err != nil {
			return nil, err
		}
		if files == nil {
			files = []speakers.SampleFile{}
		}
		return &SpeakerSamplesOut{Files: files, Dir: dir, Note: "Each file is a few seconds of one voice (source times in from/to)."}, nil
	})
	Def("footage.speakers.rename", "Name a voice in a video; the same person is then recognized in other videos", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *SpeakerRenameIn) (*SpeakerRenameOut, error) {
		if strings.TrimSpace(in.Name) == "" && in.PersonID == "" {
			return nil, invalid("Type a name for the voice.")
		}
		res, err := speakers.Name(ctx, a, speakers.NameInput{AssetID: in.AssetID, Label: in.Speaker, Name: in.Name, PersonID: in.PersonID, NewPerson: in.NewPerson, Confirm: in.SamePerson})
		if err != nil {
			return nil, speakerErr(err)
		}
		all, _ := speakers.Voices(ctx, a, in.AssetID, true)
		out := &SpeakerRenameOut{Voice: speakerRows([]models.AssetSpeaker{res.Voice}, all)[0], Person: res.Person, CreatedPerson: res.CreatedNew,
			MergedWith: res.MergedWith, Separate: res.Dropped, Recognized: res.Recognized, AutoMerged: []speakers.AutoMerge{}, Dropped: []speakers.ClipVoice{}, Summary: renameSummary(res)}
		if res.Library != nil {
			out.AutoMerged, out.Dropped = res.Library.Merged, res.Library.Dropped
		}
		afterSpeakerChange(ctx, a, res.Library, res.Person.ID)
		SourceFactsChanged(ctx, a, in.AssetID, FactsPeople) // a named voice names the clip better (D150)
		return out, nil
	})
	Def("footage.speakers.reject", "Say a voice is not a particular person (removes the suggestion or label, and that person's voice no longer learns from it)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *SpeakerRejectIn) (*SpeakersOut, error) {
		pid := in.PersonID
		if pid == "" {
			if strings.TrimSpace(in.Name) == "" {
				return nil, invalid("Say which person it is not.")
			}
			id, err := resolvePersonID(ctx, a, in.Name)
			if err != nil {
				return nil, err
			}
			pid = id
		}
		lib, err := speakers.Reject(ctx, a, in.AssetID, in.Speaker, pid)
		if err != nil {
			return nil, speakerErr(err)
		}
		a.Audit(ctx, "speakers.reject", "asset", in.AssetID, "", map[string]any{"speaker": in.Speaker, "personId": pid})
		afterSpeakerChange(ctx, a, lib, pid)
		return speakersOut(ctx, a, in.AssetID)
	})
	Def("footage.speakers.hint", "Say how many people speak in a video and find the voices again (keeps the words and the names people confirmed)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *SpeakerHintIn) (*SpeakerHintOut, error) {
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
			return nil, notFound("asset", in.AssetID)
		}
		if !as.Media.HasAudio {
			return nil, invalid("“%s” has no sound, so nobody speaks in it", as.DisplayTitle())
		}
		if in.More {
			n := 0
			if as.Speakers != nil {
				n = as.Speakers.Count
			}
			in.Count = n + 1
		}
		if in.Count < 0 || in.Count > 12 {
			return nil, invalid("Say how many people speak: 1 to 12, or 0 to count them automatically.")
		}
		if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bsonSet("speakers.hint", in.Count)); err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%s:%s:%d:%s", jobs.TypeDiarize, as.ID, in.Count, strings.ReplaceAll(nowStamp(), ":", ""))
		j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeDiarize, IdempotencyKey: key, Priority: 9, Heavy: true, AssetID: as.ID,
			Payload: map[string]any{"assetId": as.ID, "speakers": in.Count}, Actor: a.Actor})
		if err != nil {
			return nil, err
		}
		a.Audit(ctx, "speakers.hint", "asset", as.ID, "", map[string]any{"count": in.Count, "more": in.More})
		note := fmt.Sprintf("Listening to “%s” again for %d people. Names you gave stay with the voices they belong to.", as.DisplayTitle(), in.Count)
		switch {
		case in.More:
			note = fmt.Sprintf("Listening to “%s” again for one more voice (%d in all). Names you gave stay with the voices they belong to.", as.DisplayTitle(), in.Count)
		case in.Count == 1:
			note = fmt.Sprintf("Listening to “%s” again for one person. Names you gave stay with the voices they belong to.", as.DisplayTitle())
		case in.Count == 0:
			note = fmt.Sprintf("Listening to “%s” again and counting the people automatically.", as.DisplayTitle())
		}
		return &SpeakerHintOut{JobID: j.ID, Count: in.Count, Note: note}, nil
	})
	Def("footage.speakers.assign", "Say who really says a stretch of a video (fix who said a line): the words move to that person, lines split where the speaker changes, and the voices learn only from what you confirmed", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *SpeakerAssignIn) (*SpeakerAssignOut, error) {
		out, _, err := assignSpeaker(ctx, a, in, "")
		return out, err
	})
	Def("footage.speakers.unmerge", "Separate two voices that were combined into one (undo giving them the same name)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *SpeakerUnmergeIn) (*SpeakersOut, error) {
		res, err := speakers.Unmerge(ctx, a, in.AssetID, in.Speaker)
		if err != nil {
			return nil, speakerErr(err)
		}
		afterSpeakerChange(ctx, a, res.Library)
		out, err := speakersOut(ctx, a, in.AssetID)
		if err != nil {
			return nil, err
		}
		n := len(res.Separated)
		out.Note = out.Summary
		out.Summary = fmt.Sprintf("Separated %s again.", count(n+1, "voice", "voices"))
		return out, nil
	})
	Def("footage.speakers.detect", "Find who speaks in videos (again): listens for the voices in the given videos, or in every video with a transcript but no voices yet", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *SpeakerDetectIn) (*SpeakerDetectOut, error) {
		out := &SpeakerDetectOut{Jobs: []speakers.Queued{}, Skipped: []string{}}
		var assets []models.Asset
		switch {
		case in.All && len(in.AssetIDs) > 0:
			return nil, invalid("Pick some videos or all of them, not both.")
		case in.All:
			as, err := speakers.MissingDetection(ctx, a)
			if err != nil {
				return nil, err
			}
			assets = as
		case len(in.AssetIDs) == 0:
			return nil, invalid("Say which videos to listen to, or all of them.")
		default:
			for _, id := range in.AssetIDs {
				var as models.Asset
				if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &as); err != nil {
					return nil, notFound("asset", id)
				}
				switch {
				case !as.Media.HasAudio:
					out.Skipped = append(out.Skipped, fmt.Sprintf("“%s” has no sound.", as.DisplayTitle()))
				case as.Transcript == nil || as.Transcript.State != "available":
					out.Skipped = append(out.Skipped, fmt.Sprintf("“%s” has no transcript yet (voices are found with it).", as.DisplayTitle()))
				default:
					assets = append(assets, as)
				}
			}
		}
		qs, err := speakers.QueueDetection(ctx, a, assets, speakers.QueueOptions{CatchUp: in.All})
		if err != nil {
			return nil, err
		}
		out.Jobs = qs
		for _, q := range qs {
			if q.Created {
				out.Queued++
			}
		}
		if out.Queued > 0 {
			a.Audit(ctx, "speakers.detect", "asset", qs[0].AssetID, "", map[string]any{"videos": len(qs), "queued": out.Queued, "all": in.All})
		}
		switch {
		case len(qs) == 0 && in.All:
			out.Summary = "Every video with a transcript already has its voices."
		case len(qs) == 0:
			out.Summary = "Nothing to listen to."
		case len(qs) == 1:
			out.Summary = fmt.Sprintf("Listening for the voices in “%s”.", qs[0].Title)
		default:
			out.Summary = fmt.Sprintf("Listening for the voices in %d videos.", len(qs))
		}
		return out, nil
	})
}

// count says "1 video" / "3 videos".
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func bsonSet(k string, v any) bson.M {
	return bson.M{"$set": bson.M{k: v, "updatedAt": time.Now().UTC()}}
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	Offer("footage.speakers.detect", Offering{ID: "who-speaks", Group: "understand", Title: "Who speaks",
		Does:  "Tells the voices in your footage apart and recognizes the people you named, on this computer.",
		Words: []string{"voices", "speakers", "who is talking"}, Needs: Needs{Works: []string{"transcripts", "editing"}}})
}
