package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/ops"
)

// detailsCmd is a command whose --details is on or off, for printers that
// read it (detailsMode, readyToSave).
func detailsCmd(on bool) *cobra.Command {
	c := &cobra.Command{Use: "metarush"}
	c.Flags().Bool("details", on, "")
	return c
}

// Cut sheet commands speak plainly: no IDs, paths, codes or commands unless
// --details asks for them; --json carries everything.
func TestCutOutputKeepsTechnicalDetailBehindDetails(t *testing.T) {
	path := "/Users/you/cuts/builder.cut.md"
	chk := &ops.CutCheckOut{Valid: true, BaseRevisionID: "rev_01jabcdefghjkmnpqrstvwxyz0", BaseNumber: 3, HeadRevisionID: "rev_01jabcdefghjkmnpqrstvwxyz9", HeadNumber: 4, HeadMoved: true, Rebased: true,
		Duration: 18.4, Changes: []string{"hook now ends at 0:03.88 instead of 0:06.551, 2.7 s shorter."},
		Anchors:      []cutsheet.Anchor{{ClipID: "hook", Field: "out", Phrase: "in my tiny garage", Time: 3.88, Words: "in my tiny garage,"}},
		Problems:     []cutsheet.Problem{{Severity: "warning", Code: "heavy_crop", Line: 21, Col: 4, EndCol: 8, Message: "Cropping interview-a to this shape cuts off a lot of the picture.", Hint: "Say what to keep with · focus 40% across."}},
		ConfirmToken: "ct_1792344972_833cae70be773312a48168387418a683"} // gitleaks:allow (made up for the test)
	next := func(c *cobra.Command) string { return cutApplyCommand(c, path, &sheetFlags{}, chk.ConfirmToken) }
	plainCmd := detailsCmd(false)
	plain := stdout(t, func() { printCutCheck(plainCmd, path, chk, next(plainCmd)) })
	for _, banned := range []string{"rev_", "ct_", "metarush ", "/Users/", "heavy_crop", ":21:4"} {
		if strings.Contains(plain, banned) {
			t.Errorf("plain check output shows %q:\n%s", banned, plain)
		}
	}
	for _, want := range []string{"Looks good: 1 change compared with version 3", "hook now ends at 0:03.88", "“in my tiny garage” ends hook at 0:03.88.", "line 21: Cropping", "Someone saved version 4", "Ready to save. Nothing has changed yet."} {
		if !strings.Contains(plain, want) {
			t.Errorf("plain check output lacks %q:\n%s", want, plain)
		}
	}
	detailedCmd := detailsCmd(true)
	detailed := stdout(t, func() { printCutCheck(detailedCmd, path, chk, next(detailedCmd)) })
	for _, want := range []string{path + ":21:4:", "(heavy_crop)", "next: metarush cut apply " + path + " --confirm ct_1792344972_"} {
		if !strings.Contains(detailed, want) {
			t.Errorf("--details check output lacks %q:\n%s", want, detailed)
		}
	}

	f := &ops.FolderOut{Dir: "/Users/you/cuts/builder", ProjectID: "prj_01jabcdefghjkmnpqrstvwxyz0", ProjectName: "Builder story", Branch: "main",
		Base: ops.FolderBase{RevisionID: "rev_01jabcdefghjkmnpqrstvwxyz0", Number: 3}, Files: []ops.FolderFile{{Name: "cut.md", State: "modified"}, {Name: "footage.md", State: "unchanged"}},
		Changes: []string{"hook now ends at 0:03.88."}, Message: "Ready to save as a new version.", Next: "metarush project push /Users/you/cuts/builder --confirm ct_1_abc",
		SheetURL: "http://127.0.0.1:4845/p/prj_01jabcdefghjkmnpqrstvwxyz0?rev=rev_01jabcdefghjkmnpqrstvwxyz0&tab=sheet"}
	plainFolder := stdout(t, func() { printFolder(f, false) })
	for _, banned := range []string{"prj_", "rev_", "metarush ", "/Users/", "ct_", "http"} {
		if strings.Contains(plainFolder, banned) {
			t.Errorf("plain folder output shows %q:\n%s", banned, plainFolder)
		}
	}
	if !strings.Contains(plainFolder, "builder: Builder story, version 3") || !strings.Contains(plainFolder, "cut.md modified") {
		t.Errorf("plain folder output:\n%s", plainFolder)
	}
	if d := stdout(t, func() { printFolder(f, true) }); !strings.Contains(d, "next: metarush project push") || !strings.Contains(d, "folder: /Users/you/cuts/builder") ||
		!strings.Contains(d, "in the dashboard: http://127.0.0.1:4845/p/prj_01jabcdefghjkmnpqrstvwxyz0?rev=") {
		t.Errorf("--details folder output:\n%s", d)
	}
}
