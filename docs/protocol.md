# Ringback's links

How ringback's parts talk to each other ("The parts" in `docs/design.md`). The phone talks
to the hub over HTTP. The speech nodes and the agents each open one WebSocket to the hub, so
neither needs to be reachable. A session's mod talks to the agent on its own Mac, over a
socket only you can open (`~/.local/state/ringback/voice.sock`).

## The phone

The app posts JSON and gets JSON back, on the hub's `listen` address. What lets it act on a
call is the call's ID, a random string of 16 to 128 characters that the push brings. What
names the phone is its push token, with the key it got when it paired: `/pair` trades a
one-time code from the portal (`ringback://pair?hub=…&code=…`, "The hub's portal" in
`docs/design.md`) for that key, and every request that carries the push token (`token`) must
carry the key (`key`) too, or the hub answers 401 `{"error": "not paired"}`. Each request gets
at most 5 s, or 15 s for `/offer`, `/answered`, `/claude-app`, `/reask` and `/cancel`.

| Endpoint | What it's for |
|---|---|
| `/pair` | trades a pairing code (`code`, valid 10 minutes, once) for the phone's key, registering it with what `/register` takes; answers `{"key": "…"}` (64 hex characters), or 403 `{"error": "pairing code not valid"}` |
| `/register` | the phone's push tokens and name, its work hours, and whether its On-device voice is on, with its key; with a new push token and the key, the phone moves to the new token |
| `/check` | the connection check: every 30 min the hub wakes the phone with a background push, and the app answers here; its work hours come along. Without a token it only says the hub was reached |
| `/events` | what happened to a call: rang, answered, ended and how long it lasted, called now, sent to the Claude app |
| `/waiting` | the calls that wait, for Recents, and the hold switch |
| `/contacts` | the sessions listening on every Mac, for the Sessions tab |
| `/hold` | reads or sets the phone's hold switch |
| `/offer` | the call's WebRTC offer, with the call's ID: made while the phone rings (`"ringing": true`), or for a waiting call called now (`"now": true`), a call back (`"back": true`) or a session called from Sessions (`"contact"`: the session's ID); the agent that owns the call accepts or refuses it, and the hub answers with its SDP. With no speech node linked: 503 `no speech node` |
| `/answered` | you answered a call whose offer came while it rang |
| `/claude-app` | sends a waiting call's question to the session's own dialog, which Remote Control shows in the Claude app |
| `/cancel` | `{"call_id"}`: cancels a waiting call, so it never rings; 200, or 403 `{"error": "not waiting"}` (it rang, or was cancelled already). The hub drops it from the waiting calls at once and tells its agent (`cancel`), or, not linked, answers its next `place` for it `{"ok": true, "cancelled": true, "by": "phone"}` for 3 days |
| `/reask` | you took calls: the held ones among them ask again now |
| `/shown` | what sessions showed you, newest first, kept a week: each with its `id`, `session_id`, `project`, `kind` (`image`, `pdf`, `video` or `model`), `name`, `caption`, `at`, a model's `model` (its facts: `animated`, `frames`, `fps`, `up`…), and its `file`'s, `poster`'s and `thumb`'s `size` and `type` (a model has no file) |
| `/shown/file` | `{"id", "part": "file" \| "poster" \| "thumb"}` with the token and key: that file, as it came, with its `Content-Type`; 404 if it isn't kept. Up to 5 min |
| `/view` | a model's live view: `{"shown_id", "sdp", "width", "height"}` with the token and key, the phone's WebRTC offer with a video transceiver that receives only and a data channel. The hub asks its agents (`view`) and answers `{"sdp", "view_id"}`, or as the agents refused: 404 (no computer has it), 409 (its computer can't render 3D models, and why), 410 (it's gone from there). Up to 45 s. On the data channel the phone sends where it looks from (`{"camera": {"yaw", "pitch", "distance", "target"}}`), `{"frame": n}` and `{"play": bool}`; the renderer's `ready` and state come back (`render/README.md`), and `{"status": "ended", "why"}` when it ends |
| `/ping` | `GET`: whether the hub can be reached, checked while the phone rings |
| `/phone-speech` | a WebSocket, for a call the phone hears and speaks itself (the last section) |

The hub also serves its portal (`/portal/`). Calls and sessions come only from the agents,
over their links.

## The WebSocket links

Version 1. A hub refuses a hello with another version.

- The hub listens on the same address as its HTTP endpoints. `GET /speech` and `GET /agent`
  upgrade to a WebSocket. The request carries `Authorization: Bearer <key or token>`: the
  part's own key, or one of the tokens in the hub's config (`[tokens] speech = "…"`,
  `agent = "…"`), if it has them; the hub answers 401 to anything else. A part that links
  with its key is named as it paired, whatever its hello says.
- A part gets its key by pairing: `POST /pair-link` with `{"code": "…", "kind": "agent" or
  "speech", "name": "…"}` trades a one-time code made for that kind (valid 10 minutes, once:
  the portal's Setup page, or `ringback-hub pair mac` or `node`) for `{"key": "…"}` (64 hex
  characters), or 403 `{"error": "pairing code not valid"}`. Pairing again under the same
  name replaces the key; forgetting the part on the portal drops it, and unpairing it there
  drops the key and closes its link.
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
| hub → node | `{"type": "hear", "stream": 7, "sensitivity": 0.5, "gate": 0.5}` opens it, with the app's interruption sensitivity and background filter (0 = off), mapped to Silero's thresholds as `speech/internal/hearing/params.go` does |
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
| agent → hub | `{"type": "place", "id": 1, "call_id", "project", "subject", "session_id", "question": false}`, to place a call or ask again about a held one. Reply: `{"ok": true}` (it rings), `{"ok": true, "held": true, "held_until": "…"}`, or, while no speech node is linked, `{"ok": true, "held": true, "reason": "no speech"}`: it waits, and the hub sends `reask` once a node links. One call rings at a time: while another rings or is on, or waits before it, `"reason": "in turn"`, and the hub sends `reask` when it's its turn; a call you didn't answer, `"reason": "missed"` with `held_until`, its next try, or after its last `"reason": "call back"`, until you call its session |
| hub → agent | `{"type": "reask", "call_ids": [...]}`: you took calls, a node linked, or it's a call's turn; the held ones among them ask again now |
| hub → agent | `{"type": "missed", "call_id", "reason": "missed", "held_until": "…", "tries": 1}`: the phone declined or missed the call, or said nothing of it within 75 s; it's held to ring again at `held_until`, or with `"reason": "call back"` and no time after its last try |
| agent → hub | `{"type": "drop", "call_id"}`: the agent forgot a held call, as when you called its session yourself: it no longer waits, and the next in turn can ring |

### The phone's requests

The hub passes each to the agent that placed the call, or that lists the session, and answers
the phone with the reply's `status` and `error`.

| Direction | Message |
|---|---|
| hub → agent | `{"type": "offer", "id": 2, "call_id", "kind": "ring" \| "now" \| "back" \| "contact", "session_id" (for contact), "ringing": true}`. Reply: `{"ok": true}` (the call goes ahead; the agent readies its Haiku) or a refusal (`403 unknown call`, `403 not listening`…). The phone's offer itself carries the sensitivity and background filter, which the hub keeps for the hearing stream |
| hub → agent | `{"type": "claude_app", "id": 3, "call_id"}`, from `/claude-app`. Reply: ok, or `403 not waiting`, `409 not listening`, `409 no question` |
| hub → agent | `{"type": "cancel", "id": 4, "call_id", "by": "phone"}` (or `"portal"`), from `/cancel` or the portal: the agent forgets the waiting call, settles what it asked as `cancelled`, tells the session and sends `drop`. Reply: ok, or `403 not waiting` |

### What sessions show you

Not over the link: the agent posts it to `/show` over HTTP or HTTPS, with its key or token as
`Authorization: Bearer …`, as multipart form data. First `meta`, JSON: `session_id`,
`project`, `kind` (`image`, `pdf`, `video` or `model`), `name` (the file's) and `caption`,
and a model's `model`, the facts its renderer printed (`render/README.md`); then `file`, the
file itself, `poster`, a picture of it, and `thumb`, a small JPEG for the app's lists, each with
its `Content-Type`. A PDF, a clip and a model need a poster, every one a thumbnail, and a model
has no file: it stays on its computer. Files up to 100 MB, posters up to 10 MB, thumbnails up
to 1 MB. Reply: `{"ok": true, "id", "phones": [...]}`, a line per phone notified;
or 400 with `error`, 401 if the key or token is wrong.

The hub pushes each paired phone an alert with `"mutable-content": 1`, the project as its
title and the caption as its body, `thread-id` the session, and `"shown": id`, `"session_id"`,
`"kind"` and `"part"`: `poster`, or `file` for an image without one, which the app's
notification service extension fetches from `/shown/file` and attaches.

### A model's live view

| Direction | Message |
|---|---|
| hub → agent | `{"type": "view", "id": 5, "view_id", "shown_id", "width", "height"}`, from `/view`. Reply: ok once its renderer's stream has started (`serve`), or `404 not shown from here`, `409` (no renderer, and what's missing), `410` (the model is gone, and where it was) |
| agent → hub | binary: `V`, the view's ID (36 characters), then a frame, H.264 in Annex B, which the hub sends the phone on its video track |
| agent → hub | `{"type": "view_message", "view_id", "message"}`: what the renderer says, `ready` and its state, for the phone's data channel |
| hub → agent | `{"type": "view_input", "view_id", "input"}`: the phone's camera, frame or play, and `{"keyframe": true}` when the phone lost a picture or has just connected |
| hub → agent | `{"type": "view_end", "view_id", "why"}`: the phone closed it, or its connection failed |
| agent → hub | `{"type": "view_ended", "view_id", "why"}`: its renderer stopped, and why if it failed |

### During a call

The agent numbers its replies in a call 1, 2, 3…, taking the next number the moment it starts
one (the opening, a reply to a turn, one to a note), so a reply cut off while it's still being
written can be told from the next.

| Direction | Message |
|---|---|
| hub → agent | `{"type": "answered", "call_id"}` when you answer (`/answered`, or an offer without ringing) |
| hub → agent | `{"type": "connected", "call_id"}` when the audio connects; the voice may speak once both have come |
| agent → hub | `{"type": "say", "call_id", "reply": 3, "text": "…"}`: the reply's text as it's written, in any slices (the agent sends a sentence at a time); the hub cuts it into spoken pieces |
| agent → hub | `{"type": "said", "call_id", "reply": 3}` at the end of a reply; a reply with nothing to say (only a tool call) is just this, and the hub says `idle` once the voice is |
| hub → agent | `{"type": "heard", "call_id", "reply": 3, "text": "…"}` as reply 3 plays: everything of the current reply you've heard so far (for what's held until you've heard it announced) |
| hub → agent | `{"type": "interrupted", "call_id", "reply": 3, "heard": "…"}` when you cut the voice off: what you'd heard of reply 3; the rest is never spoken, and the hub drops whatever more comes of it, so the agent stops writing it. You also cut off a reply to your turn that hasn't come yet: `reply` is then the next number and `heard` is empty |
| hub → agent | `{"type": "turn", "call_id", "text": "…", "interrupted": true}`: your words, for the next reply, which the agent starts at once. `interrupted` says you cut the voice off since your last turn (or a reply that hadn't come yet), or it hadn't finished speaking once yet. Empty when a stop held no words the recogniser caught: if it interrupted, the voice carries on (the `noise` prompt in `agent/texts.go`), once in a row; otherwise it goes unanswered, and its reply is just `said` |
| hub → agent | `{"type": "speaking", "call_id"}` when the voice's audio starts playing; the first one is the opening, which the session's notes wait for |
| hub → agent | `{"type": "idle", "call_id"}` when the voice has finished speaking and you aren't speaking. A note from the session (`[The session: …]`, a new ask) waits for it, then runs the next reply |
| hub → agent | `{"type": "quiet", "call_id"}` once, 8 s after `idle` if you haven't started speaking since. If the session waits for your answer to something put to you, the voice asks again, once for each (the `reask` note in `agent/texts.go`) |
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

## The mod and the agent (the socket)

Each session's mod talks to the agent on its own Mac over HTTP on a Unix socket,
`~/.local/state/ringback/voice.sock`, which only you can open (the host in URLs is `voice`).
JSON bodies; `agent/api.go` has the details.

| Request | What it's for |
|---|---|
| `POST /calls` | places a call with a session's briefing: `session_id`, `project` and `briefing`, and the session's `cwd` and the call's `subject`. Outside your work hours the answer has `held_until`, when it can ring |
| `GET /sessions/{id}/messages?project=…&cwd=…` | collects what waits for the session, once a second, which also tells the agent the session listens: each message has a `kind` (`message`, what the voice passed on; `outcome`, how a call went), its `text`, and the call's `transcript` if there is one |
| `POST /sessions/{id}/news` | the session's answer: `{"live": true}` if the voice told the call you're on, false if the session should call you |
| `POST /sessions/{id}/permissions`, `POST /sessions/{id}/questions` | asks your permission for a tool call, or asks you questions with their options. It's put to the call you're on with the session (`"call": "live"`), to one placed to ask it (`"placed"`), or held outside your work hours (`"held"`, with `held_until`), and answered with its `id`; 502 if no call can be placed |
| `GET /sessions/{id}/{permissions\|questions}/{ask}?wait=S` | waits up to S seconds (at most 25) for the answer: a permission's `verdict` (`allow`, `deny`, `none`, `terminal`, `cancelled` or `pending`), or the questions' `status` (`answered`, `none`, `terminal`, `cancelled` or `pending`) and `answers`, with what you `said`, and `"held": true` while its call is held. `none`: you hung up without answering. `terminal`: you sent the held call's question to the Claude app, so the session asks in its own dialog. `cancelled`: you cancelled the waiting call, so the session asks there too |
| `DELETE /sessions/{id}/{permissions\|questions}/{ask}` | withdraws it: the session no longer waits for an answer |
| `POST /sessions/{id}/show` | shows you a file, from the mod's `show` tool: `path` (absolute, or in `cwd`), `caption`, `project` and `cwd`. The agent makes its poster (a model's with its renderer) and thumbnail and sends them to the hub (`/show`). The answer is the hub's, with its `id` and `phones`, or `"sending": true` if it's still on its way after 25 s; 400 with `error` if it can't be shown (its kind, size, or it can't be read), 502 if the hub can't be reached or refuses it |

`POST /sessions/{id}/messages` and `POST /sessions/{id}/{permissions|questions}/{ask}` leave a
message or record an answer as the voice would, for tools.

