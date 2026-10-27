package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/secretpath"
)

// homeWarning is a warning about where a home is: Detail is the technical
// text (init --json keeps it), Say the same in plain words.
type homeWarning struct{ Detail, Say string }

// checkHomeLocation applies the secret-location rules when a home is set up
// (init, machine join). With --secrets env the secrets live in <home>/.env, so
// that file is checked like every secret file MetaRush writes: never inside a
// sync folder; inside a repository only when git ignores it and
// --allow-unsafe-secret-path is given. A keychain-mode home keeps no secrets,
// so a repository or sync-folder location is only reported (doctor fails it).
func checkHomeLocation(h string, o *initOpts) ([]homeWarning, error) {
	v := secretpath.Inspect(h)
	if o.secrets == "env" {
		if v.Kind == secretpath.KindGit {
			// Save writes the home's own ignore file; judge the location as it will be.
			if err := os.MkdirAll(h, 0o700); err != nil {
				return nil, err
			}
			if err := config.EnsureHomeIgnoreFile(h); err != nil {
				return nil, err
			}
		}
		if err := secretpath.Check(config.DotEnvPath(h), "the secrets file for --secrets env (<home>/.env)", o.allowUnsafeSecretPath); err != nil {
			return nil, err
		}
	}
	var warns []homeWarning
	switch v.Kind {
	case secretpath.KindSync:
		warns = append(warns, homeWarning{fmt.Sprintf("the installation home is %s: its state and cache get uploaded (secrets stay in the keychain); prefer a home outside synced folders — `metarush doctor` reports this as a failure", v.Describe()),
			fmt.Sprintf("MetaRush's folder is inside %s, which uploads what is in it (passwords and keys stay in the keychain). A folder outside synced folders is better.", v.Service)})
	case secretpath.KindGit:
		warns = append(warns, homeWarning{fmt.Sprintf("the installation home is inside the git repository %s; its own .gitignore keeps it out of commits — keep that file", v.Root),
			"MetaRush's folder is inside a code repository. Its own ignore file keeps it out of commits, so keep that file."})
	}
	return warns, nil
}

// homeSecurityChecks are doctor's secret-location checks. They need only the
// home path, so they run even when config.Load refuses an unsafe <home>/.env.
func homeSecurityChecks(h string, add addCheck) {
	moveFix := "move the installation home outside repositories and synced folders (e.g. ~/.metarush), keeping secrets in the keychain"
	moveDo := "Move MetaRush's folder out of synced folders and code repositories."
	// Judge the home by its contents (config.yaml): the home's own .gitignore
	// ignores what is inside it, not the folder entry itself.
	switch v := secretpath.Inspect(config.Path(h)); {
	case v.Kind == secretpath.KindSync:
		add("security", "home location", "fail", "the installation home is "+v.Describe(), moveFix,
			fmt.Sprintf("MetaRush's folder is inside %s, which uploads what is in it", v.Service), moveDo)
	case v.Kind == secretpath.KindGit && !v.Ignored:
		add("security", "home location", "fail", "the installation home is "+v.Describe(), "keep its .gitignore (`*`), or "+moveFix,
			"MetaRush's folder is inside a code repository and could be committed with it", "Keep the folder's ignore file, or move the folder out of the repository.")
	case v.Kind == secretpath.KindGit:
		add("security", "home location", "warn", "the installation home is "+v.Describe(), moveFix,
			"MetaRush's folder is inside a code repository (git ignores it, so it isn't committed)", "Move it somewhere else, such as your home folder.")
	default:
		add("security", "home location", "ok", "outside git repositories and synced folders", "")
	}
	if config.Exists(h) {
		if config.HomeIgnoreFileOK(h) {
			add("security", "home .gitignore", "ok", "ignores everything in the home (defense in depth)", "")
		} else {
			add("security", "home .gitignore", "warn", filepath.Join(h, config.HomeIgnoreFileName)+" is missing or does not ignore everything", "write a line containing * to it (MetaRush recreates a missing one on the next run)",
				"A safety file in MetaRush's folder (the one that keeps it out of code repositories) is missing or changed", "A missing one comes back by itself; a changed one must ignore everything again.")
		}
	}
	env := config.DotEnvPath(h)
	if _, err := os.Stat(env); err == nil {
		switch v := secretpath.Inspect(env); {
		case v.Kind == secretpath.KindSync:
			add("security", ".env location", "fail", env+" is "+v.Describe(), "move the secrets into the keychain (`metarush init --reconfigure --secrets keychain`) and rotate them — the file may already be uploaded",
				fmt.Sprintf("The file with your passwords and keys is inside %s, so it may already be uploaded", v.Service), "Move them into the keychain and replace them: they may be exposed.")
		case v.Kind == secretpath.KindGit && !v.Ignored:
			add("security", ".env location", "fail", env+" is not ignored by git (it could be committed)", "add it to .gitignore and check `git log` that it was never committed (rotate the credentials if it was), or use the keychain",
				"The file with your passwords and keys could be committed to a code repository", "Make git ignore it (and replace the keys if it was ever committed), or use the keychain.")
		case v.Kind == secretpath.KindGit:
			add("security", ".env location", "warn", env+" is "+v.Describe(), "prefer the keychain, or a home outside the repository",
				"The file with your passwords and keys is inside a code repository (git ignores it)", "Use the keychain instead, or keep MetaRush's folder outside the repository.")
		default:
			add("security", ".env location", "ok", env+" (outside repositories and synced folders)", "")
		}
		if mode, loose := secretpath.LooseMode(env); loose {
			add("security", ".env permissions", "warn", fmt.Sprintf("%s is %v", env, mode), "chmod 600 it",
				"Other people on this computer can read the file with your passwords and keys", "Make it readable only by you.")
		}
	}
	for _, f := range []string{filepath.Join(h, "state", "confirm.key"), filepath.Join(h, "state", "sessions.json"), filepath.Join(h, "state", "providers.json")} {
		if mode, loose := secretpath.LooseMode(f); loose {
			add("security", "secret file permissions", "warn", fmt.Sprintf("%s is %v (readable by other users)", f, mode), "chmod 600 it",
				"Other people on this computer can read one of MetaRush's private files", "Make it readable only by you.")
		}
	}
	dirs := []string{h}
	if d, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(d, secretpath.DefaultDirName))
	}
	if wd, err := os.Getwd(); err == nil {
		if v := secretpath.Inspect(wd); v.Kind == secretpath.KindGit {
			dirs = append(dirs, v.Root) // the repository doctor runs in
		}
	}
	stray := secretpath.FindStray(dirs, 3, 20000, env)
	if len(stray) > 0 {
		add("security", "stray credential files", "warn", fmt.Sprintf("%d file(s) with credentials: %s", len(stray), strings.Join(stray, ", ")),
			"delete join bundles after `machine join` and CI dotenv files after loading them into CI secrets",
			fmt.Sprintf("Found %s holding passwords or keys that should have been deleted", plural(len(stray), "file")),
			"Delete a machine's join file once it has joined, and test-key files once they are loaded.")
	} else {
		add("security", "stray credential files", "ok", "no join bundles or credential dotenv files in the home, ~/"+secretpath.DefaultDirName+" or this repository", "")
	}
}
