// Package cli is the metarush command line. Every command that touches shared
// state goes through the ops registry, so the CLI, the dashboard API and skills
// share one implementation and one schema.
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/activity"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/intake"
	"github.com/metavert-io/metarush/internal/sandbox"
	"github.com/metavert-io/metarush/internal/uploads"
	"github.com/metavert-io/metarush/internal/version"
)

var homeFlag string

// Execute runs the CLI and exits with a meaningful code.
func Execute() {
	// Inside a Linux sandbox with MetaRush's network (D232) this binary is the
	// forwarder in front of the ability's program: no settings, no database,
	// no command line of its own.
	if len(os.Args) > 1 && os.Args[1] == sandbox.ForwardCommand {
		os.Exit(sandbox.ForwardMain(os.Args[2:]))
	}
	root := &cobra.Command{
		Use:           "metarush",
		Short:         "MetaRush — a skills-first, AI-native video editing harness",
		Long:          "MetaRush turns raw footage into structured, reproducible cuts. Claude Code skills drive this CLI; the local dashboard shows the same shared state.\n\nAdd --json to any command for stable machine-readable output.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.String(),
		// The dashboard assistant's shim may only run read-only commands.
		PersistentPreRunE: func(c *cobra.Command, _ []string) error { return assistantGuard(c) },
	}
	root.PersistentFlags().StringVar(&homeFlag, "home", "", "installation home (default $METARUSH_HOME or ~/.metarush)")
	root.PersistentFlags().Bool("json", false, "machine-readable JSON output")
	root.PersistentFlags().Bool("details", false, "also show the technical detail: IDs, file paths, codes and the exact commands")
	root.PersistentFlags().String("agent", "", "the AI app running this command (codex, cursor…; default $METARUSH_AGENT): what it saves says which app made it")
	root.PersistentFlags().Bool("propose", false, "only propose (default on when $METARUSH_PROPOSE is set): changes are saved as drafts for a person to review, nothing people-only runs")
	for _, c := range commands() {
		root.AddCommand(c)
	}
	if cmd, err := root.ExecuteC(); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		// A command typed wrong (an unknown flag, a missing argument): say so
		// and where its help is.
		fmt.Fprintln(os.Stderr, sentence(err.Error()))
		if cmd != nil {
			fmt.Fprintf(os.Stderr, "%s --help shows what it takes.\n", cmd.CommandPath())
		}
		os.Exit(1)
	}
}

func commands() []*cobra.Command {
	return []*cobra.Command{initCmd(), doctorCmd(), dashboardCmd(), serveCmd(), stopCmd(), workerCmd(), migrateCmd(),
		projectCmd(), assetCmd(), ingestCmd(), importCmd(), extensionCmd(), footageCmd(), editCmd(), cutCmd(), revisionCmd(), renderCmd(), qaCmd(), deliverCmd(), jobCmd(), commentCmd(),
		workspaceCmd(), machineCmd(), providersCmd(), voiceoverCmd(), mixCmd(), soundCmd(), conversationCmd(), recordingCmd(), cacheCmd(), syncCmd(), opsCmd(), schemaCmd(), skillsCmd(), assistantCmd(), transcriptAliasCmd(), versionCmd(),
		activityCmd(), notificationsCmd(), searchCmd(), watchCmd(), auditCmd(), previewCmd(), peopleCmd(), todosCmd(),
		logCmd(), improveCmd(), motionCmd(), remotionCmd(), rendererCmd(), serviceCmd(), resourcesCmd(), addCmd(), brandCmd(), contentCmd(), keepCmd(),
		exportCmd(), storageCmd(), mcpCmd(), draftCmd(), transcribeModelServerCmd(), databaseCmd(), setupCommand(),
		libraryCmd(), collectionCmd(), trashCmd(), remoteCmd(), generateCmd(), aiCmd(), presenterCmd(), looksCmd(), consentCmd(), captionsCmd(), authCmd(), usersCmd(), accountCmd(), recipeCmd(), abilityCmd(), catalogueCmd(), publishCmd(), shareCmd(), componentsCmd()} // recipeCmd: Recipes
}

// Ensure op packages register.
var (
	_ = ingest.Progress
	_ = activity.Build
	_ = uploads.Precheck
	_ = intake.RouteLink
)
