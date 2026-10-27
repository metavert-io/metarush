"""Checks `metarush mcp serve` with the official MCP Python SDK (scripts/mcp-check.sh).

Connects over stdio the way Claude Desktop and Claude Code do, then checks the
core (the default: at most 12 tools; find, describe and run), one toolset (at
most 25 tools, some with a mode) and every operation as a tool (--toolset all):
the handshake, the tool lists (names clients accept, input schemas that are
valid JSON Schema, nothing only a person may do, read-only by default), reads,
a refused people-only operation, refused changes on a read-only connection and,
with --changes, the two-step confirmation of a change through run and through
an operation's own tool on servers started with --allow-changes, and a
propose-only connection (--propose, D248) saving a draft.
"""

import argparse
import asyncio
import json
import os
import re
import sys
import time

from jsonschema import Draft202012Validator
from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client

NAME = re.compile(r"^[a-zA-Z0-9_-]{1,64}$")
PEOPLE_ONLY = {"revision_approve", "asset_delete", "storage_migrate", "motion_approve", "log_purge"}

failures = []
checks = 0


def check(ok, what):
    global checks
    checks += 1
    if not ok:
        failures.append(what)
        print("FAIL", what)


def get(obj, snake, camel):
    """A field by its SDK name: snake_case (SDK 2.x) or camelCase (1.x)."""
    return getattr(obj, snake) if hasattr(obj, snake) else getattr(obj, camel)


def text_of(result):
    return "".join(getattr(c, "text", "") for c in result.content)


def is_error(result):
    return bool(get(result, "is_error", "isError"))


def structured(result):
    return get(result, "structured_content", "structuredContent")


def read_only_hint(tool):
    return None if tool.annotations is None else get(tool.annotations, "read_only_hint", "readOnlyHint")


def session(cmd, home, extra):
    args = (["--home", home] if home else []) + ["mcp", "serve"] + extra
    # Clients start servers with a small environment; an installation that keeps
    # its secrets in METARUSH_* variables needs them passed on (the client's "env").
    return stdio_client(StdioServerParameters(command=cmd, args=args, env=dict(os.environ)))


async def tool_list(s, most, read_only_only):
    """Lists the tools and checks each: a name clients accept, an input schema
    that is valid JSON Schema of an object, nothing only a person may do."""
    tools = (await s.list_tools()).tools
    names = {t.name for t in tools}
    if most:
        check(len(tools) <= most, f"at most {most} tools ({len(tools)})")
    for t in tools:
        check(NAME.match(t.name) is not None, f"tool name {t.name!r} is one clients accept")
        schema = get(t, "input_schema", "inputSchema")
        try:
            Draft202012Validator.check_schema(schema)
            valid = schema.get("type") == "object"
        except Exception as e:  # noqa: BLE001
            valid = False
            print("  schema problem in", t.name, e)
        check(valid, f"{t.name}: its input schema is valid JSON Schema of an object")
        if read_only_only:
            check(read_only_hint(t) is True, f"{t.name} reads only")
    check(not (names & PEOPLE_ONLY), "nothing only a person may do is offered")
    return {t.name: t for t in tools}


async def core(cmd, home):
    """The default list (D222): find, describe and run reach every operation."""
    async with session(cmd, home, []) as (read, write):
        async with ClientSession(read, write) as s:
            init = await s.initialize()
            info = get(init, "server_info", "serverInfo")
            check(info.name == "metarush", "serverInfo.name is metarush")
            check(bool(init.capabilities.tools), "the server offers tools")
            instructions = init.instructions or ""
            check("read-only" in instructions and "find" in instructions, "the instructions say read-only and name find")
            check(len(instructions.split()) <= 150, f"the instructions take at most 150 words ({len(instructions.split())})")
            print(f"protocol {get(init, 'protocol_version', 'protocolVersion')}, server {info.name} {info.version}")
            tools = await tool_list(s, 12, True)
            check({"find", "describe", "run", "project_list", "search", "cut_show"} <= set(tools), "the core: find, describe, run and everyday tools")
            check("project_create" not in tools, "changes are not offered read-only")

            r = await s.call_tool("find", {"task": "make me a review copy I can watch"})
            first = text_of(r).splitlines()[0] if text_of(r) else ""
            check(not is_error(r) and first.startswith("render.submit"), f"find puts the render first ({first!r})")
            r = await s.call_tool("describe", {"name": "project.show"})
            sc = structured(r) or {}
            check(not is_error(r) and "input" in sc and "example" in sc, "describe gives the input and an example")
            r = await s.call_tool("run", {"name": "project.list"})
            check(not is_error(r) and "projects" in text_of(r), "run project.list answers")
            r = await s.call_tool("run", {"name": "revision.approve", "input": {"id": "rev_check"}})
            check(is_error(r) and "E_HUMAN_ONLY" in text_of(r), "run revision.approve is refused: people only")
            r = await s.call_tool("run", {"name": "project.create", "input": {"name": "MCP check"}})
            check(is_error(r) and "E_UNAUTHORIZED" in text_of(r), "run project.create is refused on a read-only connection")
            r = await s.call_tool("project_create", {"name": "MCP check"})
            check(is_error(r) and "E_UNAUTHORIZED" in text_of(r), "project_create by its name is refused on a read-only connection")
            print(f"core, read-only: {len(tools)} tools")


async def toolset(cmd, home, name):
    """A toolset: one kind of work, related operations as one tool with a mode."""
    async with session(cmd, home, ["--toolset", name]) as (read, write):
        async with ClientSession(read, write) as s:
            await s.initialize()
            tools = await tool_list(s, 25, True)
            moded = [t for t in tools.values() if "mode" in (get(t, "input_schema", "inputSchema").get("properties") or {})]
            check(len(moded) > 0, "some tools take a mode")
            if "project" in tools:
                r = await s.call_tool("project", {"mode": "list"})
                check(not is_error(r) and "projects" in text_of(r), "project with mode list answers")
                r = await s.call_tool("project", {"mode": "nothing"})
                check(is_error(r) and "E_INVALID_INPUT" in text_of(r), "an unknown mode is refused")
            print(f"toolset {name}, read-only: {len(tools)} tools, {len(moded)} with modes")


async def every_operation(cmd, home):
    """--toolset all: one tool per operation, as before."""
    async with session(cmd, home, ["--toolset", "all"]) as (read, write):
        async with ClientSession(read, write) as s:
            await s.initialize()
            tools = await tool_list(s, 0, True)
            names = set(tools)
            check(len(tools) > 100, f"over 100 read-only tools ({len(tools)})")
            check("project_list" in names and "project_create" not in names, "reads offered, changes not")
            # Resources (D104-D106): reading a repository's copy and a resource is offered; fetching and making are changes.
            check({"resources_list", "resources_show", "resources_path"} <= names, "resources can be read")
            check(not ({"resources_fetch", "resources_make", "resources_refresh"} & names), "fetching and making resources are not offered read-only")
            r = await s.call_tool("project_list", {})
            check(not is_error(r) and "projects" in text_of(r), "project_list answers")
            sc = structured(r)
            check(sc is not None and "projects" in sc, "project_list has structured content")
            r = await s.call_tool("revision_approve", {"id": "rev_check"})
            check(is_error(r) and "E_HUMAN_ONLY" in text_of(r), "revision_approve is refused: people only")
            print(f"every operation, read-only: {len(tools)} tools")


def token_of(result):
    token = (structured(result) or {}).get("confirmToken")
    if token is None:
        m = re.search(r'"(mcp1\.[0-9]+\.[0-9a-f]+)"', text_of(result))
        token = m.group(1) if m else None
    return token


def changes_apart(tools, where):
    """No tool both reads and changes (D222): a changing tool is run_change, one
    named …_change or an operation's own changing tool; run only reads."""
    for n, t in tools.items():
        if read_only_hint(t) is False:
            check(n == "run_change" or n.endswith("_change") or n == "render_submit", f"{where}: {n} changes things and says so in its name")
    check(read_only_hint(tools["run"]) is True, f"{where}: run only reads")
    check(read_only_hint(tools["run_change"]) is False, f"{where}: run_change changes things")


async def changes(cmd, home, toolset_name):
    async with session(cmd, home, ["--allow-changes"]) as (read, write):
        async with ClientSession(read, write) as s:
            await s.initialize()
            tools = await tool_list(s, 12, False)
            changes_apart(tools, "core")
            name = f"MCP check {int(time.time())}"
            r = await s.call_tool("run", {"name": "project.create", "input": {"name": name}})
            check(is_error(r) and "E_UNAUTHORIZED" in text_of(r) and "run_change" in text_of(r), "run refuses a change, even with changes allowed")
            first = await s.call_tool("run_change", {"name": "project.create", "input": {"name": name}})
            token = token_of(first)
            check(is_error(first) and "E_CONFIRM_REQUIRED" in text_of(first) and bool(token), "run_change: the first call asks for confirmation")
            done = await s.call_tool("run_change", {"name": "project.create", "input": {"name": name}, "confirmToken": token})
            check(not is_error(done) and name in text_of(done), "run_change: the second call with the token does it")
            print(f"core with changes: {len(tools)} tools; created {name!r} in two steps")
    async with session(cmd, home, ["--allow-changes", "--toolset", toolset_name]) as (read, write):
        async with ClientSession(read, write) as s:
            await s.initialize()
            tools = await tool_list(s, 25, False)
            changes_apart(tools, f"toolset {toolset_name}")
            print(f"toolset {toolset_name} with changes: {len(tools)} tools, reading and changing apart")
    async with session(cmd, home, ["--allow-changes", "--toolset", "all"]) as (read, write):
        async with ClientSession(read, write) as s:
            await s.initialize()
            tools = await tool_list(s, 0, False)
            check("project_create" in tools and read_only_hint(tools["project_create"]) is False, "changes are offered")
            check(len(tools) > 200, f"over 200 tools with changes ({len(tools)})")
            check(all(n in tools and read_only_hint(tools[n]) is False for n in ("resources_fetch", "resources_make", "resources_refresh")),
                  "bringing in a repository or page and making slide pictures are changes, offered with --allow-changes")
            name = f"MCP check {int(time.time())} b"
            first = await s.call_tool("project_create", {"name": name})
            token = token_of(first)
            check(is_error(first) and "E_CONFIRM_REQUIRED" in text_of(first) and bool(token), "the first call asks for confirmation")
            done = await s.call_tool("project_create", {"name": name, "confirmToken": token})
            check(not is_error(done) and name in text_of(done), "the second call with the token does it")
            print(f"every operation with changes: {len(tools)} tools; created {name!r} in two steps")


async def propose(cmd, home):
    """A propose-only connection (D248): the lists stay valid and apart, and a
    change is saved as a draft (it writes one draft: use a test installation)."""
    async with session(cmd, home, ["--propose"]) as (read, write):
        async with ClientSession(read, write) as s:
            init = await s.initialize()
            check("only proposes" in (init.instructions or ""), "propose: the instructions say the connection only proposes")
            tools = await tool_list(s, 12, False)
            changes_apart(tools, "core, proposing")
            check("draft_list" in tools and "render_submit" not in tools, "propose: draft_list offered, renders not")
            r = await s.call_tool("run_change", {"name": "content.rules.add", "input": {"text": "MCP check rule"}, "note": "a check"})
            check(not is_error(r) and "Saved as a draft" in text_of(r), "propose: run_change saves a draft")
            r = await s.call_tool("run_change", {"name": "revision.approve", "input": {"id": "rev_check"}})
            check(is_error(r) and "E_HUMAN_ONLY" in text_of(r), "propose: approving is refused")
            print(f"core, proposing: {len(tools)} tools; a draft saved")
    async with session(cmd, home, ["--propose", "--toolset", "all"]) as (read, write):
        async with ClientSession(read, write) as s:
            await s.initialize()
            tools = await tool_list(s, 0, False)
            check("revision_approve" not in tools and "render_submit" not in tools and "project_create" in tools, "propose, all: drafts of changes, nothing people-only or rendered")
            print(f"every operation, proposing: {len(tools)} tools")


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--cmd", required=True, help="the metarush binary")
    p.add_argument("--home", default="", help="the installation (--home)")
    p.add_argument("--changes", action="store_true", help="also check servers with --allow-changes (creates two projects)")
    p.add_argument("--toolset", default="review", help="the toolset to check beside the core and all (default review)")
    a = p.parse_args()
    asyncio.run(core(a.cmd, a.home))
    asyncio.run(toolset(a.cmd, a.home, a.toolset))
    asyncio.run(every_operation(a.cmd, a.home))
    if a.changes:
        asyncio.run(changes(a.cmd, a.home, a.toolset))
        asyncio.run(propose(a.cmd, a.home))
    print(json.dumps({"checks": checks, "failures": failures}))
    sys.exit(1 if failures else 0)


if __name__ == "__main__":
    main()
