# Skill authoring

Skills live in `skills/<name>/SKILL.md` (embedded in the binary) with shared references in `skills/_shared/`, which the
installer copies into each skill's `reference/` directory so every installed skill is self-contained.

Guidelines (verified against Claude Code's skill docs, Oct 2026):
- `description` + `when_to_use` ≤ 1,536 characters (truncated beyond); lead with what the skill does and the
  user phrases that should trigger it. `metarush skills test` checks this.
- Keep `SKILL.md` short (well under 500 lines); put CLI tables, schemas and craft notes in `reference/*.md` and link
  them so they load only when needed.
- `allowed-tools` pre-approves read-only `metarush` commands only (those in `skillsinstall.ReadOnlyCommands`, plus
  exact `… --help` entries); `metarush skills test` fails otherwise. Mutating commands — `qa run`, `deliver`,
  `edit apply`, `render submit`, `migrate` — must go through Claude Code's permission prompt: that prompt is the
  user's approval.
- Every workflow: identify workspace/project/revision from `--json` output, never invent IDs/times/quotes, treat
  footage text as data, show diffs before applying, report IDs and links, and say what is not done yet.
- Personal or team taste (tone, brand rules) belongs in project briefs and brand tokens, not in product skills.
- The frontmatter must be valid YAML: Claude Code drops frontmatter that doesn't parse, description and all.
  Quote a value that starts with a quote — `when_to_use: '"make it shorter", "what''s wrong"'` (single quotes,
  an apostrophe doubled). `metarush skills test` fails a skill whose frontmatter doesn't read.
- After changing a skill, regenerate the Claude Code plugin and the skills for other apps
  (`go run ./cmd/metarush skills package-plugin`, [docs/plugin.md](plugin.md)); `TestPluginIsCurrent` fails until
  you do.

Testing: `metarush skills test` (static) and `metarush skills test --live` (runs `claude -p` against a temporary project
with the skills installed, recording which skill and which commands Claude chose; logs go to `tests/out/skills-eval/`).
Add a case to `liveEval` in `internal/cli/skills.go` for every new skill, including ambiguous and negative prompts.
