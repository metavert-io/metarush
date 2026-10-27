"""Starts the community DaVinci Resolve MCP server over stdio — NOT YET RUNNABLE.

The server (github.com/samuelgursky/davinci-resolve-mcp, MIT) is a Python project a person
installs with its own environment; it loads DaVinci Resolve's scripting library from the
app and talks to the running Resolve Studio over the app's own local connection. This
launcher sets the three variables that library needs, from where Resolve installs them,
and starts the server (`resolve-mcp`, its start command, set up per computer). MetaRush
would keep it running for a conversation and offer its tools as steps a person starts
(notes.md).
"""
import os
import platform
import shutil
import sys

PLACES = {
    "Darwin": ("/Library/Application Support/Blackmagic Design/DaVinci Resolve/Developer/Scripting",
               "/Applications/DaVinci Resolve/DaVinci Resolve.app/Contents/Libraries/Fusion/fusionscript.so"),
    "Linux": ("/opt/resolve/Developer/Scripting", "/opt/resolve/libs/Fusion/fusionscript.so"),
}


def main():
    api, lib = PLACES.get(platform.system(), (None, None))
    if not api:
        sys.exit("DaVinci Resolve's scripting runs on a Mac or Linux.")
    env = dict(os.environ, RESOLVE_SCRIPT_API=api, RESOLVE_SCRIPT_LIB=lib, PYTHONPATH=os.path.join(api, "Modules"))
    server = shutil.which("resolve-mcp")
    if os.environ.get("METARUSH_TEST"):
        print("would start", server or "resolve-mcp", "with RESOLVE_SCRIPT_API", api)
        return
    if not server:
        sys.exit("The DaVinci Resolve MCP server isn't set up on this computer.")
    os.execve(server, [server], env)


if __name__ == "__main__":
    main()
