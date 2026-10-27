package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/catalog"
	"github.com/metavert-io/metarush/internal/mcp"
	"github.com/metavert-io/metarush/internal/procio"
)

func mcpCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "mcp", Short: "Use MetaRush from MCP clients (Claude Desktop, Claude Code, Codex, Cursor): its operations as tools, read-only unless you let it propose or make changes"}
	var allow bool
	var toolset string
	serve := &cobra.Command{
		Use:   "serve [--allow-changes | --propose] [--agent <name>] [--toolset core|review|edit|library|admin|all]",
		Short: "Serve MetaRush's operations to an MCP client over standard input and output (the client starts it)",
		Long: `Runs a Model Context Protocol server on standard input and output, for a client that starts it as
a local (stdio) server. Its tools reach MetaRush's operations, the same ones the command line and the
dashboard use, and every call goes to the activity log (channel "mcp"). Things only a person may do
(approving versions, deleting footage, moving the media) are never offered.

A few tools, not hundreds: by default the core (at most 12 tools), whose find, describe and run reach
every operation, beside a few everyday tools. --toolset review|edit|library|admin offers one kind of work
as direct tools instead (at most 25, related operations as one tool with a mode); --toolset all offers
one tool per operation.

Read-only by default. With --propose, the app may only propose: every change it asks for is saved
as a draft that waits in Awaiting your review (a new version stays beside the cut) until a person
approves it, says Not quite or turns it down; nothing people-only, paid or final runs. With
--allow-changes, tools that change MetaRush are offered too, and each change is confirmed: the first
call says what it would do and returns a confirm token, the second call with the token does it (edits
keep their own check: cut_check, then cut_apply). --agent names the app (default: the name the client
gives), so what it saves says which app made it.

Codex (~/.codex/config.toml):
    [mcp_servers.metarush]
    command = "metarush"
    args = ["mcp", "serve", "--propose", "--agent", "codex"]

Cursor (.cursor/mcp.json):
    {"mcpServers": {"metarush": {"command": "metarush", "args": ["mcp", "serve", "--propose", "--agent", "cursor"]}}}

Claude Code:
    claude mcp add metarush -- metarush mcp serve
    claude mcp add metarush --scope user -- metarush mcp serve --allow-changes

Claude Desktop (claude_desktop_config.json; use the full path that "which metarush" prints):
    {"mcpServers": {"metarush": {"command": "/opt/homebrew/bin/metarush", "args": ["mcp", "serve"]}}}

Add "--home", "<folder>" before "mcp" to use another installation. This is a local stdio server:
no network port, no mcp-remote bridge (that is only for HTTP servers).`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if assistantMode() {
				return fail(c, apperr.Unauthorized, "the dashboard assistant cannot start an MCP server", "")
			}
			// The protocol owns standard output; anything else printed goes to standard error.
			out, err := procio.ProtocolStdout()
			if err != nil {
				return err
			}
			defer out.Close()
			agent, proposing := agentFlags(c)
			if proposing && allow {
				return fail(c, apperr.InvalidInput, "Choose one: --propose (changes wait as drafts for a person) or --allow-changes (changes are made, each confirmed).", "")
			}
			a, err := openApp(c, "mcp")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			s, err := mcp.New(a, mcp.Options{AllowChanges: allow, Propose: proposing, Agent: agent, Toolset: toolset})
			if err != nil {
				return failErr(c, err)
			}
			mode := "read-only"
			switch {
			case proposing:
				mode = "proposing: changes are saved as drafts for a person to review"
			case allow:
				mode = "changes allowed, each one confirmed"
			}
			fmt.Fprintf(os.Stderr, "MetaRush MCP server for workspace %s: %d tools (%s), %s\n", a.Cfg.Workspace.Name, len(s.List()), toolsetName(toolset), mode)
			return s.Serve(ctx, os.Stdin, out)
		},
	}
	serve.Flags().BoolVar(&allow, "allow-changes", false, "also offer the tools that change MetaRush (each change is confirmed with a second call)")
	serve.Flags().StringVar(&toolset, "toolset", mcp.ToolsetCore, "which tools to offer: "+strings.Join(mcp.Toolsets(), ", "))
	var allowList bool
	var listSet string
	tools := &cobra.Command{
		Use:   "tools [--allow-changes | --propose] [--toolset core|review|edit|library|admin|all]",
		Short: "List the tools an MCP client sees",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			_, proposing := agentFlags(c)
			if proposing && allowList {
				return fail(c, apperr.InvalidInput, "Choose one: --propose or --allow-changes.", "")
			}
			acc := mcp.AccessOf(allowList, proposing)
			list, err := mcp.ToolListFor(listSet, acc)
			if err != nil {
				return failErr(c, err)
			}
			type row struct {
				Name     string   `json:"name"`
				Op       string   `json:"op,omitempty"`
				Ops      []string `json:"ops"`
				Modes    []string `json:"modes,omitempty"`
				Changes  bool     `json:"changes"`
				Summary  string   `json:"summary"`
				Readonly bool     `json:"readOnly"`
			}
			rows := []row{}
			for _, t := range list {
				ro, _ := t.Annotations["readOnlyHint"].(bool)
				rows = append(rows, row{Name: t.Name, Op: t.Op(), Ops: t.Ops(), Modes: t.Modes(), Changes: !ro, Readonly: ro, Summary: t.Description})
			}
			out := map[string]any{"tools": rows, "count": len(rows), "toolset": toolsetName(listSet), "readOnly": acc == mcp.ReadOnly, "access": acc,
				"instructions": mcp.InstructionsFor(listSet, acc), "size": mcp.ListSizeFor(list, listSet, acc)}
			return emit(c, out, func() {
				for _, r := range rows {
					first, _, _ := strings.Cut(r.Summary, ". ")
					if i := strings.Index(first, ". Modes — "); i > 0 {
						first = first[:i]
					}
					fmt.Printf("%-24s %s\n", r.Name, catalogShort(strings.TrimSuffix(first, ".")))
				}
				switch acc {
				case mcp.Propose:
					fmt.Printf("%d tools (%s); changes are saved as drafts for a person to review.\n", len(rows), toolsetName(listSet))
				case mcp.Changes:
					fmt.Printf("%d tools (%s); those that change MetaRush are confirmed with a second call.\n", len(rows), toolsetName(listSet))
				default:
					fmt.Printf("%d tools (%s), read-only (--allow-changes also lists the ones that change MetaRush).\n", len(rows), toolsetName(listSet))
				}
			})
		},
	}
	tools.Flags().BoolVar(&allowList, "allow-changes", false, "list the tools a server started with --allow-changes offers")
	tools.Flags().StringVar(&listSet, "toolset", mcp.ToolsetCore, "which toolset: "+strings.Join(mcp.Toolsets(), ", "))
	cmd.AddCommand(serve, tools)
	return cmd
}

func toolsetName(set string) string {
	if set == "" {
		return mcp.ToolsetCore
	}
	return set
}

func catalogShort(s string) string { return catalog.Short(s) }
