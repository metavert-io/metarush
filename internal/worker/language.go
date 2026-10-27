package worker

import (
	"context"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcribe"
)

// clipLanguage decides the language a clip is written down in (D243): the
// language a person set (on the clip, or with the job that asks for it
// again), else the one heard before (a clip transcribed again after a fix
// keeps its language), else what whisper.cpp's language detection hears now,
// else the workspace's language. Another language than the workspace's is
// taken only when the detection is sure of it (transcribe.DetectSure); when
// it isn't, the clip keeps the workspace's language and says what was heard.
func clipLanguage(ctx context.Context, w *Worker, as *models.Asset, j *models.Job, wav, scratch string) models.SpokenLanguage {
	now := time.Now().UTC()
	if code, _ := j.Payload["language"].(string); code != "" {
		by, _ := j.Payload["languageBy"].(string)
		uid, _ := j.Payload["userId"].(string)
		if by == "" {
			by = models.LanguagePerson
		}
		return models.SpokenLanguage{Code: code, By: by, UserID: uid, At: now}
	}
	if l := as.Language; l != nil && l.Code != "" && (l.By == models.LanguagePerson || l.By == models.LanguageHeard) {
		return *l
	}
	def := models.SpokenLanguage{Code: w.Langs.Default, By: models.LanguageSettings, At: now}
	if w.Langs.Detect == nil {
		return def
	}
	h, err := w.Langs.Detect.Detect(ctx, wav, scratch)
	if err != nil {
		w.Log("listening for the language of %s failed (the workspace's language is used): %v", as.ID, err)
		return def
	}
	def.Listened = h.Listened
	switch {
	case h.Language == "":
		return def // too little speech to tell
	case sameBase(h.Language, def.Code):
		return models.SpokenLanguage{Code: def.Code, By: models.LanguageHeard, Score: h.Score, Listened: h.Listened, At: now}
	case h.Score >= transcribe.DetectSure && w.Langs.For(h.Language) != nil:
		return models.SpokenLanguage{Code: h.Language, By: models.LanguageHeard, Score: h.Score, Listened: h.Listened, At: now}
	}
	def.Heard, def.Score = h.Language, h.Score
	return def
}

func sameBase(a, b string) bool {
	base := func(s string) string {
		s = strings.ToLower(s)
		if i := strings.IndexByte(s, '-'); i > 0 {
			return s[:i]
		}
		return s
	}
	return base(a) == base(b)
}

// cutLanguage is the language most of a cut's words are spoken in (each
// clip's language weighed by the words the cut plays from it), else def.
func cutLanguage(assets []models.Asset, words map[string][]editspec.TranscriptWord, def string) string {
	count := map[string]int{}
	best, most := def, 0
	for _, as := range assets {
		code := ""
		switch {
		case as.Language != nil && as.Language.Code != "":
			code = as.Language.Code
		case as.Transcript != nil && as.Transcript.Language != "":
			code = as.Transcript.Language
		}
		if code == "" || len(words[as.ID]) == 0 {
			continue
		}
		count[code] += len(words[as.ID])
		if count[code] > most || (count[code] == most && code < best) {
			best, most = code, count[code]
		}
	}
	return best
}

// personLanguageSince is a language a person set for the clip after since
// that isn't the one this job writes (nil when there is none).
func personLanguageSince(ctx context.Context, a *app.App, assetID string, since time.Time, spoken *models.SpokenLanguage) *models.SpokenLanguage {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
		return nil
	}
	l := as.Language
	if l == nil || l.By != models.LanguagePerson || !l.At.After(since) {
		return nil
	}
	if spoken.By == models.LanguagePerson && sameBase(spoken.Code, l.Code) {
		return nil
	}
	return l
}
