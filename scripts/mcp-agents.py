"""Fake AI apps on the review desk (D248-D251), driven by the official MCP Python SDK.

Two scripted MCP clients connect to `metarush mcp serve --propose` the way
Codex and Cursor do (each names itself in clientInfo: "codex-mcp-client",
"cursor-vscode"), read, check and propose, and fail to approve:

  propose  "codex" (the core) reads the cut and proposes a shorter first clip as
           a draft version; "cursor" (the edit toolset) proposes a house rule.
           Both are refused approving a version, deciding a draft, a final
           render, a change through a reading tool and a change by a tool
           name the list doesn't offer. Prints the drafts' IDs as JSON.
  news     after a person decided (the browser run, or a terminal), "codex"
           hears it once on its next call.
  redo     after Not quite, "codex" reads what should change and makes a new
           draft in the old one's place.

scripts/mcp-agents.sh sets up the SDK in a throwaway virtual environment.
"""

import argparse
import asyncio
import json
import os
import sys

from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client
from mcp.types import Implementation

failures = []
checks = 0


def check(ok, what):
    global checks
    checks += 1
    if not ok:
        failures.append(what)
        print("FAIL", what, file=sys.stderr)


def get(obj, snake, camel):
    return getattr(obj, snake) if hasattr(obj, snake) else getattr(obj, camel)


def text_of(result):
    return "\n".join(getattr(c, "text", "") for c in result.content)


def is_error(result):
    return bool(get(result, "is_error", "isError"))


def structured(result):
    return get(result, "structured_content", "structuredContent") or {}


def read_only(tool):
    return None if tool.annotations is None else get(tool.annotations, "read_only_hint", "readOnlyHint")


def server(cmd, home, extra):
    args = (["--home", home] if home else []) + ["mcp", "serve", "--propose"] + extra
    return stdio_client(StdioServerParameters(command=cmd, args=args, env=dict(os.environ)))


def refused(r, code, what):
    check(is_error(r) and code in text_of(r), f"{what} is refused ({code}): {text_of(r)[:160]!r}")


async def codex(cmd, home, project):
    """Codex: the core, reads the cut and proposes a shorter first clip."""
    async with server(cmd, home, []) as (read, write):
        async with ClientSession(read, write, client_info=Implementation(name="codex-mcp-client", version="0.1")) as s:
            init = await s.initialize()
            instructions = init.instructions or ""
            check("only proposes" in instructions, "codex: the instructions say the connection only proposes")
            tools = {t.name: t for t in (await s.list_tools()).tools}
            check(len(tools) <= 12, f"codex: at most 12 tools ({len(tools)})")
            check({"find", "describe", "run", "run_change", "draft_list", "cut_show"} <= set(tools), "codex: the core with run_change and draft_list")
            check("render_submit" not in tools, "codex: no render tool when proposing")
            check(read_only(tools["run"]) is True and read_only(tools["run_change"]) is False, "codex: run reads, run_change saves drafts")

            # Read and check.
            r = await s.call_tool("find", {"task": "shorten the first clip of the cut"})
            check(not is_error(r) and text_of(r).strip() != "", "codex: find answers")
            r = await s.call_tool("project_show", {"id": project})
            show = structured(r)
            head = show.get("headRevisionId", "")
            check(not is_error(r) and head, f"codex: project_show gives the cut's latest version ({head})")
            r = await s.call_tool("run", {"name": "revision.show", "input": {"id": head}})
            spec = structured(r).get("spec", {})
            clip = (spec.get("clips") or [{}])[0]
            check(not is_error(r) and clip.get("id"), "codex: revision.show gives the cut")
            r = await s.call_tool("cut_show", {"projectId": project})
            check(not is_error(r) and "sheet" in structured(r), "codex: cut_show gives the cut sheet")
            plan = {"plan": {"baseRevisionId": head, "instruction": "Shorter first clip", "rationale": "The opening drags.",
                             "ops": [{"op": "updateClip", "id": clip["id"], "set": {"out": round(clip["out"] - 1, 3)}}]}}
            r = await s.call_tool("run", {"name": "edit.check", "input": plan})
            check(not is_error(r) and structured(r).get("valid") is True, "codex: the change checks out")

            # Propose: saved as a draft beside the cut.
            r = await s.call_tool("run_change", {"name": "edit.apply", "input": plan, "note": "The opening drags; I took a second off the first clip."})
            d = structured(r)
            check(not is_error(r) and "Saved as a draft" in text_of(r), "codex: run_change saves a draft")
            check(d.get("kind") == "version" and d.get("agent") == "codex" and d.get("via") == "mcp", f"codex: a version draft, made by codex through MCP ({d.get('kind')}, {d.get('agent')}, {d.get('via')})")
            rev = (d.get("revision") or {}).get("id", "")
            r = await s.call_tool("project_show", {"id": project})
            check(structured(r).get("headRevisionId") == head, "codex: the cut's latest version didn't move")

            # Fail to approve, render, decide or slip a change through.
            refused(await s.call_tool("run_change", {"name": "revision.approve", "input": {"id": rev}}), "E_HUMAN_ONLY", "codex: approving its draft's version")
            refused(await s.call_tool("run_change", {"name": "draft.decide", "input": {"id": d.get("id"), "decision": "approve"}}), "E_HUMAN_ONLY", "codex: deciding its draft")
            refused(await s.call_tool("run", {"name": "revision.approve", "input": {"id": rev}}), "E_HUMAN_ONLY", "codex: approving through run")
            refused(await s.call_tool("run", {"name": "edit.apply", "input": plan}), "E_UNAUTHORIZED", "codex: a change through the reading tool")
            refused(await s.call_tool("run_change", {"name": "render.submit", "input": {"revisionId": head, "quality": "final"}}), "E_UNAUTHORIZED", "codex: a final render")
            refused(await s.call_tool("project_create", {"name": "Codex's own"}), "E_UNAUTHORIZED", "codex: a change by a tool name it wasn't offered")
            r = await s.call_tool("draft_list", {})
            mine = structured(r).get("drafts", [])
            check(any(x.get("id") == d.get("id") for x in mine) and all(x.get("agent") == "codex" for x in mine), "codex: draft_list shows its own drafts")
            return d.get("id")


async def cursor(cmd, home, project):
    """Cursor: the edit toolset, proposes a house rule and a brand colour."""
    async with server(cmd, home, ["--toolset", "edit"]) as (read, write):
        async with ClientSession(read, write, client_info=Implementation(name="cursor-vscode", version="1.0")) as s:
            await s.initialize()
            tools = {t.name: t for t in (await s.list_tools()).tools}
            check(len(tools) <= 25, f"cursor: at most 25 tools ({len(tools)})")
            check("brand_change" in tools and read_only(tools["brand_change"]) is False and read_only(tools["brand"]) is True, "cursor: the brand's reads and drafts are apart")
            check("render_change" not in tools, "cursor: no render changes when proposing")
            r = await s.call_tool("brand", {"mode": "show", "projectId": project})
            check(not is_error(r), "cursor: brand show answers")
            r = await s.call_tool("run_change", {"name": "content.rules.add", "input": {"projectId": project, "text": "The name bar never animates"},
                                                 "note": "You asked for still name bars twice."})
            d = structured(r)
            check(not is_error(r) and d.get("kind") == "change" and d.get("agent") == "cursor", f"cursor: a house rule as a draft ({d.get('kind')}, {d.get('agent')})")
            refused(await s.call_tool("brand", {"mode": "set", "projectId": project, "primary": "#2255aa"}), "E_UNAUTHORIZED", "cursor: a change through the reading tool")
            refused(await s.call_tool("run_change", {"name": "revision.approve", "input": {"id": "rev_any"}}), "E_HUMAN_ONLY", "cursor: approving a version")
            refused(await s.call_tool("run_change", {"name": "generate.shot", "input": {"projectId": project}}), "E_UNAUTHORIZED", "cursor: spending money")
            r = await s.call_tool("draft_list", {})
            check(all(x.get("agent") == "cursor" for x in structured(r).get("drafts", [])), "cursor: draft_list shows only its own")
            return d.get("id")


async def redo(cmd, home, project):
    """After Not quite: codex makes a new draft in the old one's place."""
    async with server(cmd, home, []) as (read, write):
        async with ClientSession(read, write, client_info=Implementation(name="codex-mcp-client", version="0.1")) as s:
            await s.initialize()
            r = await s.call_tool("draft_list", {"state": "not_quite"})
            old = (structured(r).get("drafts") or [{}])[0]
            check(old.get("decision", {}).get("note"), "codex reads what should change")
            rev = old.get("revision") or {}
            r = await s.call_tool("run", {"name": "revision.show", "input": {"id": rev.get("id", "")}})
            parent = structured(r).get("summary", {}).get("parentId", "")
            r = await s.call_tool("run", {"name": "revision.show", "input": {"id": parent}})
            clip = (structured(r).get("spec", {}).get("clips") or [{}])[0]
            plan = {"plan": {"baseRevisionId": parent, "instruction": "A little off the first clip", "rationale": old["decision"]["note"],
                             "ops": [{"op": "updateClip", "id": clip["id"], "set": {"out": round(clip["out"] - 0.5, 3)}}]}}
            r = await s.call_tool("run_change", {"name": "edit.apply", "input": plan, "note": "Kept more of the opening, as you asked.", "replaces": old.get("id")})
            d = structured(r)
            check(not is_error(r) and d.get("replaces") == old.get("id"), f"codex's new draft takes the old one's place: {text_of(r)[:200]!r}")
            return d.get("id")


async def news(cmd, home, expect):
    """After a person decided: codex hears it once, on its next call."""
    async with server(cmd, home, []) as (read, write):
        async with ClientSession(read, write, client_info=Implementation(name="codex-mcp-client", version="0.1")) as s:
            await s.initialize()
            r = await s.call_tool("draft_list", {})
            t = text_of(r)
            check("News about your drafts" in t and expect in t, f"codex hears the person's decision ({expect!r}): {t[-300:]!r}")
            r = await s.call_tool("project_list", {})
            check("News about your drafts" not in text_of(r), "codex hears it once")


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--cmd", required=True)
    p.add_argument("--home", default="")
    p.add_argument("--project", default="", help="a project with a cut (ID or name)")
    p.add_argument("--phase", default="propose", choices=["propose", "news", "redo"])
    p.add_argument("--expect", default="Not quite", help="news: words the decision's news has")
    a = p.parse_args()
    out = {}
    if a.phase == "propose":
        out["codex"] = asyncio.run(codex(a.cmd, a.home, a.project))
        out["cursor"] = asyncio.run(cursor(a.cmd, a.home, a.project))
    elif a.phase == "redo":
        out["codex"] = asyncio.run(redo(a.cmd, a.home, a.project))
    else:
        asyncio.run(news(a.cmd, a.home, a.expect))
    out.update({"checks": checks, "failures": failures})
    print(json.dumps(out))
    sys.exit(1 if failures else 0)


if __name__ == "__main__":
    main()
