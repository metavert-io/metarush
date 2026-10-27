# DaVinci Resolve through its MCP server — not yet runnable

What it will do: let Claude work in DaVinci Resolve Studio on this computer — make a project, bring footage into
the media pool, build and change timelines, add markers, start renders — through the community DaVinci Resolve MCP
server, with a person starting each step, because each one changes another app.

**Why it doesn't run yet**: `metarush ability check` refuses its manifest, because three things it needs
aren't in MetaRush yet:

1. **An ability that is an MCP server** (`serves: mcp`): today a run starts a program, waits for it to end and takes
   its files. An MCP server stays running for a conversation and answers tool calls. MetaRush would start it in the
   sandbox, offer its tools through `find` and `run_change` (never as tools of their own), record every call,
   ask a person for each change (never Auto: `changes-outside`), and stop it when the conversation is idle.
2. **The tools it offers** (`tools:`), each with what it changes, so an owner approves them and a person sees the
   step before it runs.
3. **Reaching a running app** (`app:`): the server loads Resolve's scripting library (`fusionscript.so`, inside the
   app) into its own process, and that library talks to the running Resolve over the app's own local connection.
   That isn't HTTP, so the one local service an owner can allow can't carry it; the sandbox would also have to
   let it read the app's library. And the server is a Python project with its own environment (`resolve-mcp`),
   where an ability's program today is one file pinned by its fingerprint.

- **The server's licence:** MIT (github.com/samuelgursky/davinci-resolve-mcp). It runs beside MetaRush and is never
  bundled.
- **Resolve:** driving Resolve from another program (external scripting) is a **DaVinci Resolve Studio** feature (the
  paid version), turned on in Resolve under Preferences → General → External scripting using: Local. The free
  Resolve can't be driven from outside.
- **Installing the server:** its own setup (`npx davinci-resolve-mcp setup`, which makes a Python environment with its
  packages) or a `git clone` and its `install.py`; it needs Python 3.10–3.12.
- **Cost:** free (Resolve Studio is a paid app). **Key:** none. **What leaves this computer:** nothing.
  **Who starts a run:** a person, for every step.
