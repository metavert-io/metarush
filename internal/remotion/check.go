package remotion

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
)

// Checking a version (D137): the change in plain words, its packages, the
// static read, a type check with MetaRush's TypeScript, the compositions it
// can affect, a low-resolution pass over each before and after in the jail to
// find where the picture changed, and full-size before/after stills there. A
// passing version waits for a person; nothing here can approve it.

// Check limits.
const (
	maxCheckCompositions = 6
	stillsPerComposition = 3
	checkSeconds         = 900
)

// checkOptions say what to check.
type checkOptions struct {
	Message      string
	Compositions []string // only these (else every affected one)
	NoStills     bool
	OutDir       string // where the stills are written on this machine
	Say          func(string)
}

// checkReport is a check's outcome with the local files of its stills.
type checkReport struct {
	Source   *models.RemotionSource
	Version  *models.RemotionVersion
	New      bool // a new version (else the files were an existing one)
	Same     bool // nothing changed against the base
	Check    *models.RemotionCheck
	Stills   []localStill
	Packages []models.RemotionPackageChange
}

type localStill struct {
	Composition string
	Frame       int
	At          float64
	Before      string
	After       string
	Difference  float64
}

// checkFolder makes a checkout folder the next version and checks it.
func checkFolder(ctx context.Context, a *app.App, dir string, o checkOptions) (*checkReport, error) {
	dir, err := fullPath(dir)
	if err != nil {
		return nil, err
	}
	l, err := readLock(dir)
	if err != nil {
		return nil, err
	}
	if l.WorkspaceID != "" && l.WorkspaceID != a.WorkspaceID() {
		return nil, apperr.New(apperr.Conflict, "That checkout belongs to another workspace.")
	}
	s, err := getSource(ctx, a, l.SourceID)
	if err != nil {
		return nil, err
	}
	base, err := getVersion(ctx, a, l.Base)
	if err != nil {
		return nil, err
	}
	baseM, err := loadManifest(ctx, a, base)
	if err != nil {
		return nil, err
	}
	say := o.Say
	if say == nil {
		say = func(string) {}
	}
	say("reading the folder")
	snap, err := readFolder(ctx, dir)
	if err != nil {
		return nil, err
	}
	if !l.Public {
		// public/ wasn't written: it stays as it was.
		for _, f := range baseM.Files {
			if isPublic(f.Path) {
				snap.files = append(snap.files, snapFile{ManifestFile: f})
			}
		}
	}
	if why := projectProblem(snap); why != "" {
		return nil, apperr.New(apperr.Validation, "%s", why)
	}
	var toSend []snapFile
	for _, f := range snap.files {
		if f.local != "" {
			if _, inBase := baseM.File(f.Path); !inBase || mustSend(baseM, f) {
				toSend = append(toSend, f)
			}
		}
	}
	say("saving the changed files")
	if err := uploadFiles(ctx, a, toSend, nil); err != nil {
		return nil, err
	}
	m := snap.manifest()
	if m.Hash() == base.Hash {
		return &checkReport{Source: s, Version: base, Same: true}, nil
	}
	mobj, err := putBytes(ctx, a, m.Bytes(), "application/json")
	if err != nil {
		return nil, err
	}
	files, bytes, pf, pb := totals(m)
	msg := strings.TrimSpace(o.Message)
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	v := &models.RemotionVersion{Hash: m.Hash(), Parent: base.Hash, Kind: models.RemotionKindEdit, Message: msg, By: a.Actor, At: time.Now().UTC(),
		Machine: a.Cfg.Installation.Name, Manifest: mobj, Files: files, Bytes: bytes, PublicFiles: pf, PublicBytes: pb}
	v, isNew, err := insertVersion(ctx, a, s, v)
	if err != nil {
		return nil, err
	}
	rep, err := checkVersion(ctx, a, s, v, o)
	if err != nil {
		return nil, err
	}
	rep.New = isNew
	return rep, nil
}

// mustSend reports a kept file whose bytes the base doesn't have.
func mustSend(base *Manifest, f snapFile) bool {
	b, ok := base.File(f.Path)
	return !ok || b.SHA256 != f.SHA256
}

// checkVersion checks a recorded version against its parent.
func checkVersion(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, o checkOptions) (*checkReport, error) {
	say := o.Say
	if say == nil {
		say = func(string) {}
	}
	started := time.Now()
	rep := &checkReport{Source: s, Version: v}
	chk := &models.RemotionCheck{At: time.Now().UTC(), By: a.Actor, Machine: a.Cfg.Installation.Name}
	rep.Check = chk
	finish := func() (*checkReport, error) {
		chk.Seconds = math.Round(time.Since(started).Seconds()*10) / 10
		chk.Summary = checkSummary(s, v, chk, rep.Packages)
		if err := setVersion(ctx, a, v.Hash, bson.M{"check": chk}); err != nil {
			return nil, err
		}
		v.Check = chk
		a.Audit(ctx, "remotion.check", "remotion", s.ID, s.ProjectID, map[string]any{"version": v.Hash, "ok": chk.OK, "stage": chk.Stage, "affected": len(chk.Affected), "changed": len(chk.Changed)})
		if fresh, err := getVersion(ctx, a, v.Hash); err == nil {
			remindApproval(ctx, a, s, fresh) // D172: a to-do and the bell when it waits for a person
		}
		return rep, nil
	}
	videoDir, release, err := rendererDir(a)
	if err != nil {
		return nil, err
	}
	defer release()
	m, err := loadManifest(ctx, a, v)
	if err != nil {
		return nil, err
	}
	var parent *models.RemotionVersion
	var parentM *Manifest
	if v.Parent != "" {
		if parent, err = getVersion(ctx, a, v.Parent); err != nil {
			return nil, err
		}
		if parentM, err = loadManifest(ctx, a, parent); err != nil {
			return nil, err
		}
	}
	// 1. The change in words.
	var cs []Change
	if parentM != nil {
		cs = Diff(parentM, m)
		chk.Changes, chk.Words = fileChanges(cs, func(f ManifestFile) ([]byte, bool) {
			if f.Size > 4<<20 {
				return nil, false
			}
			b, err := readObject(ctx, a, f.SHA256, f.Size)
			return b, err == nil
		})
	}
	// 2. Packages: a new set waits for a person before anything is installed.
	pi, perr := versionPackages(ctx, a, m)
	if perr != nil {
		chk.Stage = "packages"
		chk.Problems = append(chk.Problems, models.RemotionProblem{Severity: "error", Code: "packages", Message: upperFirst(perr.Error()) + "."})
		return finish()
	}
	allowed := packagesAllowed(s, pi.lockHash)
	if parent != nil {
		rep.Packages = diffPackages(parent.Packages, pi.packages)
	}
	if err := setVersion(ctx, a, v.Hash, bson.M{"packagesAllowed": allowed, "packagesChanged": rep.Packages}); err != nil {
		return nil, err
	}
	v.PackagesAllowed, v.PackagesChanged = allowed, rep.Packages
	if !allowed {
		chk.Stage = "packages"
		chk.Problems = append(chk.Problems, models.RemotionProblem{Severity: "error", Code: "packages_need_ok",
			Message: "This change " + packageWords(rep.Packages) + ". A person needs to allow that before MetaRush installs anything.",
			Hint:    "A person allows them (in the dashboard, or in their own terminal); then check it again."})
		// Still read it as text: the person sees what it is.
		rs, ps, err := readVersion(ctx, a, v, m, videoDir, readOptions{Allowed: false, Say: say})
		if err == nil {
			rs.done()
			chk.Problems = append(chk.Problems, ps...)
		}
		return finish()
	}
	// 3. Read it: static, packages, fonts, compositions in the jail.
	var prevFonts []models.RemotionFont
	if parent != nil {
		prevFonts = parent.Fonts
	}
	rs, ps, err := readVersion(ctx, a, v, m, videoDir, readOptions{Allowed: true, Jailed: true, PrevFonts: prevFonts, Say: say})
	if err != nil {
		return nil, err
	}
	defer rs.done()
	chk.Problems = append(chk.Problems, ps...)
	if v.Read != nil {
		chk.Blocked = v.Read.Blocked
	}
	if v.Read == nil || !v.Read.OK || hasErrors(ps) {
		chk.Stage = "read"
		return finish()
	}
	// 4. Types, with MetaRush's TypeScript (no project code runs).
	say("type-checking")
	types, terr := typeCheck(ctx, a, videoDir, rs.work)
	if terr != nil {
		chk.Problems = append(chk.Problems, models.RemotionProblem{Severity: "warning", Code: "types", Message: "The type check didn't run: " + terr.Error()})
	} else if newErrs := newTypeErrors(ctx, a, parent, parentM, videoDir, types); len(newErrs) > 0 {
		chk.Problems = append(chk.Problems, newErrs...)
		chk.Stage = "types"
		return finish()
	} else if n := countErrors(types); n > 0 {
		chk.Problems = append(chk.Problems, models.RemotionProblem{Severity: "warning", Code: "types_before",
			Message: fmt.Sprintf("The project's code has %s the type check finds; this change didn't add any.", plural(n, "problem", "problems"))})
	}
	// 5. What it can affect, and where the picture changed.
	var before []models.RemotionComposition
	if parent != nil {
		before = parent.Compositions
	}
	chk.Affected = affected(before, v.Compositions, cs, v.Static.StaticFiles)
	if len(o.Compositions) > 0 {
		chk.Affected = filterIn(chk.Affected, o.Compositions, v)
	}
	if parent == nil || o.NoStills || len(chk.Affected) == 0 {
		chk.OK, chk.Stage = true, ""
		return finish()
	}
	if err := pictureChanges(ctx, a, videoDir, s, parent, parentM, v, rs, rep, o, say); err != nil {
		var er *errRun
		if asErrRun(err, &er) {
			chk.Stage = "render"
			chk.Problems = append(chk.Problems, models.RemotionProblem{Severity: "error", Code: "render", Message: "MetaRush couldn't draw the frames: " + er.msg + "."})
			chk.Blocked = append(chk.Blocked, er.blocked...)
			return finish()
		}
		return nil, err
	}
	if len(chk.Blocked) > 0 {
		chk.Problems = append(chk.Problems, models.RemotionProblem{Severity: "error", Code: "network",
			Message: "Its code tried to reach " + joinAnd(dedupe(chk.Blocked)) + " while it was drawn; renders have no network, so MetaRush blocked it."})
		chk.Stage = "network"
		return finish()
	}
	chk.OK = true
	return finish()
}

func asErrRun(err error, target **errRun) bool {
	e, ok := err.(*errRun)
	if ok {
		*target = e
	}
	return ok
}

// pictureChanges runs the before/after frame pass and draws the stills.
func pictureChanges(ctx context.Context, a *app.App, videoDir string, s *models.RemotionSource, parent *models.RemotionVersion, parentM *Manifest,
	v *models.RemotionVersion, rs *readState, rep *checkReport, o checkOptions, say func(string)) error {
	chk := rep.Check
	comps := chk.Affected
	if len(comps) > maxCheckCompositions {
		comps = comps[:maxCheckCompositions]
	}
	out := o.OutDir
	if out == "" {
		out = filepath.Join(a.Cfg.Scratch.Dir, "remotion-check", v.Hash[:16])
	}
	_ = os.RemoveAll(out)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	// The parent's packages and files (its own environment when its lockfile differs).
	ppi, err := versionPackages(ctx, a, parentM)
	if err != nil {
		return err
	}
	penv := rs.env
	if ppi.envKey != rs.pi.envKey {
		if penv, err = ensureEnv(ctx, a, ppi, videoDir, say); err != nil {
			return err
		}
	}
	pwork, pdone, err := workTree(ctx, a, parent, parentM, penv)
	if err != nil {
		return err
	}
	defer pdone()
	pfonts, err := fontFiles(ctx, a, parent.Fonts)
	if err != nil {
		return err
	}
	nfonts, err := fontFiles(ctx, a, v.Fonts)
	if err != nil {
		return err
	}
	beforeB := runBundle{Name: "before", Work: pwork, Entry: parent.Entry, Bundle: bundleDir(a, parent), Fonts: pfonts}
	afterB := runBundle{Name: "after", Work: rs.work, Entry: v.Entry, Bundle: bundleDir(a, v), Fonts: nfonts}
	run := func(env string, bundles []runBundle, tasks []runTask) (*runResult, error) {
		res, err := jailRun(ctx, a, videoDir, runSpec{Env: env, Bundles: bundles, Tasks: tasks, MaxSeconds: checkSeconds, FrameTimeoutMs: frameTimeout(v)}, nil)
		if res != nil {
			chk.Blocked = append(chk.Blocked, res.Blocked...)
		}
		return res, err
	}
	// 1. A frame a second, small, before and after.
	say("comparing frames before and after the change")
	frameDir := func(side, comp string) string { return filepath.Join(out, "pass", side, comp) }
	var beforeTasks, afterTasks []runTask
	for _, c := range comps {
		nc := v.Composition(c)
		if nc == nil {
			continue
		}
		every := max(1, int(math.Round(nc.FPS)))
		afterTasks = append(afterTasks, runTask{Type: "frames", Bundle: "after", Composition: c, Every: every, Scale: 0.2, OutDir: frameDir("after", c)})
		if parent.Composition(c) != nil {
			beforeTasks = append(beforeTasks, runTask{Type: "frames", Bundle: "before", Composition: c, Every: every, Scale: 0.2, OutDir: frameDir("before", c)})
		}
	}
	if penv == rs.env {
		if _, err := run(rs.env, []runBundle{beforeB, afterB}, append(beforeTasks, afterTasks...)); err != nil {
			return err
		}
	} else {
		if _, err := run(penv, []runBundle{beforeB}, beforeTasks); err != nil {
			return err
		}
		if _, err := run(rs.env, []runBundle{afterB}, afterTasks); err != nil {
			return err
		}
	}
	// 2. Where they differ: moments, and the frames worth drawing.
	type pick struct {
		comp  string
		frame int
		at    float64
		diff  float64
	}
	var picks []pick
	for _, c := range comps {
		nc := v.Composition(c)
		if nc == nil {
			continue
		}
		chk.Compared = append(chk.Compared, c)
		every := max(1, int(math.Round(nc.FPS)))
		after := framesIn(frameDir("after", c))
		beforeFrames := framesIn(frameDir("before", c))
		var diffs []float64
		for i := range after {
			d := 1.0
			if i < len(beforeFrames) {
				if x, err := frameDiff(beforeFrames[i], after[i]); err == nil {
					d = x
				}
			}
			diffs = append(diffs, d)
		}
		changed := false
		var cur *models.RemotionMoment
		best := map[int]float64{}
		for i, d := range diffs {
			at := float64(i*every) / nc.FPS
			if d > changeThreshold {
				changed = true
				if cur == nil {
					chk.Moments = append(chk.Moments, models.RemotionMoment{Composition: c, From: round3(at), To: round3(at)})
					cur = &chk.Moments[len(chk.Moments)-1]
				}
				cur.To = round3(math.Min(nc.Duration, at+float64(every)/nc.FPS))
				m := len(chk.Moments) - 1
				if d > best[m] {
					best[m] = d
					picks = append(picks, pick{c, i * every, at, d})
				}
			} else {
				cur = nil
			}
		}
		if len(beforeFrames) != len(after) && len(beforeFrames) > 0 {
			changed = true // the length changed
		}
		if changed {
			chk.Changed = append(chk.Changed, c)
		}
	}
	// The strongest moment per stretch, at most a few per composition.
	sort.SliceStable(picks, func(i, j int) bool { return picks[i].diff > picks[j].diff })
	per := map[string]int{}
	seen := map[string]bool{}
	var chosen []pick
	for _, p := range picks {
		k := fmt.Sprintf("%s@%d", p.comp, p.frame)
		if seen[k] || per[p.comp] >= stillsPerComposition {
			continue
		}
		// One still per stretch: skip frames within 2 seconds of a chosen one.
		near := false
		for _, c := range chosen {
			if c.comp == p.comp && math.Abs(c.at-p.at) < 2 {
				near = true
			}
		}
		if near {
			continue
		}
		seen[k] = true
		per[p.comp]++
		chosen = append(chosen, p)
	}
	sort.SliceStable(chosen, func(i, j int) bool {
		if chosen[i].comp != chosen[j].comp {
			return indexOf(comps, chosen[i].comp) < indexOf(comps, chosen[j].comp)
		}
		return chosen[i].frame < chosen[j].frame
	})
	if len(chosen) == 0 {
		return nil
	}
	// 3. Full stills there, before and after.
	say("drawing before and after stills")
	var bi, ai []stillItem
	for _, p := range chosen {
		name := fmt.Sprintf("%s-%05d", p.comp, p.frame)
		scale := 0.5
		if c := v.Composition(p.comp); c != nil {
			scale = fitScale(c.Width, 960) // about 960 pixels wide: enough to read words, small to keep
		}
		ai = append(ai, stillItem{Composition: p.comp, Frame: p.frame, Out: filepath.Join(out, name+"-after.jpg"), Scale: scale})
		if parent.Composition(p.comp) != nil {
			bi = append(bi, stillItem{Composition: p.comp, Frame: p.frame, Out: filepath.Join(out, name+"-before.jpg"), Scale: scale})
		}
	}
	tasks := []runTask{{Type: "stills", Bundle: "after", Items: ai}}
	if len(bi) > 0 {
		tasks = append([]runTask{{Type: "stills", Bundle: "before", Items: bi}}, tasks...)
	}
	if penv == rs.env {
		if _, err := run(rs.env, []runBundle{beforeB, afterB}, tasks); err != nil {
			return err
		}
	} else {
		if len(bi) > 0 {
			if _, err := run(penv, []runBundle{beforeB}, tasks[:1]); err != nil {
				return err
			}
		}
		if _, err := run(rs.env, []runBundle{afterB}, tasks[len(tasks)-1:]); err != nil {
			return err
		}
	}
	for _, p := range chosen {
		name := fmt.Sprintf("%s-%05d", p.comp, p.frame)
		ls := localStill{Composition: p.comp, Frame: p.frame, At: round3(p.at), After: filepath.Join(out, name+"-after.jpg"), Difference: math.Round(p.diff*1000) / 1000}
		after, err := putLocal(ctx, a, ls.After, "image/jpeg")
		if err != nil {
			return err
		}
		st := models.RemotionStill{Composition: p.comp, Frame: p.frame, At: ls.At, After: after, Difference: ls.Difference}
		if bp := filepath.Join(out, name+"-before.jpg"); exists(bp) {
			ls.Before = bp
			if obj, err := putLocal(ctx, a, bp, "image/jpeg"); err == nil {
				st.Before = &obj
			}
		}
		if c := v.Composition(p.comp); c != nil {
			st.After.Width, st.After.Height = c.Width/2, c.Height/2
			if st.Before != nil {
				st.Before.Width, st.Before.Height = c.Width/2, c.Height/2
			}
		}
		chk.Stills = append(chk.Stills, st)
		rep.Stills = append(rep.Stills, ls)
	}
	return nil
}

func indexOf(xs []string, x string) int {
	for i, y := range xs {
		if y == x {
			return i
		}
	}
	return len(xs)
}

// filterIn keeps the compositions asked for (known ones).
func filterIn(affected, want []string, v *models.RemotionVersion) []string {
	var out []string
	for _, w := range want {
		if v.Composition(w) != nil {
			out = append(out, w)
		}
	}
	if len(out) == 0 {
		return affected
	}
	return out
}

// newTypeErrors are the type problems this version has that its parent didn't.
func newTypeErrors(ctx context.Context, a *app.App, parent *models.RemotionVersion, parentM *Manifest, videoDir string, now []models.RemotionProblem) []models.RemotionProblem {
	if countErrors(now) == 0 {
		return nil
	}
	old := map[string]bool{}
	if parent != nil && parentM != nil {
		if ppi, err := versionPackages(ctx, a, parentM); err == nil {
			if env, err := ensureEnv(ctx, a, ppi, videoDir, nil); err == nil {
				if pwork, done, err := workTree(ctx, a, parent, parentM, env); err == nil {
					if ps, err := typeCheck(ctx, a, videoDir, pwork); err == nil {
						for _, p := range ps {
							old[p.File+"\x00"+p.Message] = true
						}
					}
					done()
				}
			}
		}
	}
	var out []models.RemotionProblem
	for _, p := range now {
		if p.Severity == "error" && !old[p.File+"\x00"+p.Message] {
			p.Message = "Type problem: " + p.Message
			out = append(out, p)
		}
	}
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

func countErrors(ps []models.RemotionProblem) int {
	n := 0
	for _, p := range ps {
		if p.Severity == "error" {
			n++
		}
	}
	return n
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// checkSummary says what the check found, in plain words.
func checkSummary(s *models.RemotionSource, v *models.RemotionVersion, chk *models.RemotionCheck, pk []models.RemotionPackageChange) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Version %d of “%s”", v.Number, s.Title)
	if v.Message != "" {
		fmt.Fprintf(&b, " (“%s”)", clip(v.Message, 120))
	}
	b.WriteString(": ")
	if w := changeWords(chk.Changes, chk.Words); w != "" {
		b.WriteString(w + " ")
	} else if len(chk.Changes) == 0 && v.Parent == "" {
		b.WriteString("the project as it was brought in. ")
	}
	if len(pk) > 0 && v.PackagesAllowed {
		b.WriteString("It " + packageWords(pk) + ". ")
	}
	if !chk.OK {
		for _, p := range chk.Problems {
			if p.Severity == "error" {
				b.WriteString(strings.TrimSpace(p.Message))
				if !strings.HasSuffix(b.String(), ".") {
					b.WriteString(".")
				}
				return b.String()
			}
		}
		return b.String() + "It didn't pass its check."
	}
	switch {
	case len(chk.Changed) > 0:
		var where []string
		for _, m := range chk.Moments {
			if len(where) >= 4 {
				break
			}
			where = append(where, fmt.Sprintf("%s %s–%s", m.Composition, clockTenths(m.From), clockTenths(m.To)))
		}
		b.WriteString("The picture changes in " + joinAnd(where) + ".")
		if same := len(chk.Compared) - len(chk.Changed); same > 0 {
			fmt.Fprintf(&b, " %s compared %s the same.", upperFirst(plural(same, "other composition", "other compositions")), map[bool]string{true: "looks", false: "look"}[same == 1])
		}
		if rest := len(chk.Affected) - len(chk.Compared); rest > 0 {
			fmt.Fprintf(&b, " %s it could affect %s compared (check them by name to see).", upperFirst(plural(rest, "more composition", "more compositions")), map[bool]string{true: "wasn't", false: "weren't"}[rest == 1])
		}
	case len(chk.Compared) > 0:
		b.WriteString("No composition compared (" + joinAnd(chk.Compared) + ") looks different.")
		if rest := len(chk.Affected) - len(chk.Compared); rest > 0 {
			fmt.Fprintf(&b, " %s it could affect %s compared.", upperFirst(plural(rest, "more composition", "more compositions")), map[bool]string{true: "wasn't", false: "weren't"}[rest == 1])
		}
	case len(chk.Affected) > 0:
		b.WriteString("It can affect " + joinAnd(chk.Affected) + ".")
	default:
		b.WriteString("No composition's picture changes.")
	}
	if v.State == models.RemotionWaiting {
		b.WriteString(" It waits for a person to approve it.")
	}
	return b.String()
}

func clockTenths(s float64) string {
	m := int(s) / 60
	sec := s - float64(m*60)
	return fmt.Sprintf("%d:%04.1f", m, sec)
}
