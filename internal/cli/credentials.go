package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/secretpath"
)

// Content Credentials (D415–D424, docs/content-credentials.md): `metarush
// credentials` says whether delivered videos carry them and whether this
// computer can sign; `setup` (an owner, a person at their own terminal) makes
// a test certificate or takes a certificate and its key from files, read
// once; `test` signs a sample; `check <file>` reads a video's; `remove`; and
// `on|off|required` is the workspace's choice (`content credentials` a
// client's Content Settings').

func credentialsCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "credentials",
		Short: "Content Credentials on delivered videos: whether they are on and whether this computer can sign them (status, setup, test, check, remove, on|off|required)",
		Long: `Content Credentials are a signed record inside a delivered video, saying it was made with MetaRush,
from which footage (by fingerprint) and which parts AI made or changed. Apps and sites that read them show
who signed it. They are off until an owner turns them on; each computer that delivers needs its own signing
certificate, which an owner sets up there.

  metarush credentials                                   # whether they are on, and this computer's certificate
  metarush credentials setup --test [--organisation "Example Studio"]   # a test certificate (verifiers show it as unknown)
  metarush credentials setup --cert chain.pem --key key.pem             # your own, from a certificate authority
  metarush credentials test                              # sign a one-second sample and check it
  metarush credentials on | required | off               # owners: what every final delivery does
  metarush credentials check video.mp4                   # what a video's Content Credentials say`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "credentials.status", ops.CredentialsStatusIn{ProjectID: project}, func(o any) { printCredentialsStatus(c, o.(*ops.CredentialsStatusOut)) })
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "also what a project's Content Settings say")
	status := &cobra.Command{
		Use:   "status [--project <p>]",
		Short: "Whether delivered videos carry Content Credentials and whether this computer can sign them",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "credentials.status", ops.CredentialsStatusIn{ProjectID: project}, func(o any) { printCredentialsStatus(c, o.(*ops.CredentialsStatusOut)) })
		},
	}
	status.Flags().StringVar(&project, "project", "", "also what a project's Content Settings say")
	cmd.AddCommand(status, credentialsSetupCmd(), credentialsTestCmd(), credentialsCheckCmd(), credentialsRemoveCmd())
	for _, mode := range []string{models.CredentialsOn, models.CredentialsRequired, models.CredentialsOff} {
		cmd.AddCommand(credentialsModeCmd(mode))
	}
	return cmd
}

func printCredentialsStatus(c *cobra.Command, o *ops.CredentialsStatusOut) {
	for _, l := range o.Lines {
		fmt.Println(l)
	}
	h := o.Here
	if h.Certificate != nil && !strings.Contains(o.Summary, h.Says) {
		fmt.Println(h.Says)
	}
	for _, p := range h.Problems {
		if !strings.Contains(h.Says, strings.TrimSuffix(p, ".")) {
			fmt.Println("  • " + p)
		}
	}
	if o.Workspace != nil && o.Workspace.TimestampURL != "" {
		fmt.Println("A timestamp service vouches for when each video is signed (it receives a fingerprint, never the video).")
	}
	if detailsMode(c) {
		if f := h.Certificate; f != nil {
			fmt.Printf("  certificate: %s, issued by %s, %s, valid %s to %s, fingerprint %s\n", f.Name, f.Issuer, f.Alg, f.ValidFrom.Format("2006-01-02"), f.ValidUntil.Format("2006-01-02"), f.Fingerprint)
			fmt.Printf("  key kept in: %s on %s\n", h.Kept, h.Machine)
		}
		if h.Tool != "" {
			fmt.Printf("  c2patool %s\n", h.Tool)
		}
		if o.Workspace != nil && o.Workspace.TimestampURL != "" {
			fmt.Printf("  timestamp service: %s\n", o.Workspace.TimestampURL)
		}
	}
}

// credentialsAtTerminal: setting up and removing a signing certificate are a
// person's, at their own terminal (D417): never Claude Code or another AI
// app, never a script nobody watches.
func credentialsAtTerminal(c *cobra.Command, what string) error {
	if agentAtTheKeys(c) || !atTerminal() {
		return failErr(c, apperr.New(apperr.HumanOnly, "Only a person %s this computer's signing certificate, at their own terminal: Claude, Auto and AI apps never do.", what).
			WithHint("Run the command yourself in a terminal, or use Settings → Delivery in the dashboard."))
	}
	return nil
}

// readPEMFile reads a certificate or key file a person named (at most 64 KB).
func readPEMFile(path, what string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", apperr.New(apperr.NotFound, "MetaRush can't read the %s file.", what).WithDetails(map[string]any{"path": path})
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if err != nil {
		return "", err
	}
	if len(b) > 64<<10 {
		return "", apperr.New(apperr.InvalidInput, "The %s file is larger than a %s can be.", what, what)
	}
	return string(b), nil
}

// keyFileSafe refuses a private key that a sync folder uploads or git would
// commit, as other secrets are (internal/secretpath): it may already be out,
// so the person gets a new one rather than signing with it.
func keyFileSafe(path string) error {
	switch v := secretpath.Inspect(path); {
	case v.Kind == secretpath.KindSync:
		return apperr.New(apperr.InvalidInput, "The private key is in a folder %s uploads, so MetaRush won't take it from there.", v.Service).
			WithHint("It may already have been uploaded: ask your certificate authority for a new certificate and key, keep the key outside synced folders, and set it up from there.").
			WithDetails(map[string]any{"path": v.Path})
	case v.Kind == secretpath.KindGit && !v.Ignored:
		return apperr.New(apperr.InvalidInput, "The private key is in a git repository that would commit it, so MetaRush won't take it from there.").
			WithHint("Move it out of the repository (if it was ever committed, ask your certificate authority for a new certificate and key), then set it up from there.").
			WithDetails(map[string]any{"path": v.Path})
	}
	return nil
}

func credentialsSetupCmd() *cobra.Command {
	var in ops.CredentialsSetupIn
	var cert, key string
	cmd := &cobra.Command{
		Use:   "setup (--test [--organisation <name>] | --cert <chain.pem> --key <key.pem>) [--replace]",
		Short: "Owners, people only: set up this computer's signing certificate (a test one made here, or yours from PEM files; the key goes into this computer's keychain)",
		Long: `Sets up the certificate this computer signs Content Credentials with. Only an owner, at their own terminal.

  --test                 a test certificate made here, valid for a year. Verifiers show its signer as unknown:
                         it is for trying Content Credentials, never for a client. --organisation puts a name
                         in it (your organisation's, nobody else's); without it, no name goes in.
  --cert/--key           your own certificate chain (the signing certificate first, then the ones that issued it)
                         and its private key, PEM, without a password: read once, the key kept in this computer's
                         keychain and never shown again. Verifiers show who signed: the organisation it names.

Each computer that delivers needs its own. A sample is signed and checked right after when c2patool is here.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := credentialsAtTerminal(c, "sets up"); err != nil {
				return err
			}
			if cert != "" || key != "" {
				if cert == "" || key == "" {
					return fail(c, apperr.InvalidInput, "Give both --cert and --key.", "")
				}
				var err error
				if in.Certificate, err = readPEMFile(cert, "certificate"); err != nil {
					return failErr(c, err)
				}
				if err := keyFileSafe(key); err != nil {
					return failErr(c, err)
				}
				if in.PrivateKey, err = readPEMFile(key, "private key"); err != nil {
					return failErr(c, err)
				}
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "credentials.setup", in, ops.Caller{Human: true}, func(o any) {
				out := o.(*ops.CredentialsSetupOut)
				fmt.Println(out.Summary)
				if key != "" {
					fmt.Printf("MetaRush no longer needs %s: keep it somewhere safe, or delete it.\n", filepath.Base(key))
				}
			})
		},
	}
	cmd.Flags().BoolVar(&in.Test, "test", false, "make a test certificate on this computer (verifiers show its signer as unknown)")
	cmd.Flags().StringVar(&in.Organisation, "organisation", "", "the name a test certificate is for (your organisation's; default none)")
	cmd.Flags().StringVar(&cert, "cert", "", "your certificate chain, PEM (the signing certificate first)")
	cmd.Flags().StringVar(&key, "key", "", "its private key, PEM, without a password")
	cmd.Flags().BoolVar(&in.Replace, "replace", false, "replace the certificate this computer has")
	return cmd
}

func credentialsTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test",
		Short: "Sign a one-second sample with this computer's certificate and check it: whether Content Credentials work here",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "credentials.test", struct{}{}, func(o any) {
				out := o.(*ops.CredentialsTestOut)
				fmt.Println(out.Summary)
				if detailsMode(c) && out.ManifestHash != "" {
					fmt.Printf("  manifest %s, c2patool %s\n", out.ManifestHash, out.Tool)
				}
			})
		},
	}
}

func credentialsCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check <video>",
		Short: "What a video's Content Credentials say: whether it carries them, whether they check out, who signed",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "credentials.check", ops.CredentialsCheckIn{Path: args[0]}, func(o any) {
				out := o.(*ops.CredentialsCheckOut)
				fmt.Println(out.Summary)
				if out.Carries {
					if out.Generator != "" {
						fmt.Printf("Made with %s, from %s.\n", out.Generator, pluralCount(out.Parts, "part", "parts"))
					}
				}
				if detailsMode(c) {
					if out.ManifestHash != "" {
						fmt.Printf("  manifest %s\n", out.ManifestHash)
					}
					for _, s := range out.SourceTypes {
						fmt.Printf("  source type %s\n", s)
					}
				}
			})
		},
	}
}

func pluralCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func credentialsRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove",
		Short: "Owners, people only: remove this computer's signing certificate and its key",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := credentialsAtTerminal(c, "removes"); err != nil {
				return err
			}
			if interactive() && !askYes("Remove this computer's signing certificate? Videos delivered here then go out without Content Credentials", false) {
				fmt.Println("Nothing was removed.")
				return nil
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "credentials.remove", struct{}{}, ops.Caller{Human: true}, func(o any) {
				fmt.Println("This computer's signing certificate and its key are removed.")
				printCredentialsStatus(c, o.(*ops.CredentialsStatusOut))
			})
		},
	}
}

func credentialsModeCmd(mode string) *cobra.Command {
	var url string
	var noTimestamp bool
	short := map[string]string{
		models.CredentialsOn:       "Owners: every final video MetaRush delivers carries Content Credentials (one that can't be signed goes out without them, and says why)",
		models.CredentialsRequired: "Owners: every final video carries Content Credentials, and a version this computer can't sign waits",
		models.CredentialsOff:      "Owners, people only: delivered videos go out without Content Credentials",
	}[mode]
	cmd := &cobra.Command{
		Use:   mode + " [--timestamp-url <url> | --no-timestamp]",
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			in := ops.CredentialsSetIn{Mode: mode}
			switch {
			case noTimestamp && url != "":
				return fail(c, apperr.InvalidInput, "Give --timestamp-url or --no-timestamp, not both.", "")
			case noTimestamp:
				empty := ""
				in.TimestampURL = &empty
			case url != "":
				in.TimestampURL = &url
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "credentials.set", in, ops.Caller{Human: interactive() && !agentAtTheKeys(c)}, func(o any) { printCredentialsStatus(c, o.(*ops.CredentialsStatusOut)) })
		},
	}
	cmd.Flags().StringVar(&url, "timestamp-url", "", "a timestamp service (RFC 3161, an https address on the internet) that vouches for when each video is signed; it receives a fingerprint, never the video")
	cmd.Flags().BoolVar(&noTimestamp, "no-timestamp", false, "no timestamp service")
	return cmd
}

func contentCredentialsCmd() *cobra.Command {
	var in ops.ContentCredentialsSetIn
	cmd := &cobra.Command{
		Use:   "credentials <on|required|off|workspace> [--project <project>]",
		Short: "Owners: whether the delivered videos of every project using a project's own settings carry Content Credentials (on, required, off; asking less is people only) or follow the workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Mode = args[0]
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "content.credentials.set", in, ops.Caller{Human: interactive() && !agentAtTheKeys(c)}, func(o any) { printCredentialsStatus(c, o.(*ops.CredentialsStatusOut)) })
		},
	}
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "a project with its own settings (default: the default's)")
	return cmd
}
