package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/plainwords"
)

// theirs marks people's own words in plain output — a clip's title or file
// name, a person's, project's or resource's name — with curly quotes, the mark
// the plain-words guards read as theirs (D113): such words may look like
// anything a code does (riverside_sam_raw-video-cfr_garden_show_0002), and the
// voice guide quotes them anyway.
func theirs(s string) string { return plainwords.TheirsOpen + s + plainwords.TheirsClose }

// theirsCol is theirs for a column n characters wide: shortened inside the marks.
func theirsCol(s string, n int) string { return theirs(trunc(s, max(1, n-2))) }

// exitError carries an already-printed error and the process exit code.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

func jsonMode(c *cobra.Command) bool {
	v, _ := c.Flags().GetBool("json")
	return v
}

// detailsMode reports whether the person asked for the technical detail
// (IDs, paths, codes, the exact commands) with --details. Plain output leaves
// it out (skills/_shared/voice.md); --json always has everything.
func detailsMode(c *cobra.Command) bool {
	v, _ := c.Flags().GetBool("details")
	return v
}

// emit prints v as JSON in --json mode, otherwise calls human.
func emit(c *cobra.Command, v any, human func()) error {
	if jsonMode(c) {
		enc := json.NewEncoder(os.Stdout)
		if !assistantMode() {
			// People read it indented; Claude reads it on one line: the same
			// data, about a third fewer tokens in its memory (D181).
			enc.SetIndent("", "  ")
		}
		return enc.Encode(map[string]any{"ok": true, "data": v})
	}
	human()
	return nil
}

// readyToSave ends a check that a person still has to confirm: plain output
// says nothing has changed yet; --details gives the exact command (with its
// confirm token) that saves it.
func readyToSave(c *cobra.Command, next string) {
	fmt.Println("Ready to save. Nothing has changed yet.")
	if next == "" {
		return
	}
	if detailsMode(c) {
		fmt.Println("next: " + next)
	} else {
		fmt.Println("(Add --details to see the command that saves it.)")
	}
}

// fail prints a structured error and returns an exit error.
func fail(c *cobra.Command, code, msg, hint string) error {
	return failErr(c, apperr.New(code, "%s", msg).WithHint(hint))
}

// failErr prints an error for people: what happened and what to do next.
// The code, the exact command and the structured details are technical: they
// are printed with --details, and --json carries them as one document.
func failErr(c *cobra.Command, err error) error {
	ae := apperr.From(err)
	if jsonMode(c) {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"ok": false, "error": ae})
		return &exitError{code: exitCodeFor(ae.Code)}
	}
	msg := sentence(ae.Message)
	raw := ae.Code == apperr.Internal && ae.Unwrap() != nil && ae.Message == ae.Unwrap().Error()
	if msg == "" || raw {
		// An unexpected error from deep inside: its text is for whoever debugs it.
		msg = "Something went wrong, and MetaRush couldn't finish."
	}
	fmt.Fprintln(os.Stderr, msg)
	if h := sentence(ae.Hint); h != "" {
		fmt.Fprintln(os.Stderr, h)
	}
	if detailsMode(c) {
		fmt.Fprintf(os.Stderr, "code: %s\n", ae.Code)
		if cmd := ae.Command(); cmd != "" {
			fmt.Fprintf(os.Stderr, "command: %s\n", cmd)
		}
		if ae.Details != nil {
			b, _ := json.MarshalIndent(ae.Details, "", "  ")
			fmt.Fprintf(os.Stderr, "details: %s\n", b)
		}
		if cause := ae.Unwrap(); cause != nil {
			fmt.Fprintf(os.Stderr, "cause: %v\n", cause)
		}
	} else if raw {
		fmt.Fprintln(os.Stderr, "(Add --details to see the error.)")
	} else if ae.Command() != "" {
		fmt.Fprintln(os.Stderr, "(Add --details to see the command that fixes it.)")
	}
	return &exitError{code: exitCodeFor(ae.Code)}
}

// sentence starts a message with a capital letter and ends it with a stop, so
// errors read like the rest of MetaRush ("render not found" → "Render not found.").
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if r := []rune(s); r[0] >= 'a' && r[0] <= 'z' {
		s = strings.ToUpper(string(r[0])) + string(r[1:])
	}
	if !strings.ContainsAny(s[len(s)-1:], ".?!…:)”\"") {
		s += "."
	}
	return s
}

func exitCodeFor(code string) int {
	switch code {
	case apperr.InvalidInput, apperr.Validation, apperr.UnsafeSecretPath:
		return 2
	case apperr.NotFound:
		return 3
	case apperr.Conflict, apperr.RevisionConflict, apperr.Locked, apperr.Held:
		return 4
	case apperr.ConfirmRequired, apperr.HumanOnly, apperr.Unauthorized, apperr.ConsentRequired, apperr.Budget:
		return 5
	case apperr.NotConfigured, apperr.ToolMissing, apperr.NoWorker:
		return 6
	default:
		return 1
	}
}
