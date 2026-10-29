// The ringback mod: `/ringback` switches calling on or off for this session. While it's on,
// each finished turn has the session write its call briefing, and the voice server on this
// Mac rings your phone with it. During the call, what you say for the session reaches it as
// a prompt, and its answer goes back to the call; if you've hung up, it calls again. After
// each call, the session is told how it went, in a note that starts no turn (see "Ringback
// mod" in docs/design.md).
import type { EngineInterface, Register } from 'claude-code'

// What the session is asked to write when a turn ends. The voice, a faster model, talks
// about it on the call and knows nothing else about the session.
const BRIEFING = `Ringback is about to call the developer's phone about this session, so write the \
call briefing now. Use no tools, and reply with the briefing alone.

The first line is "Subject: " and 3 to 8 words on what the call is about, shown on the phone \
as it rings. After a blank line, write for the voice that will talk the developer through it, \
in at most 300 words:
- what you did and what's new since the developer last heard from you;
- what needs them: decisions, questions, anything blocked, and what you'd do next;
- what the voice needs to answer likely questions, such as why you chose something.

Leave out the call itself, such as whether it will reach the developer: you'll be told \
afterwards how it went. It will be spoken, so write plain sentences: no lists, headings, \
code, tables or links, and round the numbers.`

// What the session reads when the voice passes something on from the call.
const fromCall = (text: string, transcript?: string) =>
  `The developer said this on a ringback call about this session, and the voice on the call \
passed it on:

${text}

Your final answer goes back to the call, where the voice tells the developer; if they've hung \
up, the session calls them again.${transcript ? ` The call's transcript so far: ${transcript}` : ''}`

// What the session reads about how one of its calls went. It reaches the model inside the
// next prompt's message, so it says who wrote it: unsigned, Haiku took it for the
// developer's words and said it hadn't been told.
const outcomeNote = (text: string) =>
  `A note from ringback, the mod that calls the developer's phone about this session, on how \
a call went (the developer didn't write this): ${text}`

let isOn = false
// The session is writing its briefing, in a turn this mod started.
let isBriefing = false
let isCalling = false
// Collects, once a second, what the voice passed on for this session.
let collecting: { cancel(): void } | undefined
let isCollecting = false
// A turn of the main loop is running: calls' outcomes wait for it to end.
let isTurning = false
// Calls' outcomes from the voice server, to add to the conversation between turns.
const outcomes: string[] = []

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    await $.command.register({
      name: 'ringback',
      description: 'Call my phone when this session finishes; run again to stop',
      immediate: true,
    })
    return next(e)
  })

  on('command.run', { command: 'ringback' }, async $ => {
    isOn = !isOn
    isBriefing = false
    collecting?.cancel()
    collecting = isOn ? $.clock.every(1000, () => collect($)) : undefined
    $.ui.invalidate('ui.render')
    return { text: isOn ? 'Ringback is on: this session calls your phone when it finishes.' : 'Ringback is off.' }
  })

  // While it's on, the dim line under the prompt says so, where Remote Control shows too.
  // A placeholder until ringback has an icon.
  on('ui.render', { component: 'PromptHint' }, ($, e, next) =>
    isOn ? next({ ...e, props: { ...e.props, tail: isCalling ? '☎ calling your phone…' : '☎ ringback on' } }) : next(e),
  )

  on('turn.start', ($, e, next) => {
    if (e.agentId === undefined) isTurning = true
    return next(e)
  })

  on('turn.complete', async ($, e, next) => {
    if (e.agentId === undefined) {
      isTurning = false
      await addOutcomes($)
    }
    if (isOn && e.agentId === undefined) {
      if (isBriefing) {
        isBriefing = false
        if (e.reason === 'answer') void call($, e.answer)
      } else if (e.reason === 'answer') {
        void finished($, e.answer)
      }
    }
    return next(e)
  })
}

// The voice server on this Mac, over a socket only you can open.
async function voice($: EngineInterface, path: string, body?: object) {
  return $.http.fetch(`http://voice${path}`, {
    method: body ? 'POST' : 'GET',
    headers: { 'content-type': 'application/json' },
    body: body && JSON.stringify(body),
    socketPath: `${await $.env.get('HOME')}/.local/state/ringback/voice.sock`,
  })
}

// A finished turn: news for the call you're on, or else a briefing for a new call.
async function finished($: EngineInterface, answer: string): Promise<void> {
  try {
    const response = await voice($, `/sessions/${await $.session.id()}/news`, { text: answer })
    if (response.ok && JSON.parse(response.text).live) return
  } catch {
    // No voice server: the call below says so.
  }
  if (!isOn) return
  isBriefing = true
  void $.prompt.submit({ text: BRIEFING })
}

// Submits what the voice passed on for this session, each as a prompt of its own. A call's
// outcome isn't a prompt: it's added to the conversation.
async function collect($: EngineInterface): Promise<void> {
  if (!isOn || isCollecting) return
  isCollecting = true
  try {
    const response = await voice($, `/sessions/${await $.session.id()}/messages`)
    if (!response.ok) return
    for (const m of JSON.parse(response.text).messages as { kind?: string; text: string; transcript?: string }[]) {
      if (m.kind === 'outcome') outcomes.push(m.text)
      else void $.prompt.submit({ text: fromCall(m.text, m.transcript) })
    }
  } catch {
    // The voice server isn't running; nothing can be waiting.
  } finally {
    isCollecting = false
  }
  await addOutcomes($)
}

// Adds the calls' outcomes to the conversation, and shows each to you in a dim line. Claude
// reads them on its next turn, and adding them starts none, so they can't set off another
// call. Never while a turn runs: they wait for it to end.
async function addOutcomes($: EngineInterface): Promise<void> {
  while (outcomes.length && !isTurning) {
    const text = outcomes.shift()!
    $.ui.log(text)
    try {
      await $.session.append({ message: { type: 'user', content: [{ type: 'text', text: outcomeNote(text) }] } })
    } catch (e) {
      $.ui.log(`couldn't add a call's outcome to the conversation: ${e}`, { to: 'debug' })
    }
  }
}

// Gives the briefing to the voice server, which asks the switchboard to ring the phone.
async function call($: EngineInterface, answer: string): Promise<void> {
  const [first = '', ...rest] = answer.trim().split('\n')
  const hasSubject = /^\W*subject:/i.test(first)
  const subject = hasSubject ? first.replace(/^\W*subject:\s*/i, '').replace(/\W+$/, '').slice(0, 80) : ''
  const briefing = (hasSubject ? rest.join('\n') : answer).trim()
  const cwd = await $.session.cwd()
  const body = {
    session_id: await $.session.id(),
    project: cwd.split('/').filter(Boolean).pop() ?? 'Claude',
    cwd,
    subject,
    briefing,
  }
  isCalling = true
  $.ui.invalidate('ui.render')
  try {
    const response = await voice($, '/calls', body)
    if (!response.ok) $.ui.toast(`Can't call: ${errorOf(response.text) ?? `the voice server answered ${response.status}`}`)
  } catch {
    $.ui.toast("Can't call: the voice server isn't running (ringback-voice serve)")
  } finally {
    isCalling = false
    $.ui.invalidate('ui.render')
  }
}

function errorOf(text: string): string | undefined {
  try {
    return JSON.parse(text).error
  } catch {
    return undefined
  }
}
