import { expect, mock, test } from 'claude-code/testing'

type On = Parameters<Parameters<typeof test>[1]>[1]

// What the engine beneath the mod is asked, and what the tests answer it. The voice server
// answers by path: `live` says whether you're on a call with the session, and `waiting` is
// what the voice passed on for it. `logs` are the dim lines shown in the transcript.
function engine(on: On, voice: { live?: boolean; waiting?: object[] } = {}) {
  const asked = { prompts: [] as string[], fetches: [] as { url: string; init: any }[], logs: [] as string[] }
  on('command.register', (_, e) => ({ value: { command: e.name } }))
  on('session.start', (_, e) => ({ cwd: e.cwd }))
  on('session.id', () => ({ value: 'session-1234' }))
  on('session.cwd', () => ({ value: '/home/someone/Projects/demo' }))
  on('env.get', () => ({ value: '/home/someone' }))
  on('prompt.submit', (_, e) => (asked.prompts.push(e.text), { text: e.text }))
  on('ui.invalidate', () => ({ value: undefined }))
  on('ui.log', (_, e) => {
    if (e.to === 'transcript') asked.logs.push(e.text)
    return { value: undefined }
  })
  on('turn.start', (_, e) => ({ turnId: e.turnId }))
  on('turn.complete', () => ({ text: '' }))
  on('http.fetch', (_, e) => {
    asked.fetches.push({ url: e.url, init: e.init })
    const answer = e.url.endsWith('/news')
      ? { live: voice.live ?? false }
      : e.url.endsWith('/messages')
        ? { messages: voice.waiting?.splice(0) ?? [] }
        : { ok: true }
    return { value: { status: 200, ok: true, headers: {}, text: JSON.stringify(answer) } }
  })
  return asked
}

// Lets the mod's unawaited work, such as placing a call, run to its end.
const settle = async () => {
  for (let i = 0; i < 2000; i++) await Promise.resolve()
}

const turn = (answer: string, more: object = {}) =>
  ({ answer, durationMs: 1000, isAborted: false, turnId: 't', reason: 'answer', ...more }) as any

const start = async ($: any) => {
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  return $.command.run({ command: 'ringback', args: '' })
}

test('off, a finished turn does nothing', async ($, on) => {
  const asked = engine(on)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await $.turn.complete(turn('Done.'))
  await settle()
  expect(asked.prompts.length).toBe(0)
  expect(asked.fetches.length).toBe(0)
})

test('on, a finished turn asks for a briefing, and the briefing places a call', async ($, on) => {
  const asked = engine(on)
  const ran = await start($)
  expect(ran.text).toBe('Ringback is on: this session calls your phone when it finishes.')

  await $.turn.complete(turn('All tests pass.'))
  await settle()
  expect(asked.fetches[0].url).toBe('http://voice/sessions/session-1234/news')
  expect(JSON.parse(asked.fetches[0].init.body)).toEqual({ text: 'All tests pass.' })
  expect(asked.prompts.length).toBe(1)
  expect(asked.prompts[0]).toContain('Subject: ')

  await $.turn.complete(turn('Subject: Tests pass, ready to merge.\n\nI fixed the parser. It needs your review.'))
  await settle()
  const calls = asked.fetches.filter(f => f.url === 'http://voice/calls')
  expect(calls.length).toBe(1)
  expect(calls[0].init.socketPath).toBe('/home/someone/.local/state/ringback/voice.sock')
  expect(JSON.parse(calls[0].init.body)).toEqual({
    session_id: 'session-1234',
    project: 'demo',
    cwd: '/home/someone/Projects/demo',
    subject: 'Tests pass, ready to merge',
    briefing: 'I fixed the parser. It needs your review.',
  })
  // The next finished turn asks for a briefing again.
  await $.turn.complete(turn('Merged.'))
  await settle()
  expect(asked.prompts.length).toBe(2)
})

test("on a call you're on, a finished turn is news for it, not a new call", async ($, on) => {
  const asked = engine(on, { live: true })
  await start($)
  await $.turn.complete(turn('Pushed the branch.'))
  await settle()
  expect(asked.fetches.map(f => f.url)).toEqual(['http://voice/sessions/session-1234/news'])
  expect(asked.prompts.length).toBe(0)
})

test('what the voice passes on becomes a prompt, with the transcript', async ($, on) => {
  const waiting = [{ text: 'The developer says: push it.', transcript: '.ringback/calls/2026-10-29-1412.md' }]
  const asked = engine(on, { waiting })
  const clock = mock.clock(on)
  await start($)
  await clock.advance(1000)
  await settle()
  expect(asked.prompts.length).toBe(1)
  expect(asked.prompts[0]).toContain('The developer says: push it.')
  expect(asked.prompts[0]).toContain(".ringback/calls/2026-10-29-1412.md")
  // Collected once: the next second brings nothing new.
  await clock.advance(1000)
  await settle()
  expect(asked.prompts.length).toBe(1)
})

const unanswered = `The call about "Tests pass" wasn't answered within 1 min 30 s: declined, missed, or its audio didn't connect.`
const answered = 'The call about "Tests pass" was answered and lasted 1 min 5 s. Its transcript: .ringback/calls/x.md'

test("a call's outcome is added to the conversation and shown to you, not submitted", async ($, on) => {
  const waiting = [
    { kind: 'message', text: 'The developer says: push it.', transcript: null },
    { kind: 'outcome', text: unanswered, transcript: null },
  ]
  const asked = engine(on, { waiting })
  const session = mock.session(on)
  const clock = mock.clock(on)
  await start($)
  await clock.advance(1000)
  await settle()
  // The voice's message is still a prompt; the outcome isn't.
  expect(asked.prompts.length).toBe(1)
  expect(asked.prompts[0]).toContain('The developer says: push it.')
  const rows = session.appended()
  expect(rows.length).toBe(1)
  expect(rows[0].door).toBe('note')
  expect(rows[0].message.type).toBe('user')
  // Signed, since the model reads it inside the next prompt's message.
  expect(rows[0].message.content).toEqual([{ type: 'text', text: expect.stringContaining(unanswered) }])
  expect((rows[0].message.content[0] as { text: string }).text).toStartWith('A note from ringback')
  expect(asked.logs).toEqual([unanswered])
})

test("a call's outcome waits while a turn runs, and is added when it ends", async ($, on) => {
  const waiting: object[] = []
  const asked = engine(on, { waiting })
  const session = mock.session(on)
  const clock = mock.clock(on)
  await start($)
  await $.turn.start({ text: 'Fix the parser.', turnId: 't' })
  waiting.push({ kind: 'outcome', text: answered, transcript: '.ringback/calls/x.md' })
  await clock.advance(1000)
  await settle()
  expect(session.appended().length).toBe(0)
  expect(asked.logs.length).toBe(0)
  // A subagent's turn is no turn of the session's.
  await $.turn.start({ text: 'Find the parser.', turnId: 'a', agentId: 'agent-1' })
  await $.turn.complete(turn('Found it.', { turnId: 'a', agentId: 'agent-1' }))
  await settle()
  expect(session.appended().length).toBe(0)

  await $.turn.complete(turn('Fixed the parser.'))
  await settle()
  expect(session.appended().map(row => row.message.content)).toEqual([
    [{ type: 'text', text: expect.stringContaining(answered) }],
  ])
  expect(asked.logs).toEqual([answered])
  // The turn's end still asks for a briefing, as with no outcome waiting.
  expect(asked.prompts.length).toBe(1)
  expect(asked.prompts[0]).toContain('Subject: ')
})

test('an interrupted briefing places no call', async ($, on) => {
  const asked = engine(on)
  await start($)
  await $.turn.complete(turn('Done.'))
  await settle()
  await $.turn.complete(turn('Subject: x', { reason: 'aborted', isAborted: true }))
  await settle()
  expect(asked.fetches.filter(f => f.url === 'http://voice/calls').length).toBe(0)
})

test("a subagent's turn doesn't count", async ($, on) => {
  const asked = engine(on)
  await start($)
  await $.turn.complete(turn('Found it.', { agentId: 'agent-1' }))
  await settle()
  expect(asked.prompts.length).toBe(0)
  expect(asked.fetches.length).toBe(0)
})

test('running /ringback again switches it off', async ($, on) => {
  const asked = engine(on)
  await start($)
  const ran = await $.command.run({ command: 'ringback', args: '' })
  expect(ran.text).toBe('Ringback is off.')
  await $.turn.complete(turn('Done.'))
  await settle()
  expect(asked.prompts.length).toBe(0)
})

test('while on, the line under the prompt says so', async ($, on) => {
  engine(on)
  // Stands for the engine's own line: the hint, and the tail a hook adds.
  on('ui.render', ($, e) => {
    const { Text } = $.ui.resolve(e)
    const props = e.props as { hint: string; tail?: string }
    return <Text>{props.hint + (props.tail ? ` · ${props.tail}` : '')}</Text>
  })
  const hint = {
    plugin: 'ringback',
    component: 'PromptHint',
    surface: 'terminal',
    props: { isDraft: false, isWorking: false, hint: '? for shortcuts' },
  } as const
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })

  let ui = await $.ui.mount(hint)
  expect((await ui.find({ type: 'Text' }))?.text).toBe('? for shortcuts')
  await ui.unmount()

  await $.command.run({ command: 'ringback', args: '' })
  ui = await $.ui.mount(hint)
  expect((await ui.find({ type: 'Text' }))?.text).toBe('? for shortcuts · ☎ ringback on')
  await ui.unmount()
})
