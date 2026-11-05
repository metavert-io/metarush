package secretpath

import (
	"encoding/json"
	"io"
	"os"
	"path"
	"strings"

	"github.com/metavert-io/metarush/internal/wsl"
)

// Windows users' sync folders as WSL shows Windows' drives (/mnt/c, D523).
// The services put their folders in the user's own folder by default
// (C:\Users\Sam\OneDrive, "OneDrive - Contoso", Dropbox, iCloudDrive…), and
// let people move them — most often to the top of another drive (D:\Dropbox,
// E:\OneDrive - Contoso), where a footage drive is; Dropbox's own settings
// say where its folders are, wherever they went. OneDrive syncs an
// organization's SharePoint and Teams libraries into a folder named after the
// organization, beside "OneDrive - <organization>"; Google Drive is a drive
// of its own (G:\My Drive); iCloud for Windows also syncs Pictures\iCloud
// Photos. Everything is read from the path and from files on Windows' side:
// no program runs.
func windowsSyncFolder(p string) (service, root string) {
	if user, first, ok := wsl.WindowsUserFolder(p); ok && first != "" {
		if s := homeSyncService(first, true); s != "" {
			return s, user + "/" + first
		}
		if sharePointFolder(user, first) {
			return "SharePoint", user + "/" + first
		}
		if strings.EqualFold(first, "Pictures") {
			if second := nextPart(p, user+"/"+first); strings.EqualFold(second, "iCloud Photos") {
				return "iCloud Photos", user + "/" + first + "/" + second
			}
		}
	}
	if mount, first, ok := wsl.WindowsDriveFolder(p); ok {
		if strings.EqualFold(first, "My Drive") || strings.EqualFold(first, "Shared drives") {
			return "Google Drive", mount + "/" + first
		}
		// A sync folder moved to the top of a drive.
		if s := homeSyncService(first, true); s != "" {
			return s, mount + "/" + first
		}
	}
	for _, d := range dropboxFolders() {
		if underFold(p, d) {
			return "Dropbox", d
		}
	}
	return "", ""
}

// nextPart is the part of p right after base ("" when p isn't below it).
func nextPart(p, base string) string {
	rest, ok := strings.CutPrefix(p, base+"/")
	if !ok {
		return ""
	}
	first, _, _ := strings.Cut(rest, "/")
	return first
}

// underFold reports whether p is dir or inside it, in any case (Windows' names).
func underFold(p, dir string) bool {
	p, dir = strings.ToLower(path.Clean(p)), strings.ToLower(path.Clean(dir))
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// sharePointFolder reports whether folder name in a Windows user's folder is
// where OneDrive syncs an organization's SharePoint and Teams libraries: the
// organization's name, as in a "OneDrive - <organization>" folder beside it.
func sharePointFolder(user, name string) bool {
	ents, err := os.ReadDir(user)
	if err != nil {
		return false
	}
	for _, e := range ents {
		org, ok := cutPrefixFold(e.Name(), "OneDrive - ")
		if ok && e.IsDir() && strings.TrimSpace(org) != "" && strings.EqualFold(strings.TrimSpace(org), name) {
			return true
		}
	}
	return false
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return s, false
	}
	return s[len(prefix):], true
}

// dropboxFolders are the folders Dropbox's settings on Windows say it syncs
// (a personal and a business account's), as WSL shows them: Dropbox writes
// them to info.json in the Windows user's AppData (Local, or Roaming in older
// versions).
func dropboxFolders() []string {
	home := wsl.HereWindowsHome()
	if home == "" {
		return nil
	}
	var out []string
	for _, f := range []string{"AppData/Local/Dropbox/info.json", "AppData/Roaming/Dropbox/info.json"} {
		for _, win := range readDropboxInfo(path.Join(home, f)) {
			if lp, err := wsl.HereLinuxPath(win); err == nil && path.IsAbs(lp) && lp != "/" && !containsFold(out, lp) {
				out = append(out, lp)
			}
		}
	}
	return out
}

// readDropboxInfo reads the folders' Windows paths from a Dropbox info.json
// ({"personal": {"path": "C:\\Users\\Sam\\Dropbox", …}, "business": {…}}).
func readDropboxInfo(file string) []string {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return nil
	}
	var accounts map[string]struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(b, &accounts) != nil {
		return nil
	}
	var out []string
	for _, acc := range accounts {
		if p := strings.TrimSpace(acc.Path); len(p) >= 3 && p[1] == ':' {
			out = append(out, p)
		}
	}
	return out
}

func containsFold(xs []string, s string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
