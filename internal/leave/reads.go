package leave

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
)

// What each read shows an AI (D290): every operation an AI channel can call
// (anything that neither changes things nor is people-only) says here which
// of a client's things its answer can carry — F footage, V voices, A faces,
// W words, P pictures — or "-" for none of the client's material (names,
// settings, counts, states). Anything that can carry to-do questions,
// notifications, activity lines, card titles, notes or log text counts as
// words: those quote what was said. TestEveryReadSaysWhatItShows keeps the
// table whole and TestQuietReadsQuoteNothing runs every "-" read against a
// client's transcript and to-do. TestEveryReadSaysWhatItShows keeps the table whole; an
// operation missing from it is refused on AI channels while any client has
// rules (deny by default).
var readsShow = map[string]string{
	"ability.find": "-", "ability.list": "-", "ability.show": "-", "ability.plan": "W", "ability.runs": "W",
	"account.preferences": "-", "activity.summary": "W", "add.route": "-",
	"ai.estimate": "W", "ai.list": "W", "ai.local": "-", "ai.models": "-", "ai.rules": "-", "ai.show": "WPA", "ai.voices": "-",
	"asset.card": "WPA", "asset.fixes": "PA", "asset.list": "-", "asset.names": "W", "asset.precheck": "-", "asset.show": "WPA", "asset.where": "-",
	"assistant.list": "W", "assistant.mode": "-", "assistant.models": "-", "assistant.proposals": "W", "assistant.settings": "-",
	"assistant.show": "WPA", "assistant.spending": "-", "assistant.status": "-", "assistant.suggestions": "W",
	"audit.list": "W", "auth.status": "-", "brand.show": "P", "brand.suggest": "P",
	"cache.prefetch": "-", "cache.prune": "-", "cache.repair": "-", "cache.status": "-",
	"captions.at": "W", "captions.check": "W", "captions.file": "W", "captions.languages": "W", "captions.lines": "W", "captions.looks": "-",
	"collection.list": "-", "collection.show": "-", "comment.list": "W", "consent.list": "W", "consent.show": "WA",
	"content.leave": "-", "content.rules.list": "-", "content.show": "-", "content.signoffs.list": "-", "content.style.show": "-",
	"conversation.detect": "-", "conversation.list": "-", "conversation.moments": "W", "conversation.show": "W", "conversation.styles": "-",
	"cut.check": "W", "cut.compare": "W", "cut.diff": "W", "cut.show": "W", "cut.signoffs.list": "-", "cut.where": "-",
	"cut.say.estimate": "W", "cut.say.list": "W", "cut.say.show": "W",
	"delivery.notes": "W", "delivery.ready": "W", "delivery.gate": "-", "draft.list": "W", "edit.check": "W", "edit.preview": "W", "edit.templates": "-", "edit.transitions": "-",
	"extension.status":          "-",
	"footage.describe.settings": "-", "footage.evidence": "W", "footage.faces": "PA", "footage.frames": "PA", "footage.keyframes": "WPA",
	"footage.language": "-", "footage.captions": "W", "footage.moments": "W", "footage.sheet": "WPA", "footage.speakers": "VW", "footage.speakers.samples": "V",
	"footage.switches": "WA", "footage.transcript": "W", "footage.visuals": "WPA", "footage.visuals.status": "-",
	"generate.estimate": "W", "generate.list": "W", "generate.settings": "-", "generate.show": "WPA",
	"improve.export": "W", "improve.list": "W", "improve.schedule": "-", "improve.show": "W", "improve.signals": "W",
	"ingest.sessions": "-", "ingest.url.tools": "-", "job.list": "W", "job.show": "W", "keep.list": "W", "library.list": "-",
	"log.export": "W", "log.hooks.status": "-", "log.list": "W", "log.search": "W", "log.settings": "-", "log.show": "W",
	"looks.list": "-", "looks.show": "FPA", "machine.list": "-", "machine.rekeyPlan": "-",
	"mix.preview": "-", "mix.show": "-", "motion.list": "-", "motion.new": "-", "motion.show": "WP",
	"notification.list": "W", "ops.describe": "-", "ops.find": "-",
	"people.compare": "-", "people.evidence": "-", "people.learned": "VA", "people.list": "V", "people.show": "V",
	"preview.prepare": "-", "project.checkout": "W", "project.leaves": "-", "project.links": "-", "project.list": "-", "project.pull": "W",
	"project.receipt": "W", "project.rules.list": "-", "project.show": "-", "project.sofar": "W", "project.status": "-",
	"provider.list": "-", "provider.status": "-", "provider.test": "-", "qa.show": "WPA",
	"recipe.check": "W", "recipe.checkout": "W", "recipe.compare": "W", "recipe.diff": "W", "recipe.export": "FVAWP", "recipe.list": "-",
	"recipe.plan": "W", "recipe.propose": "W", "recipe.runs": "-", "recipe.show": "W", "recipe.starters": "-", "recipe.suggestions": "W", "recipe.triggers": "-",
	"remote.status":     "-",
	"remotion.checkout": "WP", "remotion.list": "-", "remotion.map": "WPA", "remotion.pictures": "P", "remotion.push.list": "-",
	"remotion.push.plan": "W", "remotion.refresh.log": "W", "remotion.refresh.plan": "-", "remotion.refresh.runs": "-",
	"remotion.show": "WP", "remotion.stills": "PA", "remotion.words": "W",
	"render.fetch": "FVAWP", "render.follow.show": "-", "render.list": "-", "render.status": "-",
	"resources.folders": "-", "resources.list": "-", "resources.path": "P", "resources.pictures": "-", "resources.show": "-", "resources.where": "-",
	"review.packet": "W", "review.waiting": "W", "revision.compare": "W", "revision.list": "-", "revision.plan": "W", "revision.show": "W",
	"search": "W", "setup.status": "-", "components.list": "-", "sound.compare": "-", "sound.preview": "-", "sound.show": "-",
	"share.list": "W", "share.plan": "-", "share.settings": "-",
	"storage.plan": "-", "storage.status": "-", "sync.status": "-",
	"database.plan": "-", "database.move.status": "-", // where the database is, counts, computers' names (D338)
	"database.late.writes": "-", // kinds of record written late, when and on which computer: never their contents (D357)
	"todos.counts":         "-", "todos.groups": "WPA", "todos.list": "W", "todos.show": "VWA",
	"transcript.find": "W", "transcript.history": "W", "transcript.pending": "W", "transcript.spelling.list": "-", "transcript.spelling.suggestions": "W",
	"trash.list": "-", "trash.settings": "-", "users.list": "-", "version.show": "-",
	"voiceover.choose": "W", "voiceover.estimate": "W", "voiceover.fit": "W", "voiceover.models": "-", "voiceover.parse": "W",
	"voiceover.preview": "-", "voiceover.show": "W", "voiceover.takes": "VW", "voiceover.voices": "-",
	"watch.browse": "-", "watch.choose": "-", "watch.reveal": "-", "watch.status": "-",
	"worker.list": "-", "worker.settings": "-", "workspace.show": "-",
	"catalogue.list": "-", "catalogue.show": "-", "content.pack.show": "-", "content.pack.preview": "P",
	"recording.list": "-", "recording.show": "-",
	// Presenters (D273): their records quote the service's consent words with the person's name.
	"presenter.list": "W", "presenter.show": "W", "presenter.stock": "-", "presenter.optout": "-",
	// Built-in connections (D303–D306): a timeline's markers, titles and what can't
	// carry over are words; a post's plan quotes the delivery notes; a cloud
	// folder's listing is names, sizes and checksums; the platforms are settings.
	"timeline.read": "W", "publish.plan": "W", "publish.platforms": "-", "add.cloud.list": "-",
}

var kindLetters = map[rune]string{'F': models.LeaveFootage, 'V': models.LeaveVoices, 'A': models.LeaveFaces, 'W': models.LeaveWords, 'P': models.LeavePictures}

// ReadKinds says what an operation shows an AI of a client's things, and
// whether it said so at all.
func ReadKinds(op string) ([]string, bool) {
	code, ok := readsShow[op]
	if !ok {
		return nil, false
	}
	var out []string
	for _, c := range code {
		if k := kindLetters[c]; k != "" {
			out = append(out, k)
		}
	}
	return Kinds(false, out), true
}

// DeclareRead adds an operation's declaration (for packages registering
// reads of their own).
func DeclareRead(op, code string) { readsShow[op] = code }

// ShowingReads are the operations that can show a client's things.
func ShowingReads() []string {
	var out []string
	for op, code := range readsShow {
		if code != "-" {
			out = append(out, op)
		}
	}
	sort.Strings(out)
	return out
}

// Declared reports an operation that says what it shows.
func Declared(op string) bool { _, ok := readsShow[op]; return ok }

// aiOutput keeps Claude in the panel to what it may see when any client's
// rules keep things from it: the frame and cache folders are closed to it
// (spawn), so every file an answer names there is copied into the
// conversation's own folder and named there instead.
func aiOutput(ctx context.Context, a *app.App, op string, out any) any {
	if a.Actor.Via != models.ViaAssistant || out == nil {
		return out
	}
	dir := os.Getenv("METARUSH_ASSISTANT_RUN_DIR")
	if dir == "" || !AnyKeptFromClaude(ctx, a) {
		return out
	}
	roots := []string{realPath(a.Cfg.Scratch.Dir), realPath(a.Cfg.Cache.Dir)}
	seen := filepath.Join(dir, "proposals", "seen")
	rewritePaths(reflect.ValueOf(out), func(p string) (string, bool) {
		rp := realPath(p)
		inside := false
		for _, r := range roots {
			if r != "" && (rp == r || strings.HasPrefix(rp, r+string(filepath.Separator))) {
				inside = true
			}
		}
		if !inside {
			return "", false
		}
		st, err := os.Stat(rp)
		if err != nil || !st.Mode().IsRegular() {
			return "", true // a folder or nothing: the shared path is never shown
		}
		sum := sha256.Sum256([]byte(rp))
		dst := filepath.Join(seen, hex.EncodeToString(sum[:6])+"-"+filepath.Base(rp))
		if err := copyFile(rp, dst); err != nil {
			return "", true
		}
		return dst, true
	}, 0)
	return out
}

// rewritePaths walks an answer and replaces strings that are paths in the
// shared folders (fix returns the new path; ok false: not one of them).
func rewritePaths(v reflect.Value, fix func(string) (string, bool), depth int) {
	if depth > 12 || !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			rewritePaths(v.Elem(), fix, depth+1)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				rewritePaths(v.Field(i), fix, depth+1)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			rewritePaths(v.Index(i), fix, depth+1)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			e := v.MapIndex(k)
			if e.Kind() == reflect.String || (e.Kind() == reflect.Interface && !e.IsNil() && e.Elem().Kind() == reflect.String) {
				s := e.String()
				if e.Kind() == reflect.Interface {
					s = e.Elem().String()
				}
				if nv, ok := fix(s); ok {
					v.SetMapIndex(k, reflect.ValueOf(nv).Convert(v.Type().Elem()))
				}
				continue
			}
			c := reflect.New(e.Type()).Elem()
			c.Set(e)
			rewritePaths(c, fix, depth+1)
			v.SetMapIndex(k, c)
		}
	case reflect.String:
		if v.CanSet() && filepath.IsAbs(v.String()) {
			if nv, ok := fix(v.String()); ok {
				v.SetString(nv)
			}
		}
	}
}

func copyFile(src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func realPath(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
