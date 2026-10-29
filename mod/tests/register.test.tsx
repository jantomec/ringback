import { expect, test } from 'claude-code/testing'

// What the engine beneath the mod is asked, and what the tests answer it.
function engine(on: Parameters<Parameters<typeof test>[1]>[1]) {
  const asked = { prompts: [] as string[], fetches: [] as { url: string; init: any }[] }
  on('command.register', (_, e) => ({ value: { command: e.name } }))
  on('session.start', (_, e) => ({ cwd: e.cwd }))
  on('session.id', () => ({ value: 'session-1234' }))
  on('session.cwd', () => ({ value: '/home/someone/Projects/demo' }))
  on('env.get', () => ({ value: '/home/someone' }))
  on('prompt.submit', (_, e) => (asked.prompts.push(e.text), { text: e.text }))
  on('http.fetch', (_, e) => (asked.fetches.push({ url: e.url, init: e.init }), { value: { status: 200, ok: true, headers: {}, text: '{"ok": true}' } }))
  on('ui.invalidate', () => ({ value: undefined }))
  on('turn.complete', () => ({ text: '' }))
  return asked
}

// Lets the mod's unawaited work, such as placing a call, run to its end.
const settle = async () => {
  for (let i = 0; i < 2000; i++) await Promise.resolve()
}

const turn = (answer: string, more: object = {}) =>
  ({ answer, durationMs: 1000, isAborted: false, turnId: 't', reason: 'answer', ...more }) as any

test('off, a finished turn does nothing', async ($, on) => {
  const asked = engine(on)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await $.turn.complete(turn('Done.'))
  expect(asked.prompts.length).toBe(0)
  expect(asked.fetches.length).toBe(0)
})

test('on, a finished turn asks for a briefing, and the briefing places a call', async ($, on) => {
  const asked = engine(on)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  const ran = await $.command.run({ command: 'ringback', args: '' })
  expect(ran.text).toBe('Ringback is on: this session calls your phone when it finishes.')

  await $.turn.complete(turn('All tests pass.'))
  expect(asked.prompts.length).toBe(1)
  expect(asked.prompts[0]).toContain('Subject: ')

  await $.turn.complete(turn('Subject: Tests pass, ready to merge.\n\nI fixed the parser. It needs your review.'))
  await settle()
  expect(asked.fetches.length).toBe(1)
  expect(asked.fetches[0].url).toBe('http://voice/calls')
  expect(asked.fetches[0].init.socketPath).toBe('/home/someone/.local/state/ringback/voice.sock')
  expect(JSON.parse(asked.fetches[0].init.body)).toEqual({
    session_id: 'session-1234',
    project: 'demo',
    subject: 'Tests pass, ready to merge',
    briefing: 'I fixed the parser. It needs your review.',
  })
  // The next finished turn asks for a briefing again.
  await $.turn.complete(turn('Merged.'))
  expect(asked.prompts.length).toBe(2)
})

test('an interrupted briefing places no call', async ($, on) => {
  const asked = engine(on)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await $.command.run({ command: 'ringback', args: '' })
  await $.turn.complete(turn('Done.'))
  await $.turn.complete(turn('Subject: x', { reason: 'aborted', isAborted: true }))
  await settle()
  expect(asked.fetches.length).toBe(0)
})

test("a subagent's turn doesn't count", async ($, on) => {
  const asked = engine(on)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await $.command.run({ command: 'ringback', args: '' })
  await $.turn.complete(turn('Found it.', { agentId: 'agent-1' }))
  expect(asked.prompts.length).toBe(0)
})

test('running /ringback again switches it off', async ($, on) => {
  const asked = engine(on)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await $.command.run({ command: 'ringback', args: '' })
  const ran = await $.command.run({ command: 'ringback', args: '' })
  expect(ran.text).toBe('Ringback is off.')
  await $.turn.complete(turn('Done.'))
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
  const hint = { plugin: 'ringback', component: 'PromptHint', surface: 'terminal', props: { isDraft: false, isWorking: false, hint: '? for shortcuts' } } as const
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })

  let ui = await $.ui.mount(hint)
  expect((await ui.find({ type: 'Text' }))?.text).toBe('? for shortcuts')
  await ui.unmount()

  await $.command.run({ command: 'ringback', args: '' })
  ui = await $.ui.mount(hint)
  expect((await ui.find({ type: 'Text' }))?.text).toBe('? for shortcuts · ☎ ringback on')
  await ui.unmount()
})
