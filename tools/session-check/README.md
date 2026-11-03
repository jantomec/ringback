# Session check

Plays a session's calls on this Mac, through ringback's real agent and hub, up to where the
phone would ring. A Claude Code session (`claude -p`) loads the ringback mod from `mod/`,
runs `/ringback`, then does a little work. When that turn ends, the mod has the session write
its call briefing and gives it to an agent (`agent/`) started here, which asks a hub (`hub/`)
started here to ring. The hub knows only a made-up phone, registered as the app registers,
with its On-device voice on, so a call rings with no speech node linked instead of waiting for
one; it has no APNs key, so it logs the push it would have sent ("no [apns.sandbox] key in
the config") and nothing rings. Then a message is left for the session through the agent's
socket, as the voice leaves what you say on a call: the session answers it, and since no
call is live, it writes a new briefing and calls again. Neither call is answered, so once
each one's time is up the agent tells the session so, and the mod adds that to the
conversation as a note, which starts no turn. Then the session is asked at the prompt, as you
would when you're back, what it was told about its last call, and it should quote the note.
Use it to check a change to the mod, the agent's session side or the hub's placing of calls
before a call on the phone. The phone, the voice's conversation, the speech node and Nebula
aren't part of it; `tools/line-check` plays a call through the real parts with a stand-in
phone, the session's side played through the agent's socket.

Your own agent can keep running. The check builds the hub and the agent from this checkout
into a throwaway folder under `/tmp` (a Unix socket's path must stay short), and runs them
and the sessions there: the hub on a free port on 127.0.0.1 with its own HOME and config,
the agent with `--socket` and `--config` of its own, and the agent and the sessions with a
throwaway HOME, since the mod finds the agent's socket at
`$HOME/.local/state/ringback/voice.sock`. Claude Code finds your login in your login
keychain, which macOS looks for under HOME, so the check links `~/Library/Keychains` into the
throwaway HOME; nothing else of yours is used. It also leaves out the `CLAUDE…` variables of
the shell it runs in, as from an ordinary terminal, in case that's a Claude Code session
(`tools/env-check`). The sessions run with `--setting-sources ""` and `--strict-mcp-config`,
so neither your settings nor your account's connectors load.

Needs Go and Claude Code. Run from the repo root; it uses your Claude subscription:

    uv run --project tools tools/session-check/session_check.py [--model MODEL] [--unanswered S] [--keep DIR] [WORK]

- `WORK` is the prompt the session works on before it calls (by default, one sentence).
- `--model` is the session's model, Haiku by default to keep it quick.
- `--unanswered S` is how long the agent waits for each call to be answered, 10 s by
  default instead of the real 90 s (it sets `RINGBACK_UNANSWERED` for the agent).
- `--keep DIR` keeps the hub's and the agent's logs and state and everything the session
  printed (`session.jsonl`); not the socket, the keychain's link or Claude Code's own files
  from the throwaway HOME.

Each call the hub rings also starts the agent's Haiku on its briefing (`claude -p`, on your
subscription), as for a real call, so its opening is ready when you answer; it's closed when
the call is forgotten. A normal run places three calls, so three of those, besides the
session's six turns.

It prints the session's turns, the briefings among them, the hub's log, the agent's log of
calls and messages, and these checks:
- the phone's own speech (`/phone-speech`) for a call the agent didn't place is refused
  (403, `unknown call`): the hub asks the agent, which refuses it;
- the session placed a call, and how long that took from its first prompt;
- the call names the project, and its subject has 3–8 words;
- the agent placed that call, and the hub rang it: its log has the push it would send, with
  the call's ID, to the made-up phone;
- the message was left, the session answered it, and it called again with a new call ID,
  which the hub rang too, and how long that took from leaving the message;
- each call's outcome, that it wasn't answered, was shown in the session (the mod's dim
  line, a `ui_log` event in the session's stream) within S + 30 s;
- those outcomes started no turn and no call in the next 10 s;
- asked at the prompt what it was told about its last call, the session quoted that it
  "wasn't answered within S s". Only the note says how long the call waited, so the
  session can't get that from a guess;
- what the agent started (each call's Haiku and its tools) had ended within 30 s of the
  agent stopping; it's killed after that.

## Permissions (`--asks`)

    uv run --project tools tools/session-check/session_check.py --asks [--model MODEL] [--keep DIR]

The session, in default mode with ringback on, runs a command that needs your permission
(`date +%s > allowed.txt`). With no call yet, the agent places one to ask it. The check finds
the request in the agent's log and allows it through the agent's socket, as the voice does
when you say yes (`POST /sessions/{id}/permissions/{ask}`), and then checks that the file
exists. Then another (`refused.txt`) is refused, and mustn't exist once the session has
answered. Then a second session, in auto mode, runs `python3 -c 'print(6*7)'`, which must ask
nothing. Its checks:
- the permission reached the agent and the voice's yes was taken (200);
- the agent placed a call to ask it, and the hub rang it ("Permission to run a shell
  command");
- allowed, the command ran;
- the second permission reached the agent and the voice's no was taken (200); it goes to the
  session's latest call still ringing, which in these runs was the one its briefing placed
  after the first command;
- refused, the command didn't run, and what the session said about it;
- in auto mode, the command ran and asked nothing;
- what the agent started had ended within 30 s of the agent stopping.

The session's questions (AskUserQuestion) can't be tried here: `claude -p` doesn't offer
that tool, even when `--tools` names it (checked again with 2.1.317: named with Bash, the
session's tools had Bash and not AskUserQuestion). The agent's side of questions is in its
tests (`agent/sessions_test.go`), the mod's in `mod/tests`.

## What it doesn't check

- An offer (`/offer`) for a call the agent didn't place: with no speech node linked, the hub
  answers an offer 503 before asking any agent. The same refusal is checked through the
  phone's own speech, which does ask the agent.
- Which address the phone is sent for the call's audio: the hub fills in its own.

The `--asks` check waits for the session's own answer after each prompt, not for the next turn
to end: that can be the briefing the mod has the session write after the turn before, as in
the first `--asks` run of 2026-11-03, which then checked the refused file too early.

## Results

On 2026-11-03, Claude Code 2.1.317, Haiku (`claude-haiku-5-5`) as the session's model and the
agent's, with the hub and the agent from this checkout. Results from an earlier implementation
of the parts are in this file's git history.

Four runs: all 15 checks passed each time, in 42–45 s each, the builds included. The first
call was placed 5–6 s after the session's first prompt, with a subject of 4–7 words, and the
second 4–5 s after the message was left. Both outcomes were shown in the session 10.0–10.5 s
after the second call was placed, each when its own call's 10 s were up, and set off nothing
in the next 10 s. Asked what it was told, the session quoted the note each time; once with
the mod's own words before it ("A note from ringback, the mod that calls the developer's
phone…"). Each session had seven turns, `/ringback` and six of Haiku's, for about $0.002 by
Claude Code's own count; the agent's three openings aren't in that. The third call's Haiku
and its tools were still running when the agent stopped, and ended by themselves 3.3–3.6 s
later. In the second run a throwaway script also watched the processes and Claude Code's own
file for each session process (`~/.claude/sessions/<pid>.json` in the throwaway HOME) every
0.5 s: each of the three calls started a Haiku (`claude -p`, with `ringback-agent mcp`), and
the first two were closed 9–10 s later, as their calls were forgotten. That file never had a
`bridgeSessionId`: the `/remote-control` that `/ringback` runs didn't turn Remote Control on
in `claude -p` (`tools/env-check` on that file).

`--asks`, three runs, 28–32 s each: every check passed (6, 6, then 7 once the agent's placing
a call for the first permission was checked on its own). The first run waited for the wrong
turn (above) and checked `refused.txt` too early; it passed, but proved less than it said.
After the fix (two runs), each answer was taken within 0.2 s of the agent logging the
request; allowed, the file was written 2 s after the prompt. The first permission had a
call placed for it ("Permission to run a shell command"); the second joined the call the
session's briefing had placed meanwhile, so for a while two calls from one session rang at
once, as the agent does for a ringing call (a session's news goes only to a call you've
answered). Refused, the session got "The developer said no on a ringback call: "No, don't.""
as the tool's error, said it wouldn't retry, and wrote nothing. In auto mode, `print(6*7)`
ran and nothing was asked. Each run's two sessions came to about $0.003.
