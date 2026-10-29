"""Haiku's Claude Code session for one call, on the Claude subscription.

It stays open for the whole call, so it holds the conversation and each turn sends only
what's new. A call that a session placed starts it as soon as the switchboard takes the
call, so Haiku writes its opening while the phone rings, and closes it when the call ends
or is forgotten; other calls start and close it with their pipeline, from the phone's
offer on. claude_code.py makes it Pipecat's LLM service.

On a call for a session, Haiku has three tools, pass_to_session, answer_permission and
answer_question: a small MCP server (call_tools.py) that relays each tool call over a socket
in the session's private folder. The call's voice takes the tool calls from your first words
on. One made in the opening passes and answers nothing: you haven't said anything yet. Once,
writing its opening while the phone rang, Haiku passed a message nobody had said
(tools/phone-check).
"""

import asyncio
import json
import sys
import tempfile
import time
from collections.abc import Callable
from pathlib import Path

COMMAND = [
    "claude", "-p", "--model", "haiku", "--no-session-persistence",
    "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
    "--include-partial-messages",
    # The voice talks, and on a call for a session passes messages and answers with its own
    # tools (added in start): no built-in tools, no other MCP servers, no slash commands, and
    # no settings files, whose hooks could start another call.
    "--tools", "", "--strict-mcp-config", "--disable-slash-commands", "--setting-sources", "",
]  # fmt: skip
TOOLS = ["mcp__ringback__pass_to_session", "mcp__ringback__answer_permission", "mcp__ringback__answer_question"]
# The first turn. A call for a session asks for it while the phone rings, but its reply is
# spoken only once you've answered, so it says what's true when the reply is heard.
OPENING = "[The developer has answered the call.]"
NOTHING_YET = (
    "Nothing passed or answered: the developer hasn't said anything yet, so there's nothing from them "
    "for the session. Just open the call."
)


class Haiku:
    def __init__(self, instructions: str, log: Callable[[str], None], tools: bool = False):
        self._instructions = instructions
        self._log = log
        self._with_tools = tools  # its own tools, on a call for a session
        self.events: asyncio.Queue[dict | None] = asyncio.Queue()  # what it writes; None once it has ended
        self.opening: float | None = None  # when the opening was asked for
        self.first_words: float | None = None  # when the first words of the last turn's reply came
        self.used = False  # a call's voice has read its replies, so a new offer's voice can't take it over
        self._tool: Callable[[str, dict], str] | None = None  # the call's voice, once it takes tool calls
        self._tools: asyncio.Server | None = None
        self._dir: tempfile.TemporaryDirectory | None = None
        self._process: asyncio.subprocess.Process | None = None
        self._reader: asyncio.Task | None = None

    async def start(self) -> None:
        """Starts the session and asks for the call's opening."""
        # An empty working directory, so no project's CLAUDE.md is loaded.
        self._dir = tempfile.TemporaryDirectory(prefix="ringback-voice-")
        command = [*COMMAND, "--system-prompt", self._instructions]
        if self._with_tools:
            # Haiku's tools reach this call over a socket in its private folder.
            path = str(Path(self._dir.name) / "tools.sock")
            self._tools = await asyncio.start_unix_server(self._serve_tool, path=path)
            server = {"command": sys.executable, "args": ["-m", "ringback_voice.call_tools", path]}
            command += [
                "--mcp-config", json.dumps({"mcpServers": {"ringback": server}}),
                "--allowedTools", ",".join(TOOLS), "--disallowedTools", "EndConversation",
            ]  # fmt: skip
        self._process = await asyncio.create_subprocess_exec(
            *command, cwd=self._dir.name, stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE,
            limit=1 << 20,
        )  # fmt: skip
        self._reader = asyncio.create_task(self._read())
        self.opening = await self.send(OPENING)

    async def send(self, message: str) -> float:
        """Sends a turn and returns when it was sent."""
        self.first_words = None
        self._process.stdin.write(
            (json.dumps({"type": "user", "message": {"role": "user", "content": message}}) + "\n").encode()
        )
        await self._process.stdin.drain()
        return time.monotonic()

    def take_tools(self, tool: Callable[[str, dict], str]) -> None:
        """Lets the call's voice answer Haiku's tool calls from now on."""
        self._tool = tool

    async def close(self) -> None:
        if self._tools:
            self._tools.close()
            self._tools = None
        if self._process and self._process.returncode is None:
            self._process.stdin.close()
            try:
                await asyncio.wait_for(self._process.wait(), 2)
            except TimeoutError:
                self._process.kill()
                await self._process.wait()
        if self._reader:
            self._reader.cancel()
            self._reader = None
            self.events.put_nowait(None)  # nothing more comes
        if self._dir:
            self._dir.cleanup()
            self._dir = None

    async def _read(self):
        while line := await self._process.stdout.readline():
            try:
                event = json.loads(line)
            except ValueError:
                continue
            stream = event.get("event", {}) if event.get("type") == "stream_event" else {}
            if self.first_words is None and stream.get("delta", {}).get("type") == "text_delta":
                self.first_words = time.monotonic()
            await self.events.put(event)
        await self.events.put(None)

    async def _serve_tool(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter):
        try:
            request = json.loads(await reader.readline())
            if self._tool:
                text = self._tool(request.get("tool"), request.get("input") or {})
            else:
                self._log(f"Haiku's {request.get('tool')} in its opening, not passed: {request.get('input')!r}")
                text = NOTHING_YET
        except (ValueError, AttributeError):
            text = "The call couldn't read that tool call."
        writer.write((json.dumps({"text": text}) + "\n").encode())
        await writer.drain()
        writer.close()
