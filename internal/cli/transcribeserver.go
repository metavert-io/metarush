package cli

import (
	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/transcribe"
)

// transcribeModelServerCmd is the model server the worker starts to keep the
// speech model loaded between clips (Parakeet D93, whisper.cpp D121). It
// speaks JSON lines on stdin and stdout and is not meant to be run by hand,
// so it is hidden.
func transcribeModelServerCmd() *cobra.Command {
	var o transcribe.ServeOptions
	c := &cobra.Command{
		Use:    transcribe.ServerCommand,
		Short:  "Keep the speech model loaded for the worker (started by MetaRush itself)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return transcribe.ServeStdio(o)
		},
	}
	c.Flags().StringVar(&o.Engine, "engine", "", `the model to keep loaded: "" (Parakeet) or "`+transcribe.EngineWhisper+`"`)
	c.Flags().StringVar(&o.Library, "library", "", "the sherpa-onnx C library (Parakeet)")
	c.Flags().StringVar(&o.Server, "server", "", "whisper-server (whisper.cpp)")
	c.Flags().StringVar(&o.Model, "model", "", "the Parakeet model folder or the whisper model file")
	c.Flags().IntVar(&o.Threads, "threads", 2, "decoder threads")
	return c
}
