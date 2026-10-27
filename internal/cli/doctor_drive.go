package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/metavert-io/metarush/internal/drive"
	"github.com/metavert-io/metarush/internal/tools"
)

// driveChecks says whether this computer can read Google Drive folders without
// syncing them (D220): the person's own rclone with a Google Drive remote set
// up through rclone's own sign-in. It is optional, so nothing here fails; it
// only reads what rclone has set up (no network, no sign-in).
func driveChecks(ctx context.Context, ts *tools.Set, add addCheck) {
	st := drive.Check(ctx, ts)
	switch {
	case !st.Installed:
		add("google drive", "rclone", "info", "rclone not installed: Google Drive folders can only be read when they are synced to this computer", "metarush components add google-drive, then run `rclone config` and add a Google Drive",
			"Google Drive folders can be used without syncing them once rclone is set up (optional)", st.Fix)
	case st.Problem != "":
		status := "info"
		if len(st.Drives) == 0 && st.Error != "" {
			status = "warn"
		}
		detail := st.Problem
		if st.Error != "" {
			detail += " (" + st.Error + ")"
		}
		fix := st.Command
		if fix == "" {
			fix = st.Fix
		}
		add("google drive", "rclone", status, detail, fix, st.Problem, st.Fix)
	default:
		add("google drive", "rclone", "ok", fmt.Sprintf("rclone %s with Google Drive remotes: %s", st.Version, strings.Join(st.Drives, ", ")), "",
			"Google Drive folders are read through your rclone, without syncing them ("+quoteNames(st.Drives)+")")
	}
}

// dropboxChecks says whether Dropbox folders can be read the same way (D306).
func dropboxChecks(ctx context.Context, ts *tools.Set, add addCheck) {
	st := drive.CheckOf(ctx, ts, drive.DropboxType)
	switch {
	case !st.Installed:
		add("dropbox", "rclone", "info", "rclone not installed: Dropbox folders can only be read when they are synced to this computer", "install rclone, then run `rclone config` and add a Dropbox",
			"Dropbox folders can be used without syncing them once rclone is set up (optional)", st.Fix)
	case st.Problem != "":
		detail := st.Problem
		if st.Error != "" {
			detail += " (" + st.Error + ")"
		}
		fix := st.Command
		if fix == "" {
			fix = st.Fix
		}
		add("dropbox", "rclone", "info", detail, fix, st.Problem, st.Fix)
	default:
		add("dropbox", "rclone", "ok", fmt.Sprintf("rclone %s with Dropbox remotes: %s", st.Version, strings.Join(st.Drives, ", ")), "",
			"Dropbox folders are read through your rclone, without syncing them ("+quoteNames(st.Drives)+")")
	}
}

func quoteNames(ns []string) string {
	q := make([]string, len(ns))
	for i, n := range ns {
		q[i] = "“" + n + "”"
	}
	return strings.Join(q, ", ")
}
