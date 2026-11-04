# ringback — design

How ringback works, why it's built this way, and what's left to do. `docs/setup.md` installs
it, and `docs/protocol.md` specifies how its parts talk to each other.

## Goal

Your Claude Code sessions call your iPhone when they finish or need you. Each one tells you
what it did, and you talk it through hands-free, with the phone locked in your pocket and on
mobile data. It all runs on your own machines and your Claude subscription: no API key, and
no server of anyone else's in between, apart from Apple's push service.

## What a call does

1. **It rings like a phone call.** A locked phone rings through CallKit, in your headphones
   too, with the project's name as the caller and a few words on what the call is about.
2. **It's a conversation.** You can interrupt mid-sentence: the voice stops the moment it
   hears you, waits until you've finished, and answers everything you say, a short "mm-hmm"
   included.
3. **One session per call.** The voice opens with what's new, then what needs you. What you
   want done goes to that session, and its answer comes back to the call. If you've hung up
   by then, or hung up without a word before you'd heard it, the session calls again; not
   after a "bye".
4. **You approve by voice.** A permission prompt or a question the session asks you comes to
   the call. A clear yes or no settles a permission; a question takes one of its options or
   your own words. The terminal shows the prompt at the same time, and whichever you answer
   first decides.
5. **Only while you're away.** `/ringback` switches calling on in a session, and off again
   when you're back.
6. **It keeps your hours.** Outside your work hours, or while you've put calls on hold,
   calls wait in the app and ring when you take calls again. A question that waits can go to
   the Claude app instead.
7. **You can call back.** Any call in your history, and any session with ringback on, is a
   tap away in the app.
8. **A better model on request.** Ask for one and Opus takes the call over, with everything
   said so far.
9. **It understands you.** Recognition copes with non-native accents. When a reply is
   unclear, the voice asks instead of guessing, and a permission is granted only on a clear
   yes.
10. **The session is told how it went.** After each call it gets a note: answered and for how
    long, or not answered. The call's transcript is in the project.

## The parts

```
  Mac with Claude Code sessions        always-on machine             iPhone
  ┌───────────────────────────┐        ┌────────────────────┐        ┌─────────────────┐
  │ session ── ringback mod   │        │ hub                │◀───────│ app             │
  │               │ socket    │  link  │  push, hold, work  │  HTTP, │  CallKit, audio │
  │               ▼           │───────▶│  hours, waiting    │  call  │  Recents,       │
  │ agent ── Haiku / Opus     │        │  calls, sessions,  │  audio │  Sessions, hold │
  │          (claude -p)      │        │  the call's audio  │        └─────────────────┘
  └───────────────────────────┘        │  and timing,       │                ▲
                                       │  portal            │── Apple push ──┘
  machine with a GPU                   └────────────────────┘
  ┌───────────────────────────┐  link            ▲
  │ speech node: Silero,      │──────────────────┘
  │ Smart Turn, Parakeet,     │
  │ Kokoro                    │
  └───────────────────────────┘
```

| Part | Runs on | Does |
|---|---|---|
| **app** (`app/`, Swift) | an iPhone on iOS 26 or later | rings through CallKit when a VoIP push comes; carries the call's audio to the hub (WebRTC, Opus), or with On-device voice hears and speaks the call itself; Recents, Sessions and the hold switch; its settings are in the Settings app |
| **hub** (`hub/`, Go) | one always-on machine | the phone's endpoints; pushes through Apple (APNs); work hours, the hold and the calls that wait; the sessions you can call; each call's audio (WebRTC through Pion, Opus through libopus) and its timing; the portal |
| **speech node** (`speech/`, Go with sherpa-onnx) | a Linux machine with an NVIDIA GPU | hearing: Silero VAD v5, Smart Turn v3.2, Parakeet TDT 0.6B v2; speaking: Kokoro v1.0 with the `af_heart` voice |
| **agent** (`agent/`, Go) | each Mac that runs sessions | the mods' socket; each call's state; the call's Haiku, and Opus on request, as `claude -p` on your Claude subscription; the call transcripts |
| **mod** (`mod/`, TypeScript) | each Claude Code session, as a plugin | `/ringback`; has the session write its call briefing; brings what you say on a call to the session as a prompt; puts permissions and questions to the call |

**Who dials whom.** The agent and the speech node each open one WebSocket to the hub, with a
token from the hub's config, and keep it open. The phone makes requests to the hub and
connects the call's audio to it. The hub pushes to the phone through Apple. Nothing dials the
Macs, the speech node or the phone, so only the hub needs an address the others can reach.

The hub is built for macOS (`hub/build.sh`: one binary for Intel and Apple silicon, libopus
linked in, needing nothing installed). Its code has nothing Mac-only in it, but it hasn't been
built or run on Linux. The agent runs where the sessions run, on a Mac.

### Who does what in a call

- **The hub owns the call's timing:** the audio both ways; when you start and stop speaking,
  from the speech node's events; stopping the voice the moment you interrupt; cutting each
  reply into pieces to speak; and how much of each you've heard.
- **The agent owns what's said:** the call's state (placed, held, answered, remembered for
  calling back), Haiku and Opus and their tools, holding what's passed on until you've heard
  it announced, the transcript, and the note to the session afterwards.
- **The speech node runs the models:** it hears your audio and says when you speak and what
  you said, and turns text into speech. It has no state of its own about calls.
- **The mod is the session's side:** the briefing, the session's prompts and answers, and
  what the session asks you.

## A call, step by step

1. **The briefing.** When a turn ends in a session with ringback on, the mod has the session
   write its call briefing in one more turn: a first line "Subject: " with 3–8 words, shown on
   the phone as it rings, then at most 300 words written to be spoken: what's new, what needs
   you, and what the voice needs to answer likely questions. The session has its whole
   context and a warm prompt cache then, so this turn is quick and cheap. The mod gives the
   briefing to the agent on its Mac. If you're on a call with that session already, the
   briefing goes to the call as news instead.
2. **Placing the call.** The agent makes a random call ID, which is the call's secret, and
   asks the hub to ring. The hub either holds the call (outside your hours, on hold, or with
   no speech; see the next sections) or rings: a VoIP push to every paired phone with the
   call's ID, project, subject and session, and a word to the speech nodes to load their
   models (about 2.2 s, while the phone rings). The agent starts Haiku on the briefing at
   once, so its opening is written while the phone rings, as a person thinks of what to say
   while the other side picks up.
3. **Ringing.** The phone rings for 30 s. Meanwhile the app reaches the hub (`/ping`, each
   try waiting 3 s, then 6, then 12) and makes its WebRTC offer, marked as ringing, with all
   its addresses in it. The hub asks the agent that placed the call to accept it, and the
   audio connects before you answer. Until you do, the phone sends no microphone and the hub
   plays nothing. If the offer couldn't go while it rang, it goes once you've answered. A call
   whose audio hasn't connected 20 s after you answer ends: the phone says why on the call
   ("Ringback can't reach your hub. Check that your VPN is on."), then in a notification and
   on the app's banner. If you hang up first, it still says why.
4. **Answering.** The app tells the hub (`/answered`), and the voice speaks its opening.
5. **Hearing you.** The hub decodes your audio and sends it to the speech node at 16 kHz.
   Silero says when you start and stop speaking; Smart Turn says when you've finished, and a
   pause of 1.5 s ends your turn anyway; Parakeet writes down what you said, and when it writes
   nothing for a stretch of speech, writes it down once more from just before it began (it
   misses a short "Yes." with a second of audio before it). Your words reach the hub about
   0.3 s after you stop (`speech/README.md`).
6. **The reply.** The hub gives your words to the agent, and Haiku answers in a Claude Code
   session that stays open for the whole call, so each turn sends only what's new. Its first
   words come about 1.2 s after your turn (`tools/reply-speed`). The agent passes the reply
   to the hub a sentence at a time as it's written, the hub cuts it into pieces, the speech
   node speaks each, and the hub plays them.
7. **Passing it on.** What you want done goes to the session (Acting on the call, below). The
   mod collects it once a second and submits it to the session as a prompt; the session's
   answer comes back to the call as a note, and the voice tells you once it has finished
   speaking and you aren't. If you hang up before you've heard a reply that came after it to
   its end, the session calls again, with its answer as the briefing; once. Not if you spoke
   once the voice had started on it: a "bye", or anything else you said over it, makes hanging
   up your choice. Noise that cut the voice off doesn't count, once its turn comes back with
   no words.
8. **The end.** Either side hangs up. The agent finishes the transcript and leaves the
   session a note on how the call went: answered and for how long, with the transcript's path;
   not answered within 90 s; or answered but the audio never connected. The note is added to
   the session's conversation without starting a turn, so it can't set off another call, and
   it says it's from ringback: unsigned, the session took it for your words.

### Speaking a reply

The first sentence is spoken on its own as soon as it's complete, so the voice starts fast.
Then, one second before the piece that's playing ends, everything Haiku has finished since,
up to its last complete sentence, becomes the next piece. Larger pieces keep the intonation
and pauses natural; one sentence per piece left about 0.75 s of silence between sentences.
The speech node makes a sentence's first audio in about 0.12 s (`speech/README.md`), well
inside the one second.

### Interruptions

When you start speaking while the voice talks, the hub stops it at once, drops every piece
not yet played, and tells the agent how much of the reply you heard, word for word. Haiku's
session already holds the whole reply it wrote, so its next message says where you cut it
off. The voice then waits until you've finished and answers what you said.

Two settings in the app decide what counts as you speaking, and go with each call's offer:
- **Interruption Sensitivity** sets how sure and how long speech must be (0.1 s at the most
  sensitive, 0.35 s at the least): turn it down in noisy places.
- **Ignore Background Noise** is a level gate: speech quieter than a threshold doesn't start a
  turn or stop the voice. With AirPods or the phone at your ear, other people are much farther
  from the microphone than you, so they arrive quieter. Its default kept out another voice
  20 dB below yours, offline. It decides when a turn starts, not what's heard in it. It
  measures the loudness of the speech Silero hears, over as much of it as has come, up to
  400 ms: Pipecat's measure, over the last 400 ms with the silence before the speech and
  smoothed again, rose only as a short word ended, so a loud "yes" never started a turn
  (`tools/short-answers`).

Before that, the phone's voice processing removes echo and background noise, and Voice
Isolation (switched on once in Control Center) more of it.

If something stops the voice but no words are recognised, as with a cough, the voice carries
on from the start of the sentence it was in (once in a row).

### Acting on the call

On a call for a session, Haiku has three tools, served by a small MCP server the agent starts
with each `claude -p`: `pass_to_session` for what you want the session to do or know, and
`answer_permission` and `answer_question` for what the session asks you. Every call also has
`better_model`.

- Haiku says what it's passing on, or what it will answer, then calls the tool. The message
  is held until you've heard the voice say so, and dropped if you cut it off before; Haiku is
  then told, and passes it again if you still want it. A tool called before any words waits
  for the sentence after it. If Haiku reaches for a tool before saying anything, the voice
  says "Okay." at once, so you aren't left in silence.
- What Haiku writes after a tool isn't spoken if it said something before: it tends to repeat
  itself. Nor is a sentence that names one of its tools: on a call, Haiku once said the call it
  was about to make ("Then call answer_permission with allow true"). So the agent passes a
  reply on a sentence at a time, which costs no time: the hub speaks only whole sentences.
- Before you've said anything, `pass_to_session` passes nothing: an opening written while the
  phone rings has nothing of yours to pass on.
- The voice does no work itself. Whatever you want written, changed, run or looked up goes to
  the session, even something short. It answers from the briefing only what the briefing
  says.
- When several things need your answer, it says how many, asks them one at a time, and passes
  the answers on together.
- A permission: Haiku says in plain words what the session wants to do, and whether it
  deletes, overwrites, pushes or spends anything, and takes only a clear yes or no ("I said
  yes" counts). A question: it reads it and its options, and takes your choice or your own
  words; it never chooses for you.
- If you say nothing for 8 s after the voice has finished while the session waits for your
  answer, the voice says it may not have heard you and asks again, once for each thing asked.
  On a call, a "yes" to a permission went unheard, and the voice waited 35 s in silence.

The voice's instructions are in `agent/texts.go`. Its style: short sentences, rounded
numbers, nothing read out that's meant to be seen (tables, lists, code), detail offered
rather than recited, and your words read in context, since speech recognition gets
similar-sounding words wrong, technical ones especially.

### Call transcripts

The agent writes each call's transcript into the calling session's project, under
`.ringback/calls/`: the briefing, your words as recognised, what the voice said and where you
cut it off, and what went to the session. The folder holds its own `.gitignore` of `*`, so
git ignores it without a change to the project. Each message from the call ends with the
transcript's path, and reading a file inside the project needs no permission prompt, which
while you're away would mean another call.

## Where a call's speech runs

Each call uses the first of these:

1. **A speech node linked to the hub.** If several are linked, the hub picks one; for a call
   that rings, it warms them all. Between calls a node holds nothing on the GPU: a small
   process keeps its link (16 MB), and a worker process with the models (2.8 GB of memory,
   2.9 GB of the GPU's) starts when a call is coming and exits after 60 s without one
   (`speech/README.md`).
2. **The phone itself**, if On-device Voice is on in the Settings app. The hub refuses the
   call's offer for want of a node (503, `"no speech node"`), and the app opens its own link
   to the hub for the call instead (`/phone-speech`, `docs/protocol.md`). The app hears you
   with Apple's on-device recognition (SpeechTranscriber) and a level detector of its own,
   and speaks with the best English voice installed on the phone, through the same audio
   engine, so echo cancellation hears it. Only text crosses the network, and the hub counts
   what you've heard from what the phone says it played.
3. **Neither:** the call doesn't ring. It waits, as a held call does, with the reason; the
   phone gets one notification ("\<project\> wants to talk"), and the app lists the call as
   ringing when the speech computer is back. When a node links, or the phone's own voice is
   turned on, the hub has the agents ask again, and the call rings.

Apple's recognition is the fallback, not the default, because it gets more words wrong: 10.3%
of the words on your own recordings, against Parakeet's 5.9% (`tools/speech-trial`,
`tools/stt-eval`), answers to permission prompts among them ("allow it once" as "I love it
once"). It did hear in the background with the screen locked, with its final words 0.15 s
after they were asked for, and the app stayed at about 30 MB: the model runs in a system
service.

Nothing about speech nodes is set in the app. A node links to the hub by itself, with its
token; adding one, or forgetting one that's gone for good, is done on the hub's portal.

## When calls ring

**The hold.** Each phone has a hold switch, kept by the hub: on hold, or taking calls. While
every phone is on hold, no call rings, whatever speech there is: calls wait instead. You set
it with the pill beside the app's tabs, with its control in Control Center (also for the Lock
Screen or the Action button), or on the portal, which sets every phone's at once. On hold the
pill breathes in indigo with the number of calls waiting, the app's icon shows that number,
and a Lock Screen widget says when calls are taken again.

**Work hours** are set in the Settings app (From and To in 15-minute steps; From later than
To runs past midnight, and the same time means any time). They set the hold switch at their
edges: on hold when they end, taking calls when they start. Between edges, what you set
stands, as a scheduled Focus you turn off by hand stays off until its next start. The app
sends its hours and time zone with each registration and connection check, so a change
reaches the hub within half an hour at most. Days of the week aren't covered.

**A held call.** The hub answers that the call is held, and until when, if work hours will
end the hold. The agent keeps the call, without starting Haiku, and asks again then, or
after 10 min if that's sooner, so hours you widen apply within 10 min; it waits on the wall
clock, so a Mac that slept still asks on time, and keeps its Mac awake meanwhile
(`caffeinate`). A new briefing from the same session replaces the held call's subject and
briefing rather than placing a second call. The hub keeps the calls that wait, and drops one
whose agent hasn't asked about it for 25 min, so a call from a Mac that's gone doesn't wait
forever. When you take calls, the app asks the hub to have them ask again (`/reask`), and they
ring at once, together.

**Waiting calls in the app** show at the top of Recents, under Waiting, in orange, with when
they ring ("Rings tomorrow at 08:00"). Tapping one calls it now: an outgoing call that
doesn't ring, whose voice opens with the briefing and knows you called while it waited.

**A call you don't answer.** The agent forgets it after 90 s and tells its session. The hub's
log tells a decline from a missed call; the agent can't. Calls aren't retried yet, and two
sessions calling at once both ring (Next).

**Calling back.** Tapping any call in Recents calls its session straight away, without
ringing, answered calls too. The agent remembers each call for a day, across restarts, and
the call back gets a fresh Haiku with that call's briefing: for a call you missed, it opens
with what it was about; for one you took, with what changed since, or says nothing has and
asks what you want. If the session wrote a newer briefing since, that follows. If the session
isn't listening any more, the call still connects, but the voice can't pass anything on and
says so. Work hours don't apply: you called. If the agent no longer knows the call, the app
calls the session from Sessions instead, if it's there.

**During another call** (phone, FaceTime, WhatsApp), a ringback call rings as call waiting,
with iOS's own Hold & Accept, End & Accept and Decline.

**The connection check.** If the phone can't reach the hub (its VPN off, say), calls ring
but can't connect. So every 30 min the hub wakes the app with a background push, and the app
checks in with the hub. If three tries in about 25 s fail, the app shows a notification
("Claude can't call you"), again after 3 h if the problem stays, and a banner in Recents until
the hub is reachable.

## What a session asks

**Permissions.** When Claude Code is about to ask your permission while ringback is on, the
mod puts it to you on a call as well (`PermissionRequest`, which fires only when you'd be
asked, so auto mode's own approvals never ring). The prompt shows in the terminal at the
same time, and whichever you answer first decides. On a no, the session reads your words; a
yes shows as a dim line.

**Questions.** When the session asks you something with options (AskUserQuestion), the mod
holds the question, puts it to you on a call, and gives your answer back as the dialog's own,
so the dialog doesn't show.

**Where it goes:** to the call you're on with that session, as a note; to its call that's
ringing, once you answer; or else to a new call whose briefing asks it. If you don't answer
the call, it waits for the next call with that session you do answer, a call back or the
session's next call. After 15 min without an answer, or when you hang up without one or
switch ringback off, the terminal asks instead.

**Outside your hours** a question waits for the held call, and its 15 min count from when the
call rings. Meanwhile the phone gets one notification ("\<project\> has a question"), as an
ordinary notification your Focus can silence, and the waiting call shows it ("Question ·
rings at 08:00"). Swiping it ("Claude App") sends the question to the Claude app instead: the
agent tells the mod to let the session's dialog show, Remote Control shows it in the Claude
app, and the held call is forgotten and never rings. A permission's prompt shows in the
terminal anyway, and through Remote Control in the Claude app too.

**Remote Control.** Switching ringback on also runs `/remote-control`, and the rest takes it
as on: no mod API says whether it is, and the variable Claude Code gives hooks while it is
doesn't reach a mod (`tools/env-check`). Switched off since, a question sent to the Claude app
waits in the terminal's dialog.

## A better model on the line

When you ask ("can I get a better model on the line?"), Haiku says so and calls `better_model`,
which is held until you've heard it, like the other tools. Then Haiku's process is closed and
its session resumed with Opus (`claude -p --resume <id> --model opus`), with the same tools, so
Opus has the whole conversation, tool calls included. Opus is told it's taking over and speaks
first. From your request to Opus's first words took 6–7 s offline (`tools/handover-check`, in
git history): Haiku's short line, then a few seconds of silence. Opus's first turn is mostly
uncached and costs about as much of your usage as ten later turns, once. If Opus isn't
available (a usage limit, or a plan without it), the voice goes silent; there's no way back to
Haiku.

The call's Claude Code session runs in a private folder of the call's, and that folder, with
Claude Code's own record of it under `~/.claude/projects`, is deleted when the call ends, so
calls don't pile up there or show in `/resume`.

## The Sessions tab

The app's Sessions tab lists your sessions with ringback on, on every Mac, a card each: the
project, its latest briefing's subject and when, or since when ringback has been on, and a
call of the session's that waits. A session listens while its mod collects from the agent,
once a second; one that hasn't for 30 s isn't listening. Each agent tells the hub which of its
sessions listen whenever that changes, and every 30 s; the hub drops an agent's sessions when
its link drops, as when its Mac sleeps. The app fetches them every 3 s while the tab shows.

Tapping a card calls the session at once, with a call ID made on the phone. The voice has the
session's latest briefing and opens with what's new; with no briefing yet, it says it knows
nothing of the session's work and asks what you want. A session that stopped listening
meanwhile refuses the call, and the app says so.

## Staying up

- **The agent** writes what it keeps to `voice-state.json` beside its socket
  (`~/.local/state/ringback/`), readable only by you, on every change, and reads it at start:
  held calls with their briefing and what they ask, calls to call back, what missed calls
  asked, what sessions asked and wait for (under the same IDs, so a mod's long poll carries
  on), and the messages and notes waiting for each session. A call that was ringing or on
  during a restart has ended: its session is told so, and it can be called back.
- **The hub** keeps its state in `~/.local/state/ringback/`: the phones (`devices.json`), the
  calls that wait (`waiting.json`), the sessions you can call (`contacts.json`), the calls'
  events (`events.jsonl`), the parts it has seen (`seen.json`) and the portal's account
  (`portal-user.json`). A restart ends the calls that are on, and signs everyone out of the
  portal. What each call is about is kept in memory, so the portal shows calls from before a
  restart without their project.
- **The links** come back by themselves: the agent and the speech node link again after 1, 2,
  4… up to 30 s.
- **The mod** keeps ringback on through a reload of the mod, and when you resume the session
  after Claude Code restarts (`claude --resume`), for 30 days. While the agent is down, it
  offers a briefing again every 30 s for up to 30 min, a newer one taking its place, and the
  line under the prompt says "☎ waiting for the ringback agent…"; what the session asks is
  tried again every 5 s within its 15 min.
- **As services:** each part sets itself up as one with `install`: the hub and the agent as
  launchd agents (at login), the speech node as a systemd user unit (at boot, with lingering
  on), each started again whenever it stops. `install` refuses a config without the address
  or the token, and a copy of the part already started by hand.

## Weak signal

On a weak signal, lost and late packets hurt more than bandwidth, so the call's audio is set
up to lose little and recover the rest (`hub/opus.go`, `hub/call.go`):

- **Opus in 20 ms packets.** The hub sends mono at 24 kbps with Opus's forward error
  correction (FEC: a rougher copy of each packet in the next), set for 10% loss. Its answer
  asks the phone for the same: 24 kbps, only the band speech recognition uses (it hears at
  16 kHz), and FEC (`useinbandfec=1;maxaveragebitrate=24000;maxplaybackrate=16000`). Limited
  to that band, Opus adds FEC from 1% loss on.
- **Recovering your packets.** A jitter buffer passes each packet on as soon as those before
  it have come, and gives up on a missing one once 3 later ones have. libopus rebuilds a lost
  packet from the next one's FEC, or else conceals it; a gap over a second is left out.
- **Patient connection checks.** WebRTC checks during a call that the other side is still
  there; the hub ends the call only after 15 s without an answer. With tighter limits, a call
  on a network slower than about 0.5 s round trip ended half a minute in (`tools/ice-check`,
  in git history).
- **Few addresses.** The hub offers only the address it listens on, over UDP and IPv4, and the
  phone only its VPN's addresses (all its addresses if the VPN gives none), so WebRTC doesn't
  try pairs that can't connect. With every address offered, audio connected 2.1–2.4 s after
  the offer; with only the VPN's, 0.94 s (one call on mobile data).
- **Reaching the hub.** After the phone moves from Wi-Fi to mobile data, its VPN tunnel can
  stay stale for 10–20 s. The app's tries to reach the hub while it rings send fresh packets,
  which bring the tunnel back sooner, and wait longer each time (3, 6, 12 s), so a slow network
  still gets through.
- **Measured.** The headers alone cost about 50 kbps each way (36 bytes of RTP and SRTP and
  about 88 of UDP, IP and the VPN per packet, counted, not measured), most of the traffic: a
  call is about 0.9 MB a minute. Each call's "ended" report from the phone carries its WebRTC
  stats (bytes, packets, loss, jitter, how much of the voice it made up, round trip), kept in
  the hub's `events.jsonl`.

These settings were chosen by measuring an earlier implementation of them in Python
(`tools/opus-check` and `tools/ice-check`, in git history). Offline, with 5% and 15% of your
packets lost, 1.7% and 1.9% of your words were heard wrong (1.4% with none lost), against
21.9% and 73.3% without FEC and concealment. On the phone, calls held up on 3G and on the
Network Link Conditioner's Very Bad Network (about 10% lost each way, a 1 s round trip). The
hub's implementation hasn't been tried on a weak network yet (Status).

## The network

Every part reaches the hub at one address on a private network: the phone, from anywhere and
on mobile data too; each Mac's agent; and each speech node. The hub listens there for HTTP
and its links (`listen` in `hub.toml`, with its port: 8787 in the examples, and the app's
default), and the call's audio uses the
same address on UDP ports it picks. The hub also needs the internet, to reach Apple's push
service.

| From | To | What |
|---|---|---|
| phone | hub | requests (`docs/protocol.md`, "The phone"), the call's audio (WebRTC over UDP), or with On-device voice its text link |
| agent | hub | one WebSocket (`/agent`) with the agent token |
| speech node | hub | one WebSocket (`/speech`) with the speech token |
| hub | Apple | pushes (HTTPS) |
| Apple | phone | VoIP pushes that ring, background pushes for the connection check, notifications |
| mod | agent | HTTP over a Unix socket on the same Mac |

**Nebula** is what ringback is tested on, with the hub's machine and the phone on the same
Nebula network, and a lighthouse that acts as a relay (`relay.am_relay: true`). Only the
phone can start a tunnel to itself: nothing can open one to a phone on mobile data, and
direct tunnels to it failed, so the phone's traffic goes through the relay. That's why the
phone always dials the hub, and never the other way. Another VPN with the same property (the
phone reaches the hub from anywhere) should work, but hasn't been tried.

**A public address isn't supported yet.** Over plain HTTP, the portal's password, the links'
tokens, the phones' keys and the calls' IDs would cross the internet unencrypted (Security).
The hub can serve HTTPS and WSS instead (`[tls]` in `hub.toml`, `docs/setup.md`), but the rest
a public hub needs is still to do (Next): the Macs and speech nodes paired, and the call's
audio on the public address.

## Security

The private network is the outer wall: anything on it can reach the hub. Inside it:

- **The links.** An agent or a speech node links only with its token (`[tokens]` in
  `hub.toml`, sent as `Authorization: Bearer`); anything else gets 401.
- **The phone has no token.** A call's ID is the call's secret: random, at least 16
  characters, and it reaches the phone through Apple's push. The hub accepts an offer only
  for a call the agent that owns it knows. A session's ID is a secret of the same kind: with
  it, a call to that session can pass it messages, which it takes as prompts.
- **Calls and sessions come only from agents,** over their links, with the agent token.
- **A phone pairs before the hub trusts it.** The portal, signed in, makes a one-time pairing
  code (32 random bytes, valid 10 minutes, kept only as a hash, in a file only the hub's user
  can read), which opens Ringback as a link (`ringback://pair`); `ringback-hub pair` makes one
  on the hub's machine. The app trades it at `/pair` for the phone's own key (32 random bytes),
  which it keeps in its Keychain, on that device only; the hub keeps only the key's SHA-256,
  with the phone. Every request that names the phone by its push token must carry the key,
  compared in constant time, or gets 401: so only your phones register, get the IDs of the
  waiting calls and the sessions (`/waiting`, `/contacts`), set their hold, or are rung and
  pushed to. The pairing page's sign-in comes back only to a page of the portal.
- **The portal** has one account, made on the hub's machine (`ringback-hub portal-user NAME`):
  a password of at least 10 characters, hashed with PBKDF2-SHA256 at 600,000 iterations and
  a random salt, then a one-time passcode from an authenticator app (TOTP, RFC 6238: 6 digits,
  30 s, one step either way, no step used twice). The account is saved only once a passcode
  from the app matches, in `portal-user.json`, readable only by you. A sign-in is a random
  32-byte ID in a cookie (`HttpOnly`, `SameSite=Strict`, `Path=/portal`), kept in memory by its
  hash and ended after 12 h unused; every form carries a CSRF token; five failed sign-ins from
  an address shut it out for a minute. The pages allow no outside scripts or framing.
- **In transit.** The VPN encrypts everything between the parts. Inside it, the call's audio
  is encrypted anyway (SRTP), while HTTP and the links' WebSockets are not, unless the hub
  serves TLS (`[tls]`, TLS 1.2 or later): then the agent and the speech node check its
  certificate against the system's CAs, or a CA of your own (`ca`), and so does the phone.
- **On the hub's machine:** the APNs key (`.p8`) sits beside `hub.toml`, which also holds the
  links' tokens.
- **On each Mac:** the agent's socket and its state file (which holds call IDs) are readable
  only by you. Any program running as you could use the socket as a mod does. Transcripts stay
  in each project's `.ringback/calls/`.
- **The call's model.** Haiku and Opus run with no tools but the call's own, no other MCP
  servers, no slash commands and no settings files (whose hooks could start another call), in
  a folder of the call's outside your project, so the project's `CLAUDE.md` doesn't reach
  them.

## The hub's portal

Pages the hub serves under `/portal/` on its address, for setting up and for the rare times
something's wrong. They're plain HTML and SVG from templates in the hub's binary, refreshed
every 5 s by a small script, with no framework; light and dark, and laid out for a phone too.

- **Status** leads with a sentence on what a call would do now (ring which phone, with which
  speech, or wait and why), what to do if something's wrong, and a map of the system: the
  Macs with their sessions counted, the hub, the speech nodes (models loaded or idle), Apple
  push and the phones (with On-device voice). Each part and link is working, a problem, gone
  or not set up. A Mac that's gone is shown asleep, quietly; a speech node that's gone is a
  problem; a part you haven't set up is drawn dashed, with a link to Setup, and isn't a
  problem. A live call lights its path. To tell "gone" from "not set up", the hub remembers
  the nodes and Macs it has seen (`seen.json`); one gone for good can be forgotten. Under the
  map: the hold, the calls live and waiting, the sessions with their Macs, the phones' facts
  (last check-in, last push, work hours, On-device voice, read-only) and the nodes and Macs.
- **Calls:** the last 50, waiting ones first, grouped by day, with their events.
- **Log:** the hub's last 500 lines, newest first.
- **Setup:** how to add a speech node, a Mac, the phone and Apple push, with each link's
  address and token from `hub.toml` (hidden until shown, with a copy button).
- **Pair this phone** (`/portal/pair`): opened on the iPhone, from the app's "Pair this iPhone"
  banner or by hand. Signed out, it signs you in first and comes back. Its one button makes a
  one-time pairing code and opens Ringback with it at once (`ringback://pair?hub=…&code=…`,
  through a refresh, with an Open Ringback link should Safari not follow), and the app trades
  the code for the phone's key (Security).

The portal changes three things: **the hold**, on every paired phone at once, so the app shows
it too (work hours still end it at their next start, as they end the app's); **forgetting** a
node or a Mac that's gone for good; and the **pairing codes** it makes. Everything else has
one home: secrets and addresses in `hub.toml`, what the phone owns on the phone (shown on the
portal, read-only).

Signing in is described under Security. Passkeys would be better, but browsers offer them only
on HTTPS, and need a domain name rather than an IP address (not checked): a name of your own
with a certificate, or a private certificate authority installed on each device (Next).

## Choices, and what was turned down

**The shape of the system**
- **One hub, a speech node, an agent on each Mac.** The phone reaches one machine, never the
  Macs, which only dial out; the models run only where there's a GPU, and nothing heavy runs
  on the always-on machine. Turned down: the voice on every Mac (it needs Apple silicon and a
  phone that reaches each Mac), the voice on the always-on machine (it has no GPU), and a hub
  on every Mac (calls from two Macs couldn't queue).
- **Go for the hub, the agent and the speech node:** small, compiled, and needing nothing
  installed. A Python environment for the voice took 1.7 GB of disk, and a small Python service
  about 50 MB of memory, against the hub's few MB; C would need its own HTTP, JSON and key
  handling.
- **Natively on the speech node,** not in Docker, to keep its footprint small on a machine
  that runs other work. A Coral Edge TPU can't run Kokoro or Parakeet.
- **Claude Code stays on the session's Mac,** not on the hub: the hub needs no login, and the
  call's model runs on your subscription where Claude Code already is.

**Ringing and reaching the phone**
- **An app, not the phone network** (Twilio and the like): per-minute and number costs are
  hard to justify for a personal tool, narrowband audio hurts recognition, and the call's
  audio would need a public address. Kept as a fallback for weak data (Next).
- **The phone dials the hub,** since nothing can open a tunnel to a phone on mobile data.
- **Pairing by a link from the portal,** opened on the phone, rather than a QR code the app
  scans or a code typed in its settings: no camera access and no typing, and the portal's
  sign-in, with its passcode, is the proof. The app's banner opens the pairing page itself,
  since it knows the hub's address.
- **The connection check comes from the hub as a push,** and the app answers it. Pinging the
  phone raised false alarms (a locked, idle phone stops answering within 30 s) and tests the
  wrong direction; iOS doesn't run apps on a timer.
- **Call waiting** when you're on another call, rather than refusing the call as busy: you
  choose.

**Hearing and speaking**
- **Parakeet v2** for speech-to-text: about 6% of words wrong on accented English at about
  40 ms an utterance (`tools/stt-eval`). Whisper large-v3-turbo was about as accurate but
  12 times slower, and invented words on short clips; Qwen3-ASR 1.7B and Parakeet v3 did no
  better; Granite Speech wasn't tried (its keyword list wasn't exposed). Apple's on-device
  recognition got 10.3% wrong and takes no vocabulary, so it's the fallback only.
- **Kokoro `af_heart`** for the voice: preferred over Pocket TTS and Apple's standard voices
  in a listening test (Apple's Premium voices weren't tried). Pieces play as made: trimming
  the silence between them sounded worse.
- **A 1.5 s end-of-turn wait.** At 3 s, a short "mm-hmm" that Smart Turn judged unfinished
  took about 5 s to answer; at 1 s little room is left over the pauses in free speech (up to
  0.74 s on your recordings).
- **A level gate for other people's voices,** not a model. A model trained on a sample of
  your voice (VoxLock, kept as the tag `voxlock`) kept a voice 6 dB below yours out
  entirely, but slowed every interruption by about 0.1 s, raised Parakeet's errors from 5.9% to
  7.0%, and could mute you for a whole call. With AirPods, other people arrive quiet enough
  for a gate.
- **Settings between calls,** not during: the in-call screen covers the app.

**The voice**
- **Haiku on your subscription through Claude Code,** not the API, for a personal tool; in a
  session that stays open for the call, not a `claude -p` per turn (3.2 s each, 1.7 of it
  start-up).
- **The session's briefing as the voice's context.** The session's last message is too
  little; a fork of the whole session is slow to start and costs more on every call.
- **Tools for acting,** not tags in the reply: nothing ends Haiku's turn after a tag, and it
  went on to imagine the session's answers. Messages are **held until heard,** since one passed
  as soon as it was written had to be taken back when you cut the voice off.
- **The session writes the subject** with its briefing, rather than a separate model call.
- **The voice's model runs outside your project,** so your `CLAUDE.md` doesn't load into it
  and calls don't fill your `/resume` list; the agent writes a plain transcript into the
  project instead.
- **A call back gets a fresh Haiku,** not the missed call's, whose opening was written for a
  call the session placed.

**The session's side**
- **A mod,** not settings hooks and an MCP channel: a custom channel loads only at a session's
  start, behind a warning screen every time, so calling couldn't be switched on mid-session.
  The mod does it all from inside Claude Code: `/ringback`, the line under the prompt, prompts
  for the session, and holding tool calls.
- **A switch you turn on when you leave,** not a call after a minute idle, which would make
  every call a minute late.
- **Permissions from `PermissionRequest`,** which fires only when you'd be asked; at
  `tool.check`, auto mode's routine approvals would have rung you.
- **Told afterwards how the call went,** rather than asking the briefing what it didn't check:
  that led 6 of 8 briefings to say they couldn't confirm the call would reach you
  (`tools/briefing-check`).
- **What a missed call asked waits** for the next call you answer, within 15 min, rather than
  going to the terminal at once; and outside your hours it waits for the morning call, with a
  notification, rather than going to the Claude app at once. You choose from the app.

**The app**
- **Like the Phone app:** Recents and Sessions, with settings in the Settings app, which
  costs time pickers (lists in 15-minute steps instead), words at the ends of sliders (in the
  footers instead), and new settings reaching the hub at once (within half an hour instead).
- **Google's WebRTC library** in manual-audio mode, so CallKit owns the audio session; a
  WebRTC client that takes the audio session over clashes with CallKit.
- **One hold for the whole hub,** not a pause on a speech node: with a node paused, the
  phone's own voice would still take calls.

## Limits and risks

- **Calls don't queue or retry.** Two sessions calling at once both ring, and a call you
  don't answer is forgotten after 90 s with only a note to its session (Next).
- **The phone's tunnel can stop receiving.** Once (2026-11-03), after the phone moved from
  home Wi-Fi to mobile data to another Wi-Fi, for over two minutes its Nebula packets reached
  the hub and the hub answered, but nothing reached the phone. Moving between networks, with
  another VPN beside Nebula, a tunnel doesn't always recover by itself. Ringback can't fix it; it
  notices: a call whose audio can't connect ends, with a notification and a banner saying the
  hub can't be reached, and the connection check warns between calls.
- **A stale tunnel delays a call.** After the phone changes network, the VPN can take 10–20 s
  to reach the hub again; the app keeps trying, for up to 20 s after you answer, then ends the
  call and says why.
- **Every placed call costs a Haiku opening** on your subscription, even if you decline it,
  and keeps Haiku and the Mac awake until it's forgotten after 90 s.
- **Other people's voices and noise.** The background filter's default is an absolute level,
  and how loud each microphone delivers your voice on a call isn't measured; it leaves about
  5 dB under your quietest speech on your recordings. Gym noise or someone talking near you
  could still stop the voice or be heard as you, and the filter could swallow your own quiet
  speech. If that proves fragile, the next step is a gate set relative to your own level on
  the call.
- **Short words can go unheard or misheard.** On calls, "wait" said while the voice talked
  wasn't heard (Silero needs 0.2 s of speech), and short "yes" answers to a permission weren't:
  the background filter's volume lagged behind a short word, and Parakeet wrote nothing for a
  short "Yes." with audio before it. Both are fixed (`tools/short-answers`: of 168 short
  answers in Kokoro's voices at -25 to -35 LUFS, 134 were heard before and 167 after), and on
  the phone your short "Yes.", "No." and "Bye." were each heard the first time, three of three. Parakeet still writes "No." as "Noble." now and then,
  and "Okay." as "Locate."; the voice reads them in context. The voice asks again after 8 s of
  silence, and the speech node logs each stretch Silero took for speech that started no turn,
  and why ("not taken for speech", `journalctl --user -u ringback-speech`).
- **Mods are new,** so their API may change. That the terminal prompt shows while the mod
  waits on `PermissionRequest`, and that whichever answers first decides, was seen in one
  run; the docs don't say it. Where Claude Code's built-in guard mod loads (managed settings,
  or a Team or Enterprise login), an installed mod's classic-event hooks don't run, so
  permissions wouldn't reach the phone.
- **Remote Control is taken as on** in every session with ringback on; switched off since, or
  without a claude.ai subscription, a question sent to the Claude app waits in the terminal.
- **The Sessions tab lags a little:** a session switched off can show for up to about a
  minute, and calling it then is refused.
- **The connection check relies on background pushes,** which iOS delivers at most a few an
  hour, sometimes late, and none after you swipe the app away until you open it again. So its
  warning can come late or not at all.
- **The VPN on the phone can go off by itself:** Nebula did once, with Always On set, cause
  unknown. The connection check is there for this.
- **Opus unavailable** leaves the voice silent, with no way back to Haiku.
- **A crash mid-call** may leave that call's Claude Code session under `~/.claude/projects`
  (not checked).
- **Several speech nodes aren't balanced:** a call goes to any one of them, and for a call
  that rings, all of them load their models.
- **After a reboot,** the hub and the agent start only once you log in (launchd agents), and
  with FileVault and no automatic login, not before someone does; nothing has been tried
  through a reboot yet.
- **Development builds** use Apple's sandbox push environment, and their provisioning profiles
  expire; Xcode needs device support for the phone's iOS version.

## Status

Ringback is a working prototype. Here is what has been tried on the real phone with the
current code, and what has only been checked offline.

**Tried on the phone** (locked, on mobile data, with AirPods, unless it says otherwise):
- A call placed through the agent as a session's mod places it, by hand rather than from a
  Claude Code session: it rings, the audio connects while it rings, and the voice starts
  0.1–0.4 s after you answer. You talk it through and interrupt it (Haiku knew what you'd
  heard), and what you ask to pass on goes once you've heard it announced. Parakeet took
  0.07–0.09 s to write down each of your turns, and the voice answered 1.7–2.6 s after each.
- A call with no speech node waits instead of ringing, with its notification, the app's banner
  and the call under Waiting; when the node links again, the call rings within a second, and
  the node loads its models in 2.1 s (unlocked, on Wi-Fi).
- On-device Voice, with no speech node, on mobile data (one call, 64 s): the phone's link
  connected while it rang, the voice started 1.85 s after you answered, you interrupted it,
  and it answered each of your four turns. Apple's recognizer had your words 0.45–0.51 s after
  you stopped, all right ("the first two" as "the 1st 2"), and the phone made each sentence's
  speech in 0.07–0.33 s. It works, but not well enough to use: the voice sounds flatter than
  Kokoro in its pacing, pronunciation and intonation, with an Enhanced voice (Samantha).
- A real Claude Code session's call (2026-11-03): the session finished a task, the agent
  placed the call, the audio connected 1.9 s before you answered, the voice started 0.16 s
  after, answered from the briefing and took an interruption.
- A session's answer coming back to a call, and its permission answered on it (2026-11-03,
  one call of 4 min 41 s): the session called 10.4 s after its turn ended. Four times, what
  you asked went to the session and its answer reached the call 4.5–9.1 s later, the voice
  telling you 1.4–1.7 s after it came. The session's permission to compile and run came to
  the call 7.7 s after you'd asked for that; allowed by voice, it ran at once and its output
  came back. The voice answered 2.0–3.5 s after each of your turns. Your first "yes" went
  unheard, with 35 s of silence after it, and the voice read out a tool call; it now leaves
  out a sentence naming a tool, not yet tried on the phone.
- A session's question with options, and the voice asking again (2026-11-03, one call of
  1 min 27 s): the session's question placed a call at once, which rang 1 s later and opened
  with the question and its options; your choice went to the session, which carried on. When
  your "yes" to its next permission went unheard, the voice asked again 8 s after it had
  finished. That "yes" wasn't taken for speech, and the next was taken but written down as
  nothing (both since fixed, below); "It's a yes." was heard. You hung up 0.3 s into the
  voice telling the session's answer, and nothing called back (since fixed, below).
- Short answers, a permission refused, and calling again with an answer you didn't hear
  (2026-11-03, two calls of 43 s and 19 s): the session's permission came outside your work
  hours and was held; when you set the phone to take calls, it rang 4 s later. A short
  "Yes." (0.4 s) and "No." (0.3 s) were each heard the first time and written down right in
  0.07–0.09 s, and the voice answered 1.7–1.8 s after each; refused, the session didn't run
  that command. You hung up 2.1 s into the voice telling the session's answer, and the
  session called again at once, with "The answer you didn't hear": the phone rang within a
  second, and the voice opened with the answer. Your "Bye." (0.2 s) was heard too.
- Calling back a call from Recents: connected 1.5 s after the tap, the voice opening with
  "Nothing new since our last call" and what had happened since. On that call, Opus on the
  line: Haiku said so, the handover took 0.37 s once you'd heard it, Opus spoke about 6 s
  after you asked and carried on with the whole conversation.
- Calling a session from the Sessions tab: connected 0.8 s after the tap, opening on the
  session's latest briefing; the session was told afterwards.
- Pairing the phone from the portal's link, on mobile data, from the app's banner.
- A call that can't reach the hub (the hub stopped right after it pushed): it rang, and once
  answered stayed silent until the app gave up 20 s after you answered: the phone said
  "Ringback can't reach your hub. Check that your VPN is on." on the call, ended it, and said
  so in a notification. Hung up after a few seconds instead, a notification said "The call
  didn't connect" and why (on Wi-Fi).
- The portal on a real hub, from the phone's Safari: signing in, the map, and taking calls.
- In the app, apart from the hub, with an earlier build whose code for them hasn't changed:
  ringing as call waiting during another call, a call recorded as ended when the app is
  closed mid-call, the call screens.

**Built, checked offline only** (Go tests in each part, `hub/smoke-test.sh` with stand-in
agents and node, `tools/hub-check` with a stand-in phone, agent and node, `tools/session-check`
with a real session, agent and hub, `tools/line-check` with the real hub, agent and node and a
stand-in phone, `claude plugin test mod`, `tools/app-screens` and `tools/portal-screens` for
how things look):
- held calls, work hours, and the hold set from the app's pill or Control Center, or put on
  from the portal, with the pill's breathing light, the icon's badge and the Lock Screen
  widget;
- the connection check's push, and the app checking in with the hub;
- calling a waiting call now;
- a held call's question sent to the Claude app;
- the voice leaving out a sentence that names a tool;
- no call again when you said "bye" over the session's answer, or anything else;
- the hub serving HTTPS and WSS (`[tls]`), renewed without a restart, and the agent and the
  speech node linking to it; the app's side isn't tried at all;
- the weak-signal settings in the hub;
- Interruption Sensitivity and Ignore Background Noise, with real noise or someone talking
  near you;
- calls, asks and messages surviving a restart of the agent or the hub;
- a stop with no words making the voice carry on.

## Next

The to-do list, roughly in order; the README lists it too, one line an item. The four from
a phone proving it's yours go together, toward a hub someone hosts for others, so that each
of them installs only the iPhone app and the Mac client: a hub on the internet needs its
phones and other parts paired, several users on one hub need that too, and they'd want the
host unable to read their calls.

- [ ] **Try on the phone what's only been checked offline** (Status): a "bye" over the
  session's answer, held calls and the hold switch, weak signal, and restarts.
- [x] **A call that can't reach the hub ends and says why** (tried on the phone). A call you're
  on whose audio can't connect, or is lost, ends through CallKit, with a notification and the
  app's banner saying why: that the hub can't be reached, as the connection check says, or the
  error. It waits 20 s after you answer, says the reason on the call before ending, and says
  why when you hang up first too.
- [x] **A phone proves it's yours before the hub takes its push token** (tried on the phone).
  The app's banner sends an unpaired phone to the portal's pairing page, whose
  one-time link opens Ringback, which trades it for the phone's key; every request that names
  the phone by its push token carries the key (Security). A phone registered before this
  pairs once.
- [ ] **A public hub,** so no private network is needed: HTTPS and WSS with a certificate for a
  domain name; the phone, the Macs and the speech nodes paired with the hub (a one-time code
  from the portal) instead of trusting the network; the call's audio on the public address
  (its UDP ports open); then passkeys for the portal. Ready: the phone's pairing (tried on the
  phone), and HTTPS and WSS (`[tls]`, checked offline).
- [ ] **A hosted hub for several users.** Someone runs the hub, and each user has their own
  account and sees only their own phones, Macs, sessions and calls: a call rings only its
  user's phones, the portal shows each user only theirs, and each Mac's agent links as its
  user's. The host's speech nodes are shared, or a user relies on On-device Voice. The app
  comes from the App Store or TestFlight under the host's developer account, so users need no
  account of their own, and pushes go through the host's production APNs key. Needs the
  pairing above, and the end-to-end encryption below so the host can't read the calls.
- [ ] **End-to-end encrypted calls with On-device Voice.** With the phone hearing and speaking,
  only text crosses the hub, so the hub needn't read it. The phone and each Mac's agent would
  share keys from pairing them directly (a code the agent shows, so the hub can't swap the
  keys), and the call's words, its subject and the push's text would cross the hub encrypted.
  Cutting replies into pieces and counting what you've heard would move from the hub to the
  agent, and the hub's log would lose the words it keeps now. Haiku still runs through
  Anthropic, as it does now. With a speech node it can't be end to end: the node has to hear
  you.
- [ ] **The Mac client as a package:** a signed, ready-built agent and mod that a user
  installs without Go or a copy of this repository, set up as a service from the start.
- [ ] **Several sessions in turn, with retries.** One queue for every Mac's calls: a second
  call waits for the first to end. A call you decline or miss tries again 5 min, 20 min, 1 h,
  2½ h and 6 h after its first ring, with the queue paused in between; after the last, every
  call waits until you call that session back.
- [ ] **A better On-device Voice** (later). It works, but sounds flat even with an Enhanced
  voice, and apps can't use Siri's voices. Apple's on-device models improve with iOS updates,
  and nothing says newer iPhones get better ones, so this waits rather than taking much work:
  a Premium voice is the one cheap thing to try (the app picks the best voice installed).
  Kokoro, or Parakeet if its hearing falls short, on the phone only if that stays poor.
- [ ] **On-device voice on a weak signal** (later, with the item above), even with a speech
  node linked, since only text crosses the network.
- [ ] **A phone-network fallback** for where data is too weak: SIP trunks from a local
  operator, or an LTE modem with an ordinary SIM on the hub's machine, priced first.
- [ ] **Showing you things:** sometimes you need to see what a session did, such as an image or
  a large model. Claude Code's iMessage channel plugin can send images, but not the session's
  questions.
- [ ] **The call history in iCloud,** rather than on the phone only.
