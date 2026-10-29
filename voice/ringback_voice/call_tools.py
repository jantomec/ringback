"""The voice's tools on a call for a session: a small MCP server that `claude -p` starts.

    python -m ringback_voice.call_tools SOCKET

It speaks MCP over stdin and stdout, and relays each tool call to the call that started
Haiku, over SOCKET in that call's private folder. A tool call ends Haiku's turn until its
result comes back, so Haiku can't go on to imagine the session's answer, as it did when it
passed messages in tags. Its tools pass the session a message, or answer the permission or
questions the session asks you. Only the standard library, so it starts fast.
"""

import json
import socket
import sys

TOOLS = [
    {
        "name": "pass_to_session",
        "description": (
            "Pass a message from the developer to the Claude Code session this call is about: an "
            "instruction, a decision, an answer to its question, or a question the briefing doesn't "
            "answer. Say that you're passing it on before you call this."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {"message": {"type": "string", "description": "The message, as from the developer"}},
            "required": ["message"],
        },
    },
    {
        "name": "answer_permission",
        "description": (
            "Answer the session's request for permission, from a note or the briefing, with the developer's "
            "clear yes or no. Only a clear yes is allow, and only a clear no is deny. Say what you'll answer "
            "before you call this."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "permission": {"type": "string", "description": "The permission's id"},
                "allow": {"type": "boolean", "description": "true for a clear yes, false for a clear no"},
                "said": {"type": "string", "description": "The developer's own words that decided it"},
            },
            "required": ["permission", "allow", "said"],
        },
    },
    {
        "name": "answer_question",
        "description": (
            "Answer the session's questions, from a note or the briefing, with the developer's choice, or "
            "their own words where no option fits. Answer every question it asked under that id. Say what "
            "you'll answer before you call this."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "question": {"type": "string", "description": "The questions' id"},
                "answers": {
                    "type": "array",
                    "items": {
                        "type": "object",
                        "properties": {
                            "question": {"type": "string", "description": "The question, as the session asked it"},
                            "answer": {
                                "type": "string",
                                "description": (
                                    "The chosen option's label (labels joined by \", \" where several can be "
                                    "chosen), or the developer's own answer"
                                ),
                            },
                        },
                        "required": ["question", "answer"],
                    },
                },
                "said": {"type": "string", "description": "The developer's own words that decided it"},
            },
            "required": ["question", "answers", "said"],
        },
    },
]


def relay(path: str, name: str, arguments: dict) -> str:
    with socket.socket(socket.AF_UNIX) as s:
        s.connect(path)
        s.sendall((json.dumps({"tool": name, "input": arguments}) + "\n").encode())
        return json.loads(s.makefile().readline())["text"]


def answer(path: str, request: dict) -> dict | None:
    method, params = request.get("method"), request.get("params") or {}
    if method == "initialize":
        return {
            "protocolVersion": params.get("protocolVersion", "2025-06-18"),
            "capabilities": {"tools": {}},
            "serverInfo": {"name": "ringback", "version": "0.1.0"},
        }
    if method == "tools/list":
        return {"tools": TOOLS}
    if method == "tools/call":
        try:
            text = relay(path, params.get("name", ""), params.get("arguments") or {})
            return {"content": [{"type": "text", "text": text}], "isError": False}
        except (OSError, ValueError, KeyError) as e:
            return {"content": [{"type": "text", "text": f"The call couldn't take it: {e}"}], "isError": True}
    if method == "ping":
        return {}
    return None


def main() -> None:
    path = sys.argv[1]
    for line in sys.stdin:
        try:
            request = json.loads(line)
        except ValueError:
            continue
        if "id" not in request:
            continue  # a notification, such as notifications/initialized
        result = answer(path, request)
        if result is None:
            response = {"jsonrpc": "2.0", "id": request["id"], "error": {"code": -32601, "message": "unknown method"}}
        else:
            response = {"jsonrpc": "2.0", "id": request["id"], "result": result}
        sys.stdout.write(json.dumps(response) + "\n")
        sys.stdout.flush()


if __name__ == "__main__":
    main()
