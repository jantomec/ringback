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
2. **It's a conversation.** The voice lets you finish: its model reads your words at each
   pause, and waits if you're going on. You can interrupt mid-sentence: the voice stops the
   moment it hears you, then answers you or carries on from the sentence it stopped in, going by
   what you said and what you'd heard. A "mm-hmm" in the middle of a sentence agrees to nothing.
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
8. **The model you choose.** The app's Settings choose the model for conversation, and it
   starts as what your Claude Code sessions use. Ask on a call for a better one and Opus takes
   the call over, with everything said so far.
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
  │ agent ── call's model     │        │  calls, sessions,  │  audio │  Sessions, hold │
  │          (claude -p)      │        │  the call's audio  │        └─────────────────┘
  └───────────────────────────┘        │  and timing,       │                ▲
                                       │  portal            │── Apple push ──┘
  machine with a GPU                   └────────────────────┘
  ┌───────────────────────────┐  link            ▲
  │ speech node: Silero,      │──────────────────┘
  │ Smart Turn, Parakeet,     │
  │ Qwen3-TTS                 │
  └───────────────────────────┘
```

| Part | Runs on | Does |
|---|---|---|
| **app** (`app/`, Swift) | an iPhone on iOS 26 or later | rings through CallKit when a VoIP push comes; carries the call's audio to the hub (WebRTC, Opus), or with On-device voice hears and speaks the call itself; Recents, Sessions and the hold switch; what sessions show you, with its notification extension, and a model's live view; its settings are in the Settings app |
| **hub** (`hub/`, Go) | one always-on machine | the phone's endpoints; pushes through Apple (APNs); work hours, the hold and the calls that wait; the sessions you can call; each call's audio (WebRTC through Pion, Opus through libopus) and its timing; what sessions show you, kept a week, and a model's live view, relayed; the portal |
| **speech node** (`speech/`, Go with sherpa-onnx) | a Linux machine with an NVIDIA GPU | hearing: Silero VAD v5, Smart Turn v3.2, Parakeet TDT 0.6B v2; speaking: Qwen3-TTS 1.7B in Phonon's Hadley voice, through audio.cpp, or Kokoro v1.0 `af_heart` |
| **agent** (`agent/`, Go) | each Mac that runs sessions | the mods' socket; each call's state; the call's model as `claude -p`, on your Claude subscription; the call transcripts; sends what sessions show you to the hub, and streams a model's live view from its renderer |
| **renderer** (`render/`, C++ and Swift) | beside the agent, optional | renders a 3D model a session shows you: its picture, and its live view as H.264; OpenUSD's Storm, or on a Mac without it, Apple's SceneKit |
| **mod** (`mod/`, TypeScript) | each Claude Code session, as a plugin | `/ringback`; has the session write its call briefing; brings what you say on a call to the session as a prompt; puts permissions and questions to the call; the `show` tool |

**Who dials whom.** The agent and the speech node each open one WebSocket to the hub, with a
token from the hub's config, and keep it open. The phone makes requests to the hub and
connects the call's audio to it. The hub pushes to the phone through Apple. Nothing dials the
Macs, the speech node or the phone, so only the hub needs an address the others can reach.

The hub is built for macOS (`hub/build.sh`: one binary for Intel and Apple silicon, libopus
linked in, needing nothing installed), and builds and passes its checks on Linux too, where
`install` makes it a systemd service. The agent runs where the sessions run, on a Mac; it
builds on Linux too, but has no service there yet.

### Who does what in a call

- **The hub owns the call's timing:** the audio both ways; when you start and stop speaking,
  from the speech node's events; stopping the voice the moment you interrupt; cutting each
  reply into pieces to speak; and how much of each you've heard.
- **The agent owns what's said:** the call's state (placed, held, answered, remembered for
  calling back), the model and its tools, holding what's passed on until you've heard
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
   models (about 5 s with Qwen3, while the phone rings). The agent starts the model on the briefing at
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
   Silero says when you start and stop speaking; Parakeet writes down what you said, and when
   it writes nothing for a stretch of speech, writes it down once more from just before it began
   (it misses a short "Yes." with a second of audio before it). At every pause of about 0.7 s,
   your words go to the hub: whether you've finished is for the model to say. Smart Turn still
   scores each turn, for the log.
6. **The reply.** The hub gives your words to the agent, and the model answers in a Claude Code
   session that stays open for the whole call, so each turn sends only what's new, or writes
   `[wait]` if you're going on (Whose turn it is, below). Its first words come about 0.9 s after
   your turn on Opus, 1.2 s on Haiku and 2 s on Sonnet (`tools/reply-speed`). The agent passes
   the reply to the hub a sentence at a time as it's written, the hub cuts it into pieces, the
   speech node speaks each, and the hub plays them.
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
   not answered, and when it rings again; or answered but the audio never connected. The note is added to
   the session's conversation without starting a turn, so it can't set off another call, and
   it says it's from ringback: unsigned, the session took it for your words.

### Speaking a reply

The speech node's voice, Qwen3-TTS, speaks a whole reply in one take. The node's hello says so
(`whole_replies`), so the hub waits until the model has written all of the reply and sends it
as one piece. The node streams the take as it's made: first audio comes 0.02–0.07 s after the
text, about 3× faster than it plays, and the voice has no joins to hear. Pieces made
separately, as before, joined audibly wherever they met, and a fresh start at each sentence
changed how sentences began.

What it costs is the wait for the whole reply. On Opus the first sentence was complete 1.31 s
after your words were sent and the whole reply 1.66 s (medians; the slowest, 138 words, 4.68 s;
`tools/reply-speed`), so the voice starts about 0.35 s later than it would with the first
sentence alone. Every reply of 102 in real calls was under 65 words. When the model writes
some words and then calls a tool, as when it passes your words to the session, the words before
the tool go at once as a take of their own (the agent's `break`): the turn can go on for
seconds after them.

While a take is still streaming, more of it has arrived than has played, and less than it will
be, so what you've heard of it is its text at the voice's pace: the call's measured seconds a
character once a piece is done, and 0.055 s before. Spread over the audio so far, as for Kokoro's
pieces, it had you hearing nearly all of a reply 3 s into it, so "mm-hmm" seemed to come at the
end.

Cutting a take off, when you interrupt, closes its request to audio.cpp's server, which stops
within a chunk or two: the next reply's first audio came 0.34–0.36 s after a cut, in 30 cuts
at random points (`tools/tts-trial`).

If the server dies, or a take goes 5 s with no audio, at its start or partway, the node starts
it again and the reply goes on: from the sentence it was in, after the voice says "The voice
server crashed and has been restarted. Continuing from the last sentence." (or only the first
sentence of that, when none of the reply had come). The node has no word timings from the
voice, so where that sentence starts comes from the audio it sent and the voice's pace, with a
margin of 15% (at least a second) back, so a sentence may be said twice rather than missed. The
phone still plays what had come before the crash, which runs 3× ahead, so the gap you hear is
shorter than the restart.

A node with `voice = "kokoro"`, and On-device Voice, still take a reply in pieces: the first
sentence on its own as soon as it's complete, then, one second before a piece ends,
everything finished since, up to its last complete sentence. Kokoro makes each sentence on
its own anyway, about 0.12 s to first audio (`speech/README.md`).

### Whose turn it is

The model decides when you've finished, from your words and the conversation, because nothing
else could tell. On 98 turns of 8 real calls by a speaker who stops mid-sentence to find a word,
Smart Turn, which hears only the sound, waited on 7 of the 50 unfinished ones, and Sonnet reading
the words on 41 (`tools/turn-replay`). At each pause the model gets your words so far:

- **It answers,** if you've said what you wanted to. A short, clear answer to a question you've
  heard is finished, even one word.
- **Or it writes `[wait]`,** and nothing is said. What you say next goes as the next turn, and
  the earlier words are in its session already. A session's note waits meanwhile, so the voice
  doesn't talk over your thinking. If you've said nothing 3 s later, it's told so and answers.
- **If you go on before it's spoken,** its reply is stopped where it is and never spoken. Claude
  Code keeps the turn, so your words stay in its session.

Turns are asked about at 0.7 s of pause: sooner would ask in the middle of more sentences, and
later holds up every reply. So the voice's first words come about 0.7 s plus the model's time
after you stop, whether it's sure or not.

### Interruptions

When you start speaking while the voice talks, the hub stops it at once and keeps the rest of the
reply: the pieces not yet played, and what's still being made. The agent hears how much of it you
heard, word for word. Your words then come at your next pause, and go to the model with what you
heard and what you didn't. They can only be about what you heard, so they can't answer or agree
to anything in the rest. It then does one of three things:

- **Answers you.** Its reply takes the place of the rest, which is dropped, with anything it had
  announced that you hadn't heard yet; it was told that, and does it again if you still want it.
- **Writes `[continue]`,** if you only showed you're listening, or what you said changes nothing.
  The voice goes back to the start of the sentence it stopped in and plays on, as it was, and what
  it held goes as you hear it announced.
- **Writes `[wait]`,** if you're going on.

Two cases don't need the model, so the voice goes on at once: a stop with no words, as with a
cough, and a bare "mm-hmm", "yeah" or "okay" before you've heard a question in that reply. A
"yes" once you've heard one goes to the model, which takes it as the answer: on a call, a "Yes."
right after a question was one, and a "Yeah." mid-sentence wasn't, so the word alone can't say.
Parakeet can mishear so short a word: in `tools/line-check` a "Mm-hmm." came out as "Mammal.", and
the model, told it had stopped, read that in context and carried on, about a second later than
the word list would have.

A phone that speaks itself can only stop, so with On-device voice the rest is dropped, as it was
before, and the model hears what you'd heard.

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

### Acting on the call

On a call for a session, the model has three tools, served by a small MCP server the agent starts
with each `claude -p`: `pass_to_session` for what you want the session to do or know, and
`answer_permission` and `answer_question` for what the session asks you. Every call also has
`better_model`.

- The model says what it's passing on, or what it will answer, then calls the tool. The message
  is held until you've heard the voice say so, and dropped if you cut it off before; the model is
  then told, and passes it again if you still want it. A tool called before any words waits
  for the sentence after it. If the model reaches for a tool before saying anything, the voice
  says "Okay." at once, so you aren't left in silence.
- What the model writes after a tool isn't spoken if it said something before: it tends to repeat
  itself. Nor is a sentence that names one of its tools: on a call, the model once said the call it
  was about to make ("Then call answer_permission with allow true"). So the agent passes a
  reply on a sentence at a time, which costs no time: the hub speaks only whole sentences.
- Before you've said anything, `pass_to_session` passes nothing: an opening written while the
  phone rings has nothing of yours to pass on.
- The voice does no work itself. Whatever you want written, changed, run or looked up goes to
  the session, even something short. It answers from the briefing only what the briefing
  says.
- When several things need your answer, it says how many, asks them one at a time, and passes
  the answers on together.
- A permission: the model says in plain words what the session wants to do, and whether it
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
   2.9 GB of the GPU's, and with Qwen3 audio.cpp's server beside it, 2.6 GB more of memory and
   4.9 GB of the GPU's in all) starts when a call is coming and exits after 60 s without one
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
of the words on your own recordings, against Parakeet's 5.9% on the same test (`tools/speech-trial`,
`tools/stt-eval`), answers to permission prompts among them ("allow it once" as "I love it
once"). It did hear in the background with the screen locked, with its final words 0.15 s
after they were asked for, and the app stayed at about 30 MB: the model runs in a system
service.

Nothing about speech nodes is set in the app. A node links to the hub by itself, with its
key from pairing or the hub's token; pairing one, unpairing it, or forgetting one that's gone
for good, is done on the hub's portal.

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
end the hold. The agent keeps the call, without starting the model, and asks again then, or
after 10 min if that's sooner, so hours you widen apply within 10 min; it waits on the wall
clock, so a Mac that slept still asks on time, and keeps its Mac awake meanwhile
(`caffeinate`). A new briefing from the same session replaces the held call's subject and
briefing rather than placing a second call. The hub keeps the calls that wait, and drops one
whose agent hasn't asked about it for 25 min, so a call from a Mac that's gone doesn't wait
forever. When you take calls, the app asks the hub to have them ask again (`/reask`), and they
ring one at a time, in turn.

**Waiting calls in the app** show at the top of Recents, under Waiting, in orange, with when
they ring ("Rings tomorrow at 08:00"). Tapping one calls it now: an outgoing call that
doesn't ring, whose voice opens with the briefing and knows you called while it waited.

**Cancelling a waiting call**, from its swipe action or its details in the app ("Cancel Call"),
or the portal's Status or Calls page ("Cancel call"): it never rings. The hub acts first, so
it works while the Mac that holds the call sleeps: the call leaves the waiting calls at once,
the calls after it can ring (a missed call no longer holds them up), and the hub keeps it as
cancelled for 3 days, unlisted, so its agent hears of it when it next asks about the call. The
agent, told now or then, forgets it and leaves the session a note ("won't ring: the developer
cancelled it on the phone without hearing it"); what the call asked, a permission or a
question the session waits on, goes back to the session's terminal (and the Claude app),
neither allowed nor denied, and the app asks first for a call with a question. Nothing else is
lost: the briefing stays the session's latest, which a call from Sessions opens with, and the
session's next briefing places a new call. A call that rings can't be cancelled; decline it.

**A session that stops listening** (ringback off in it, or the session ended) has its waiting
calls dropped, as its agent sees within half a minute: they won't ring, since the session
couldn't act on what you said.

**One call at a time.** Every Mac's calls share one line, which the hub keeps (`hub/line.go`).
While a call rings or you're on one, a call that would ring is held in turn, as a held call is:
its agent keeps it and asks again, and once the line is free the hub asks the call that's
waited longest, which rings. A call you make yourself takes the line too.

**A call you don't answer**, or decline, rings again 5 min, 20 min, 1 h, 2½ h and 6 h after it
first rang (at least 5 min after its last ring), and until it does, nothing else rings: you
weren't there, so other calls would only pile up. After its last try it doesn't ring again; it
waits until you call its session, and so does every other call. The phone says whether you
answered, declined or missed a call; one it says nothing of within 75 s, as when it's off,
counts as missed. Meanwhile the call waits under Waiting in the app ("Rings again at 14:05"),
its session is told each time, and calling it now, calling it back from Recents, or calling
its session from Sessions ends the wait: it doesn't ring again, and the next call in turn
rings once you hang up. Each ring starts a fresh model, as the opening is written for it.

**Calling back.** Tapping any call in Recents calls its session straight away, without
ringing, answered calls too. The agent remembers each call for a day, across restarts, and
the call back gets a fresh model with that call's briefing: for a call you missed, it opens
with what it was about; for one you took, with what changed since, or says nothing has and
asks what you want. If the session wrote a newer briefing since, that follows. If the session
isn't listening any more, the call still connects, but the voice can't pass anything on and
says so. Work hours don't apply: you called. If the agent no longer knows the call, the app
calls the session from Sessions instead, if it's there.

**During another call** (phone, FaceTime, WhatsApp; not another ringback call, after which it
waits its turn), a ringback call rings as call waiting,
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

## The model for conversation

The app's Settings sheet, from the gear on Recents and Sessions, chooses the model that talks on
calls, "Default" at first: what a Claude Code session on that Mac would use, the model in its
settings or else Claude Code's own default. The list isn't kept here. Claude Code says which models
it offers, without a model turn, in about 0.5 s (`initialize` on a `claude -p`), and each agent
asks it when it links to the hub, every hour, and when you open the sheet. So a new model shows by
itself, and a name such as `opus` follows new versions. The hub remembers each Mac's list, so a
sleeping Mac's models still show, says which Macs lack a model, and keeps one choice for them all,
as it keeps the hold. It reaches each agent at once, or as it links, because a call needs it
before the phone rings. A Mac that doesn't offer it uses its own default, and the choice stays, in
case it comes back. A call uses the model chosen when it starts.

It's on a sheet of the app's own, not with the other settings, because a Settings bundle's lists
are fixed when the app is built, and a change there reaches the hub only when the app next runs.
The Settings app shows it read-only, with a link to the sheet, and the portal shows it too.

## A better model on the line

When you ask ("can I get a better model on the line?"), the model says so and calls `better_model`,
which is held until you've heard it, like the other tools. Then its process is closed and its
session resumed with Opus (`claude -p --resume <id> --model opus`), with the same tools, so Opus
has the whole conversation, tool calls included. Opus is told it's taking over and speaks first.
From your request to Opus's first words took 6–7 s offline (`tools/handover-check`, in git
history): a short line, then a few seconds of silence. Opus's first turn is mostly uncached and
costs about as much of your usage as ten later turns, once. If Opus isn't available (a usage
limit, or a plan without it), the voice goes silent; there's no way back. On Opus already, it says
so.

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

Tapping a card opens what the session showed you ("Showing you things"), with a red count on its
monogram of what you haven't opened; its green phone calls it, always, whatever it showed. A
call starts at once, with a call ID made on the phone. The voice has the
session's latest briefing and opens with what's new; with no briefing yet, it says it knows
nothing of the session's work and asks what you want. A session that stopped listening
meanwhile refuses the call, and the app says so.

## Showing you things

A call can't show you a render, a plot or a recording, and nobody watches the phone during a
call anyway. So a session can send you something to look at, which arrives as a notification
from it: under its project's name, grouped with its other notifications, as its calls ring.
It isn't messaging: you don't reply to it, and what you want done about it you say on a call
or in the terminal.

The mod gives every session a `show` tool, ringback on or off, which takes a file and a
caption. The session uses it when seeing says more than words, or when you ask, as on a call:
the voice passes "show me the render" on like anything else. The agent sends the file to the
hub with a small picture of it for lists, the hub keeps them for a week and pushes each phone
a notification, and the app's notification service extension fetches what the notification
shows. Tapping it opens the file in the app's viewer. The hub never shows it to anyone but a
paired phone, and only with its key.

In the app, what sessions showed you is a shelf of pictures in Recents, between the waiting
calls and the history, newest first, with a dot on each you haven't opened; See All has them
all by day, and by project if you pick one. A session's card in Sessions opens what it showed,
and counts what you haven't opened (The Sessions tab). The details of a session's calls, and of
the session, list what it showed, even once it isn't listening. Two other ways were weighed: a
third tab, like the Phone app's Voicemail, which the bottom bar has no room for on a 390 pt
iPhone beside the hold switch, and the gallery can become if there are many; and rows among
the calls, which would read as messages and drown the history. Nothing counts on the app's
icon, which counts the waiting calls, and the app sets that count only when it changes,
since setting it to 0 can clear the notifications delivered (not checked).

- **Images** (PNG, JPEG, GIF; HEIC and WebP open in the app): a notification shows the image
  itself up to 10 MB, so a larger one, or one in a format it can't show, comes with a smaller
  JPEG (`sips`). The app zooms it.
- **PDFs:** a notification can't show one, so it shows Quick Look's picture of its first
  page (`qlmanage`). The app shows the pages.
- **Clips** (MP4, MOV, M4V): a notification shows only its picture, since the extension has
  about 30 s to fetch what it shows, too little for a clip on mobile data; the app fetches the
  clip when you open it. Its controls go to the start, back a frame, play or pause, forward a
  frame and to the end, as a model's animation will.
- **3D models** (USD, USDZ, STL, GLB/glTF): the model stays on the computer that runs the
  session, which may hold gigabytes of it, and that computer renders it (`render/README.md`):
  with OpenUSD's own renderer, Hydra Storm, where it's installed, as on a Linux workstation
  with an NVIDIA card, through EGL with no display; on a Mac without it, with Apple's SceneKit
  and Model I/O, the fallback, which mishandles some USD files and can't open glTF. The
  notification shows its picture. Without a renderer, or with one that can't open the file,
  the session is told why and what's missing. Opened, the view is live: the computer renders it
  as you turn it (one finger), move it (two) and zoom (a pinch), with a double tap or Reset to
  go back, plays its animation with the same controls as a clip, if it has one, and streams it
  as H.264 video through the hub, as a call's audio goes, with where you look from going back
  on a data channel. It's rendered at the phone's screen pixels, at most 1920 on its long side,
  with 4× multisampling, at up to 8 Mbit/s. The phone sends the camera itself (where it looks
  from, and at what) as your fingers move, on a data channel that neither orders nor resends,
  so nothing a slow network loses adds up or holds up the next; where you let go goes on the
  reliable channel too. The computer renders a move as soon as it comes, at most 60 frames a
  second, only when something changes, and a few frames more, the last a key frame, for the
  picture to sharpen. Reset is a button on the model, apart from the toolbar's. The hub relays: the agent sends the frames over its link, and the hub puts them on
  the phone's video track. Only Apple's fallback streams so far; OpenUSD's renderer makes the
  picture, and its live view is next. Not done either: drawing the last frame turned on the
  phone while the computer catches up, which would hide the network's delay.

The notification is a communication notification, as a message's is: iOS shows the project as
the sender, with its monogram as the app draws it (tried on the phone), which needs the
Communication Notifications capability on the app, not its extension. Files are at most
100 MB.

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
- **Few addresses.** The hub offers only the address it listens on (or its `public_ip`, on
  its `udp_ports`), over UDP and IPv4, and the phone only its VPN's addresses (all its
  addresses if the VPN gives none), so WebRTC doesn't try pairs that can't connect. With every
  address offered, audio connected 2.1–2.4 s after the offer; with only the VPN's, 0.94 s (one
  call on mobile data). For a hub whose address is public (a name is looked up first), the
  phone offers every address it has, since a VPN that's on may not reach it
  (`AudioLink.overVPN`; not tried with a public hub).
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
| agent | hub | one WebSocket (`/agent`) with the Mac's key, or the agent token |
| speech node | hub | one WebSocket (`/speech`) with the node's key, or the speech token |
| hub | Apple | pushes (HTTPS) |
| Apple | phone | VoIP pushes that ring, background pushes for the connection check, notifications |
| mod | agent | HTTP over a Unix socket on the same Mac |

**Nebula** is what ringback is tested on, with the hub's machine and the phone on the same
Nebula network, and a lighthouse that acts as a relay (`relay.am_relay: true`). Only the
phone can start a tunnel to itself: nothing can open one to a phone on mobile data, and
direct tunnels to it failed, so the phone's traffic goes through the relay. That's why the
phone always dials the hub, and never the other way. Another VPN with the same property (the
phone reaches the hub from anywhere) should work, but hasn't been tried.

**A public address isn't tried yet.** Over plain HTTP, the portal's password, the links'
tokens, the phones' keys and the calls' IDs would cross the internet unencrypted (Security).
The hub has what it would need, checked offline only (`docs/setup.md`, "A public hub"): HTTPS
and WSS (`[tls]` in `hub.toml`); every part pairing, rather than sharing tokens; the call's
audio on its public address (`public_ip`) and a range of UDP ports a firewall can open
(`udp_ports`); a limit on refused requests; the app offering the call's audio every one of its
addresses for a hub on a public address, not only its VPN's, and saying what to check when it
can't reach the hub by where the hub is; and the hub building and installing on Linux. Passkeys for the portal are
still to do (Next).

## Security

The private network is the outer wall: anything on it can reach the hub. Inside it:

- **The links.** An agent or a speech node links only with its own key, from pairing, or a
  token in `hub.toml` (`[tokens]`, optional), sent as `Authorization: Bearer`; anything else
  gets 401.
- **Macs and speech nodes pair** as the phone does: the portal's Setup page, or
  `ringback-hub pair mac` (or `node`), makes a one-time code for that kind of part (32 random
  bytes, valid 10 minutes, kept only as a hash), and `ringback-agent pair` or `ringback-speech
  pair` run there trades it at `/pair-link` for the part's own key (32 random bytes), written
  into its config, readable only by you; the hub keeps only the key's SHA-256, by the name it
  paired as (`parts.json`), and names its link by that name. Pairing again under the same
  name replaces the key, and forgetting the part on the portal drops it. One that's linked
  with its key can be unpaired there (Unpair, on the status page): its link closes, and it
  can't link again until it pairs again. One linked with the hub's token can't, as it would
  link straight back; its list entry says which it uses.
- **The phone has no token.** A call's ID is the call's secret: random, at least 16
  characters, and it reaches the phone through Apple's push. The hub accepts an offer only
  for a call the agent that owns it knows. A session's ID is a secret of the same kind: with
  it, a call to that session can pass it messages, which it takes as prompts.
- **Calls and sessions come only from agents,** over their links, with a Mac's key or the
  agent token.
- **A phone pairs before the hub trusts it.** The portal, signed in, makes a one-time pairing
  code (32 random bytes, valid 10 minutes, kept only as a hash, in a file only the hub's user
  can read), which opens Ringback as a link (`ringback://pair`); `ringback-hub pair` makes one
  on the hub's machine. The app trades it at `/pair` for the phone's own key (32 random bytes),
  which it keeps in its Keychain, on that device only; the hub keeps only the key's SHA-256,
  with the phone. Every request that names the phone by its push token must carry the key,
  compared in constant time, or gets 401: so only your phones register, get the IDs of the
  waiting calls and the sessions (`/waiting`, `/contacts`), set their hold, or are rung and
  pushed to. The pairing page's sign-in comes back only to a page of the portal.
- **Refused requests.** An address with 30 requests refused (401 or 403) within a minute is
  shut out for a minute (429): every secret the hub checks is random and long, so this is
  about load more than guessing. The hub's own machine isn't counted.
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
- **The call's model.** It runs with no tools but the call's own, no other MCP
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

The portal changes four things: **the hold**, on every paired phone at once, so the app shows
it too (work hours still end it at their next start, as they end the app's); **forgetting** a
node or a Mac that's gone for good, or **unpairing** one that's linked with its own key;
**cancelling** a waiting call; and the **pairing codes** it makes. Everything else has
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
- **Parakeet v2** for speech-to-text, kept after a trial of five larger models on the speech node
  (`tools/stt-trial`): Qwen3-ASR 1.7B, Granite Speech 5.0 2B, Cohere Transcribe, Canary-Qwen 2.5B
  and Parakeet Ultra, on your recordings, through Opus and cut as a call cuts them. On the 464
  scripted words, the only neutral reference, it made 41 errors (8.8%) against 40–52, too close to
  separate, at about half the time of the leading rivals (67 ms against 120–316), with half their GPU memory,
  loading in 2 s rather than 5–27. It also keeps your fillers through Opus, which help tell when you
  haven't finished: 45, against 13–19 for Qwen3, Granite and Cohere. The 5.9% of the first comparison
  (`tools/stt-eval`) came from the Mac's runtime and a reference corrected from Whisper's own text;
  on the node, it's 8.6% before Opus. Whisper large-v3-turbo was about as accurate but 12 times
  slower, and invented words on short clips. Apple's on-device recognition got 10.3% wrong on that
  comparison and takes no vocabulary, so it's the fallback only. A bigger gain than any model may
  be how speech is cut: on free speech Parakeet made 27 errors split at pauses of 0.6 s, and 44 cut
  stretch by stretch as a call does (not tried yet: it needs a whole turn written down at once).
- **Qwen3-TTS 1.7B Base for the voice, in Phonon's Hadley voice,** a whole reply in one take
  (`tools/tts-trial`). Picked by ear: a blind test of six voices, then long stretches, where
  Hadley sounded best and pieces joined audibly. A take had to keep its words: Ming-3.0-tts-0.5B,
  the blind test's other favourite, skipped words past about 50 words a take (7 of 64), as its
  makers' own advice to cut text at 24 words suggests. Qwen3 kept every phrase up to 200 words
  in 96 takes, and skipped in 2 of 24 at 300. It's Apache 2.0, makes Hadley from a 6.7 s clip,
  and holds about 3× real time with 3 GB of the GPU. No larger model with a free licence did
  better on paper. Kokoro `af_heart` stays as `voice = "kokoro"`: faster (40×), but
  a sentence at a time. Breeze TTS 2 sounded best in the blind test and is non-commercial.
- **The model decides when you've finished,** not Smart Turn, a fixed wait or a list of words.
  Smart Turn hears audio only, and it closed 89 of 98 real turns after 0.2 s of silence, scoring
  "But uh there should be some kind of" 0.98; it told turns you went on from barely better than
  chance (AUC 0.58, where 0.5 is chance), and giving its score to the model, whole or only when
  very low, didn't help (`tools/turn-replay`). A fixed wait of 2 s would have caught 35 of the 42
  turns you resumed from, added 1.7 s to every reply, and still spoken over a longer pause. Words
  alone at the end of a turn (a filler, "the", "of") caught 14. LiveKit's turn detector can't be
  used: its good version is cloud-only, and its licence bars the local one outside LiveKit's
  framework. Better audio, such as p99lab's turn-1-mini or your pitch against your own range, may
  yet add something the words can't, at the pauses where they leave the model unsure; that needs
  your audio, measured at each pause.
- **Stopping the voice keeps the rest of its reply,** rather than dropping it and having the model
  say it again: carrying on then costs nothing, and takes no time. Before, 32 of 48 "interruptions"
  on one morning's calls cut off a reply you'd heard none of, and a "Yeah." dropped what the
  voice had announced: nine messages, five of them the same one.
- **A level gate for other people's voices,** not a model. A model trained on a sample of
  your voice (VoxLock, kept as the tag `voxlock`) kept a voice 6 dB below yours out
  entirely, but slowed every interruption by about 0.1 s, raised Parakeet's errors from 5.9% to
  7.0%, and could mute you for a whole call. With AirPods, other people arrive quiet enough
  for a gate.
- **Settings between calls,** not during: the in-call screen covers the app.

**The voice**
- **The call's model on your subscription through Claude Code,** not the API, for a personal tool; in a
  session that stays open for the call, not a `claude -p` per turn (3.2 s each, 1.7 of it
  start-up).
- **The session's briefing as the voice's context.** The session's last message is too
  little; a fork of the whole session is slow to start and costs more on every call.
- **Tools for acting,** not tags in the reply: nothing ends the model's turn after a tag, and it
  went on to imagine the session's answers. Messages are **held until heard,** since one passed
  as soon as it was written had to be taken back when you cut the voice off.
- **The session writes the subject** with its briefing, rather than a separate model call.
- **The voice's model runs outside your project,** so your `CLAUDE.md` doesn't load into it
  and calls don't fill your `/resume` list; the agent writes a plain transcript into the
  project instead.
- **A call back gets a fresh model,** not the missed call's, whose opening was written for a
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
- **What a missed call asked waits** with it, for when it rings again or you call it back,
  rather than going to the terminal at once (which shows it too, and whichever answers first
  decides); and outside your hours it waits for the morning call, with a notification, rather
  than going to the Claude app at once. You choose from the app.
- **A missed call holds the others up** until its next try, and after its last until you call
  back, rather than letting them ring meanwhile: you weren't there for the first.

**The app**
- **Like the Phone app:** Recents and Sessions, with settings in the Settings app, which
  costs time pickers (lists in 15-minute steps instead), words at the ends of sliders (in the
  footers instead), and new settings reaching the hub at once (within half an hour instead).
- **Google's WebRTC library** in manual-audio mode, so CallKit owns the audio session; a
  WebRTC client that takes the audio session over clashes with CallKit.
- **One hold for the whole hub,** not a pause on a speech node: with a node paused, the
  phone's own voice would still take calls.

## Limits and risks

- **A long reply waits, and can skip.** The voice starts only once the whole reply is written:
  about 5 s for 150 words (estimated from one timed reply). One take of 300 words skipped a
  phrase in 2 of 24, and a take stops at 2048 of Qwen3's frames, 164 s. Replies on calls have
  all been under 65 words, so neither has come up on a call.
- **audio.cpp's server can crash when a take is cut off.** It did once in 33 cut-offs ("double
  free or corruption"), and hung. The node starts it again and the reply goes on from the
  sentence it was in (above), but where that is comes from the voice's pace: a reply full of
  numbers, which the voice reads slower, could skip the end of a sentence; and the hub, not
  told of the repeat, places what you've heard of that reply behind by it.
- **A missed call can hold every call up for long.** After its last try, nothing rings until
  you call its session back or cancel it, however many calls wait; the app lists them under
  Waiting. And a
  call waiting its turn on a Mac that went to sleep holds the line until its agent asks again,
  or until the hub drops it 25 min after it last asked.
- **The phone's tunnel can stop receiving.** Once (2026-11-03), after the phone moved from
  home Wi-Fi to mobile data to another Wi-Fi, for over two minutes its Nebula packets reached
  the hub and the hub answered, but nothing reached the phone. Moving between networks, with
  another VPN beside Nebula, a tunnel doesn't always recover by itself. Ringback can't fix it; it
  notices: a call whose audio can't connect ends, with a notification and a banner saying the
  hub can't be reached, and the connection check warns between calls.
- **A stale tunnel delays a call.** After the phone changes network, the VPN can take 10–20 s
  to reach the hub again; the app keeps trying, for up to 20 s after you answer, then ends the
  call and says why.
- **What sessions show you is kept on the hub as it came,** for a week, readable by whoever runs
  the hub's machine. On your own hub that's you; on one someone hosts, it would need the
  end-to-end encryption under Next.
- **Every ring costs an opening from the model** on your subscription, even if you decline it, so a call
  you keep missing costs six; and a held call keeps its Mac awake until it rings.
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
- **Opus unavailable** leaves the voice silent, with no way back.
- **The voice can wait when you've finished.** The model waited on about 9 of 48 finished turns,
  in a replay (`tools/turn-replay`), and each then takes 3 s more. It's tuned on those same turns,
  and from one speaker. A model the app still lists after it's retired could leave a call silent
  until the list next refreshes (not tried).
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
- A "bye" over the session's answer (2026-11-04, one call of 18 s, locked, on mobile data,
  through the hub, agent and speech node as they are now): you allowed a permission, the
  session's answer reached the call 1.8 s later, you said "Okay, bye." 1.7 s into the voice
  telling it and hung up 1.5 s after that, and nothing called again. The audio connected
  0.52 s after the offer.
- The phone choosing its addresses for the call's audio by the hub's (2026-11-04, one call,
  locked, on mobile data): for the hub on Nebula it offered only the VPN's addresses, as
  before. The audio connected 1.14 s after the offer, slower than
  the 0.29–0.45 s of the five calls before it, but 2.5 s before you answered, and the voice
  started 0.16 s after you did; the address lookup comes before the offer, so the time after
  it isn't the lookup's. A hub on a public address isn't tried.
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
- What a session shows you (2026-11-05, unlocked, on Wi-Fi, by hand through the agent and
  hub): an image, a PDF and a clip each arrived as a notification, and the clip played, with
  its controls. The image didn't zoom, and nothing listed them in the app; both since fixed
  (the viewer's zoom, and the Shown shelf), since tried (below).
- A model's live view (2026-11-05, unlocked, on Wi-Fi, through the hub, with Apple's
  renderer on the Mac): it opened, turned, moved, zoomed and played. The picture was blurry at
  first, as the phone asked for it at the size of the viewer while it was still opening (96 ×
  64); sharp once it waited for its size. Turning felt smoother once the Mac rendered each
  move as it came, at up to 60 frames a second, and the phone sent them on a channel that
  neither orders nor resends. How long a turn takes to show on the phone isn't measured.
  The Shown shelf in Recents was looked at and kept, and Sessions' cards open what a session
  showed, with their count. The notifications come as from the session: its name and
  monogram, as a message's sender.
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
- the model deciding when you've finished, with `[wait]` and its 3 s limit, and a reply you speak
  over kept and carried on from the start of its sentence, or dropped for a new one (the agent's
  tests, `tools/hub-check`, `check.sh` on the GPU, `tools/line-check` with the real model and node
  and a Kokoro voice for yours, and a replay of real turns; on no call with a person yet);
- the model for conversation: the agent asking Claude Code, the hub keeping the lists and the
  choice, and the app's sheet (the agent's and hub's tests, `tools/app-screens`);
- the voice as Qwen3-TTS in Hadley's voice, a whole reply in one take: its words, speed and
  first audio on long takes, and cutting a take off (`tools/tts-trial`), the hub holding a reply
  for it and the agent's `break` (their tests), the node (`check.sh` on the GPU), and a whole
  call (`tools/line-check`: the voice answered 2.6–3.6 s after you stopped, with the model's
  first words taking 2.0–3.1 s that day); not on the phone yet;
- the hub serving HTTPS and WSS (`[tls]`), renewed without a restart, and the agent and the
  speech node linking to it; the app's side isn't tried at all;
- Macs and speech nodes pairing and linking with their own keys (a Mac's agent paired with
  a real hub and linked; the speech node only in its tests), and unpairing one that's linked;
- the call's audio on a public address and a range of ports, and refused requests shut out;
- cancelling a waiting call from the app or the portal, also while its Mac sleeps, and a
  session's waiting calls dropped once it stops listening (the hub's tests and smoke test, the
  agent's and the mod's tests; the app's screens not looked at);
- one call at a time, and a call you don't answer ringing again 5 min to 6 h after, with
  nothing else ringing meanwhile, and waiting after its last try until you call back (the hub's
  tests, its smoke test and hub-check, and the agent's tests);
- what the app and the portal say about reaching the hub by whether it's on a private address
  (for yours, the same words as before);
- the hub on Linux: `hub/build.sh` building it there with libopus linked in, its checks and
  smoke test passing, and `install` making a systemd user service that came back 12 s after a
  kill (on the speech node's machine, then taken away);
- a model's live view, through the real hub, agent and Apple's renderer on one Mac, with a
  stand-in phone decoding the video (`tools/view-check`): the first frame 0.15 s after the
  phone opens it, a turn of the camera changing the picture 0.05 s after it's sent, the
  settled view as sharp as the renderer's own still (43 dB), playing, going to a frame, and
  closing it stopping the renderer; with no network between, so the phone will see more;
- a session showing you a model: the agent rendering its picture with Apple's fallback on
  this Mac, and refusing a GLB, saying why (by hand, through the agent and hub as installed,
  2026-11-05; the agent's tests with a stand-in renderer); OpenUSD's renderer rendering USD,
  GLB and STL, built with `render/build.sh` on a Linux workstation with an NVIDIA card, and on
  a Mac against conda-forge's OpenUSD in a scratch folder (`build.sh` isn't run on a Mac); no
  agent runs on Linux yet to show them; the app's Shown shelf, its gallery and the viewer's zoom
  (`tools/app-screens`);
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

- [ ] **Try on the phone what's only been checked offline** (Status): held calls and the hold
  switch, weak signal, and restarts.
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
  phone); HTTPS and WSS (`[tls]`), the Macs' and speech nodes' pairing, the call's audio on the
  public address, and a limit on refused requests (checked offline; `docs/setup.md`, "A public
  hub"), the app's audio for a hub on a public address, and the hub on Linux. Left:
  passkeys, and trying it on a server.
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
  agent, and the hub's log would lose the words it keeps now. The model still runs through
  Anthropic, as it does now. With a speech node it can't be end to end: the node has to hear
  you.
- [ ] **The Mac client as a package:** a signed, ready-built agent and mod that a user
  installs without Go or a copy of this repository, set up as a service from the start.
- [x] **Several sessions in turn, with retries** (checked offline, not tried on the phone).
  One queue for every Mac's calls: a second call waits for the first to end. A call you
  decline or miss tries again 5 min, 20 min, 1 h, 2½ h and 6 h after its first ring, with the
  queue paused in between; after the last, every call waits until you call that session back.
- [ ] **A better On-device Voice** (later). It works, but sounds flat even with an Enhanced
  voice, and apps can't use Siri's voices. Apple's on-device models improve with iOS updates,
  and nothing says newer iPhones get better ones, so this waits rather than taking much work:
  a Premium voice is the one cheap thing to try (the app picks the best voice installed).
  Kokoro, or Parakeet if its hearing falls short, on the phone only if that stays poor.
- [ ] **On-device voice on a weak signal** (later, with the item above), even with a speech
  node linked, since only text crosses the network.
- [ ] **A phone-network fallback** for where data is too weak: SIP trunks from a local
  operator, or an LTE modem with an ordinary SIM on the hub's machine, priced first.
- [x] **Showing you things** (tried on the phone): a session sends you an image, a PDF, a clip
  or a 3D model, as a message from it ("Showing you things"); a model you turn, move, zoom
  and play live, rendered on its own computer, with Apple's renderer on a Mac.
- [ ] **3D models on Linux:** OpenUSD's renderer renders a model's picture on Linux (checked on
  a workstation with an NVIDIA card) but can't stream its live view yet, which needs `serve`
  in `ringback-render`, encoding H.264 there (NVENC); and the agent needs a service on Linux
  (systemd, as the hub's and the speech node's), with the mod in its sessions.
- [ ] **The call history in iCloud,** rather than on the phone only.
- [ ] **Ending a call by voice:** when you say bye the voice goes over what you settled, says bye
  and hangs up; speaking during that stops it. It needs the app to end the call through CallKit
  when the hub says so (`hang_up` is there, and nothing sends it yet).
- [ ] **At least 60 s between calls,** so you can think about the last one before the next rings;
  not for calls you make yourself.
- [ ] **What a session showed you, during a call:** the call screen can't be left, so nothing
  shown can be opened until you hang up.
- [ ] **Sending text or a link to the phone,** which `show` can't: it takes files.
- [ ] **The caller's name from where the session started and its title:** it's the session's
  current folder now, so a session that moved rings as "data", and its transcript goes there too.
- [ ] **Better speech models:** five rivals to each model were tried on the speech node
  (`tools/stt-trial`, `tools/tts-trial`). Parakeet v2 stays, and the voice is now Qwen3-TTS in
  Hadley's voice, a whole reply in one take. Left: trying the voice on a call, writing down a
  whole turn at once, and a test on your own technical words.
