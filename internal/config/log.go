package config

import (
	"fmt"
	"path/filepath"
)

// Log configures the activity log (`metarush log`): where the text of Claude
// conversations is kept and how long entries stay. Everything else in the log
// (operations, jobs, program runs) is always kept in the workspace database.
type Log struct {
	// Prompts says where the text of Claude prompts and responses is kept:
	// "workspace" (default: the shared database, large texts in the media store,
	// readable by everyone with access to the workspace database), "local" (only
	// in this machine's installation home) or "off" (not kept; the log still
	// records that a conversation turn happened, the tools it used and its cost).
	Prompts string `yaml:"prompts,omitempty"`
	// RetentionDays removes this machine's log entries older than this many days
	// (the daemon checks hourly). 0 or unset keeps everything until you delete it
	// (`metarush log purge`).
	RetentionDays int `yaml:"retentionDays,omitempty"`
}

// Where prompt and response text is kept (log.prompts).
const (
	PromptsWorkspace = "workspace"
	PromptsLocal     = "local"
	PromptsOff       = "off"
)

// PromptsMode is log.prompts with its default applied.
func (l Log) PromptsMode() string {
	if l.Prompts == "" {
		return PromptsWorkspace
	}
	return l.Prompts
}

func (l Log) validate() error {
	switch l.Prompts {
	case "", PromptsWorkspace, PromptsLocal, PromptsOff:
	default:
		return fmt.Errorf("log.prompts must be workspace, local or off, got %q", l.Prompts)
	}
	if l.RetentionDays < 0 {
		return fmt.Errorf("log.retentionDays must be 0 (keep everything) or a number of days, got %d", l.RetentionDays)
	}
	return nil
}

// LogSpoolDir holds log entries written while the database or media store was
// unreachable (flushed later): <home>/state/log-spool.
func (c *Config) LogSpoolDir() string { return filepath.Join(c.StateDir(), "log-spool") }

// LogBodiesDir holds prompt and response text kept only on this machine
// (log.prompts: local): <home>/state/log-bodies.
func (c *Config) LogBodiesDir() string { return filepath.Join(c.StateDir(), "log-bodies") }

// ClaudeHooksDir holds the Claude Code hook buffers (one per terminal session)
// and finished turns waiting to be logged: <home>/state/claude-hooks.
func (c *Config) ClaudeHooksDir() string { return filepath.Join(c.StateDir(), "claude-hooks") }
