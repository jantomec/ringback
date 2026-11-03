# Ringback's links

How ringback's parts talk to each other ("The parts" in `docs/design.md`). The phone talks
to the hub over HTTP. The speech nodes and the agents each open one WebSocket to the hub, so
neither needs to be reachable. A session's mod talks to the agent on its own Mac, over a
socket only you can open (`~/.local/state/ringback/voice.sock`).

## The phone

The app posts JSON and gets JSON back, on the hub's `listen` address. What lets it act on a
call is the call's ID, a random string of 16 to 128 characters that the push brings; what
lets it read the waiting calls and the sessions is its own push token. Each request gets at
most 5 s.

| Endpoint | What it's for |
|---|---|
| `/register` | the phone's push tokens and name, its work hours, and whether its On-device voice is on |
| `/check` | the connection check: every 30 min the hub wakes the phone with a background push, and the app answers here; its work hours come along |
| `/events` | what happened to a call: rang, answered, ended and how long it lasted, called now, sent to the Claude app |
| `/waiting` | the calls that wait, for Recents, and the hold switch |
| `/contacts` | the sessions listening on every Mac, for the Sessions tab |
| `/hold` | reads or sets the phone's hold switch |
| `/offer` | the call's WebRTC offer, made while the phone rings, for a waiting call called now (`now`), a call back (`back`) or a session called from Sessions (`contact`); the agent that owns the call accepts or refuses it, and the hub answers with its SDP. With no speech node linked: 503 `no speech node` |
| `/answered` | you answered a call whose offer came while it rang |
| `/claude-app` | sends a waiting call's question to the session's own dialog, which Remote Control shows in the Claude app |
| `/reask` | you took calls: the held ones among them ask again now |
| `/ping` | `GET`: whether the hub can be reached, checked while the phone rings |
| `/phone-speech` | a WebSocket, for a call the phone hears and speaks itself (the last section) |

The hub also serves its portal (`/portal/`), and two endpoints that place a call and list
sessions over plain HTTP, without a token: `/call` and `/sessions`. The hub's smoke test uses
them.

## The WebSocket links

Version 1. A hub refuses a hello with another version.

- The hub listens on the same address as its HTTP endpoints. `GET /speech` and `GET /agent`
  upgrade to a WebSocket. The request carries `Authorization: Bearer <token>`; the hub
  compares it with the tokens in its config (`[tokens] speech = "…"`, `agent = "…"`) and
  answers 401 to anything else.
- Control is JSON in text frames: `{"type": "…", …}`. Times are ISO 8601 strings, durations
  are seconds as numbers.
- A message that needs an answer carries `"id": n` (an integer the sender chooses); the
  answer is `{"type": "reply", "id": n, "ok": true, …}` or
  `{"type": "reply", "id": n, "ok": false, "status": 403, "error": "unknown call"}`.
- The opener sends `hello` first: `{"type": "hello", "version": 1, "name": "…"}`. The hub
  answers `{"type": "welcome", "version": 1}`.
- Either side pings every 15 s (WebSocket ping frames); a link silent for 45 s is closed. The
  opener reconnects after 1, 2, 4… up to 30 s.

## The speech node (`/speech`)

The hub opens a numbered **stream** for each call: one direction for hearing, one for
speaking, both with the same number. Binary frames carry audio: 4 bytes of stream number
(big-endian), then for hearing the samples, for speaking 4 more bytes of piece number
(big-endian) and then the samples. Samples are signed 16-bit little-endian, mono.

Hello: `{"type": "hello", "version": 1, "name": "…", "hearing": true, "speaking": true}`.

| Direction | Message |
|---|---|
| node → hub | `{"type": "models", "loaded": true}` when its worker has loaded the models, and `"loaded": false` when the worker exits (idle, stopped, or failed). The hub shows it on its portal; until a node says, its models count as not loaded, as they are when it links |

### Hearing

| Direction | Message |
|---|---|
| hub → node | `{"type": "warm"}`: a call is coming: load the models now. The node also loads them on `hear`, and lets them go when it has had no stream for a while (`idle` in its settings, 60 s) |
| hub → node | `{"type": "hear", "stream": 7, "sensitivity": 0.5, "gate": 0.5}` opens it, with the app's interruption sensitivity and background filter (0 = off), mapped to Silero's thresholds as `voice/ringback_voice/hearing.py` does |
| hub → node | binary: stream 7, then 20 ms of your audio at 16 kHz (320 samples) |
| node → hub | `{"type": "speech_started", "stream": 7}` when Silero hears you start (the hub interrupts the voice on it) |
| node → hub | `{"type": "speech_stopped", "stream": 7}` when you pause |
| node → hub | `{"type": "turn", "stream": 7, "text": "…", "secs": 2.4, "took": 0.05}` when Smart Turn v3 says you've finished, or the turn wait (1.5 s) has passed, with Parakeet's words for the whole turn, how long you spoke and how long recognition took |
| hub → node | `{"type": "hear_end", "stream": 7}` closes it |

### Speaking

| Direction | Message |
|---|---|
| hub → node | `{"type": "speak", "stream": 7, "piece": 3, "text": "…"}` asks for one piece of a reply |
| node → hub | binary: stream 7, piece 3, then Kokoro's audio at 24 kHz, in chunks as it's made |
| node → hub | `{"type": "spoken", "stream": 7, "piece": 3, "secs": 2.1, "took": 0.08}` after the last chunk |
| hub → node | `{"type": "speak_cancel", "stream": 7}` drops every piece of the stream not yet sent |

Pieces of one stream are made in the order asked. The voice is Kokoro `af_heart`.

## The agent (`/agent`)

The agent runs on each Mac with sessions. It keeps what its sessions need: their mods' socket,
the calls they place, held or ringing or answered, what they ask, and Haiku or Opus for each
call. A call's ID is its secret.

Hello: `{"type": "hello", "version": 1, "name": "…"}`; the name is the Mac's, for the log.

### Sessions and calls

| Direction | Message |
|---|---|
| agent → hub | `{"type": "sessions", "sessions": [{"session_id", "project", "since", "subject", "briefed_at"}]}` when they change and every 30 s; the hub lists them with this agent as their place |
| agent → hub | `{"type": "place", "id": 1, "call_id", "project", "subject", "session_id", "question": false}`, to place a call or ask again about a held one. Reply: `{"ok": true}` (it rings), `{"ok": true, "held": true, "held_until": "…"}`, or, while no speech node is linked, `{"ok": true, "held": true, "reason": "no speech"}`: it waits, and the hub sends `reask` once a node links |
| hub → agent | `{"type": "reask", "call_ids": [...]}`: you took calls; the held ones among them ask again now |

### The phone's requests

The hub passes each to the agent that placed the call, or that lists the session, and answers
the phone with the reply's `status` and `error`.

| Direction | Message |
|---|---|
| hub → agent | `{"type": "offer", "id": 2, "call_id", "kind": "ring" \| "now" \| "back" \| "contact", "session_id" (for contact), "ringing": true}`. Reply: `{"ok": true}` (the call goes ahead; the agent readies its Haiku) or a refusal (`403 unknown call`, `403 not listening`…). The phone's offer itself carries the sensitivity and background filter, which the hub keeps for the hearing stream |
| hub → agent | `{"type": "claude_app", "id": 3, "call_id"}`, from `/claude-app`. Reply: ok, or `403 not waiting`, `409 not listening`, `409 no question` |

### During a call

The agent numbers its replies in a call 1, 2, 3…, taking the next number the moment it starts
one (the opening, a reply to a turn, one to a note), so a reply cut off while it's still being
written can be told from the next.

| Direction | Message |
|---|---|
| hub → agent | `{"type": "answered", "call_id"}` when you answer (`/answered`, or an offer without ringing) |
| hub → agent | `{"type": "connected", "call_id"}` when the audio connects; the voice may speak once both have come |
| agent → hub | `{"type": "say", "call_id", "reply": 3, "text": "…"}`: the reply's text as it's written, in any slices; the hub cuts it into spoken pieces |
| agent → hub | `{"type": "said", "call_id", "reply": 3}` at the end of a reply; a reply with nothing to say (only a tool call) is just this, and the hub says `idle` once the voice is |
| hub → agent | `{"type": "heard", "call_id", "reply": 3, "text": "…"}` as reply 3 plays: everything of the current reply you've heard so far (for what's held until you've heard it announced) |
| hub → agent | `{"type": "interrupted", "call_id", "reply": 3, "heard": "…"}` when you cut the voice off: what you'd heard of reply 3; the rest is never spoken, and the hub drops whatever more comes of it, so the agent stops writing it. You also cut off a reply to your turn that hasn't come yet: `reply` is then the next number and `heard` is empty |
| hub → agent | `{"type": "turn", "call_id", "text": "…", "interrupted": true}`: your words, for the next reply, which the agent starts at once. `interrupted` says you cut the voice off since your last turn (or a reply that hadn't come yet), or it hadn't finished speaking once yet. Empty when a stop held no words the recogniser caught: if it interrupted, the voice carries on (the `NOISE` prompt), once in a row; otherwise it goes unanswered, and its reply is just `said` |
| hub → agent | `{"type": "speaking", "call_id"}` when the voice's audio starts playing; the first one is the opening, which the session's notes wait for |
| hub → agent | `{"type": "idle", "call_id"}` when the voice has finished speaking and you aren't speaking. A note from the session (`[The session: …]`, a new ask) waits for it, then runs the next reply |
| agent → hub | `{"type": "hang_up", "call_id"}` when the call should end from the voice's side (none yet) |
| hub → agent | `{"type": "ended", "call_id", "lasted": 12.3}` when the call ends; `lasted` is null if its audio never connected |

When an agent's link closes, the hub ends its calls, and the agent ends them on its side.

## The phone as the speech node (`/phone-speech`)

When no speech node is linked and the phone's own voice is on (**On-device voice** in the
Settings app, which the app sends the hub as `"on_device": true` on `/register`), a call
still rings, and the phone hears and speaks itself: no audio crosses the network, only text.
The app learns it from the offer: the hub answers `/offer` 503 `{"error": "no speech node"}`,
and the app, with its own voice on, opens this link instead, for that call.

It's a WebSocket the app opens to the hub, `GET /phone-speech`; the call's ID is its secret,
as for an offer, so there's no token. Messages are the speech node's, with no stream numbers
(the link is one call) and no binary frames, and two more for what the phone plays.

| Direction | Message |
|---|---|
| phone → hub | `{"type": "hello", "version": 1, "call_id", "kind": "ring" \| "now" \| "back" \| "contact", "session_id" (for contact), "ringing": true, "sensitivity": 0.5, "background_filter": 0.5}`, as the offer's fields |
| hub → phone | `{"type": "welcome", "version": 1}` once the agent accepts the call, or `{"type": "refused", "status": 403, "error": "unknown call"}` and the link closes |
| phone → hub | `{"type": "answered"}` when you answer a call whose link opened while it rang (as `/answered`) |
| hub → phone | `{"type": "hear"}`: start listening (once you've answered) |
| phone → hub | `{"type": "speech_started"}`, `{"type": "speech_stopped"}`, `{"type": "turn", "text": "…", "secs": 2.4, "took": 0.1}`, as the speech node's; `text` empty for a stop with no words |
| hub → phone | `{"type": "speak", "piece": 3, "text": "…"}`: synthesise and play this piece, after the ones before |
| phone → hub | `{"type": "spoken", "piece": 3, "secs": 2.1, "took": 0.2}` once its audio is all made, with its length |
| phone → hub | `{"type": "playing", "piece": 3}` the moment it starts playing, and `{"type": "played", "piece": 3}` when it has played to its end; the hub counts what you've heard from these |
| hub → phone | `{"type": "speak_cancel"}`: stop playing at once, and drop every piece not yet played |
| hub → phone | `{"type": "end"}`: the call is over from the hub's side; the phone ends it |

The link closing ends the call; the hub tells the agent how long you talked, from when the
link was welcomed or you answered, whichever came last.
