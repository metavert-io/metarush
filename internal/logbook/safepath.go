package logbook

import (
	"sync"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/secretpath"
)

// inspector decides where paths live (tests replace it).
var inspector = secretpath.Inspector{}

// CheckOutput refuses to write log data (exports, bug reports, improvement
// lists) where it could be shared by mistake: inside a git repository — even
// one that ignores the file —, a cloud-sync folder (internal/secretpath decides
// both) or a MetaRush project folder. path is the file to write; its folder need
// not exist yet.
func CheckOutput(path string) error {
	v := inspector.Inspect(path)
	details := map[string]any{"path": v.Path, "kind": v.Kind, "root": v.Root, "service": v.Service}
	switch v.Kind {
	case secretpath.KindGit:
		return apperr.New(apperr.InvalidInput, "that folder is inside a code repository, where the log could end up committed and shared").
			WithHint("save it outside the repository, for example in your Downloads folder").WithDetails(details)
	case secretpath.KindSync:
		return apperr.New(apperr.InvalidInput, "that folder is in %s, which uploads everything saved there", v.Service).
			WithHint("save it in a folder that is not synced, for example your Downloads folder; share the file yourself if you want to").WithDetails(details)
	}
	// The same marker `metarush project checkout` writes (secretpath.ProjectFolderMarker).
	if dir := secretpath.ProjectFolder(v.Path); dir != "" {
		details["kind"], details["root"] = "project", dir
		return apperr.New(apperr.InvalidInput, "that folder is inside a MetaRush project folder, which is shared with the project").
			WithHint("save it outside the project folder, for example in your Downloads folder").WithDetails(details)
	}
	return nil
}

// localBodies says whether prompts can be kept in this installation's home
// (log.prompts: local): not when the home is in a cloud-sync folder or where
// git would commit it — "this machine only" would not be true there.
type localBodies struct {
	once   sync.Once
	reason string // why not ("" when they can)
}

func (lb *localBodies) check(dir string) string {
	lb.once.Do(func() {
		if err := inspector.CheckUse(dir, "prompts kept on this machine"); err != nil {
			v := inspector.Inspect(dir)
			if v.Kind == secretpath.KindSync {
				lb.reason = "MetaRush's folder on this machine is in " + v.Service + ", which uploads everything in it, so what you ask Claude and its answers are not kept"
			} else {
				lb.reason = "MetaRush's folder on this machine is inside a code repository that would commit it, so what you ask Claude and its answers are not kept"
			}
		}
	})
	return lb.reason
}

// LocalPromptsProblem explains why prompts cannot be kept on this machine
// ("" when they can, or when log.prompts is not local).
func (l *Logger) LocalPromptsProblem() string {
	if l == nil || l.cfg == nil || l.Privacy() != PrivacyLocal {
		return ""
	}
	return l.local.check(l.cfg.LogBodiesDir())
}
