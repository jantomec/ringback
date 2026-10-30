// What a mod sees of its session's environment (tools/env-check): the variables its own `$.env`
// has, and the CLAUDE ones a command it runs (`$.process.run`) gets, when the session starts and
// each time a turn ends. One JSON line each, appended to the file $ENV_CHECK_OUT names; nothing
// without it.
import type { EngineInterface, Register } from 'claude-code'

async function look($: EngineInterface, when: string): Promise<void> {
  if (!(await $.env.get('ENV_CHECK_OUT'))) return
  const { stdout } = await $.process.run(['/usr/bin/printenv'])
  const seen = {
    when,
    env: {
      CLAUDE_CODE_BRIDGE_SESSION_ID: await $.env.get('CLAUDE_CODE_BRIDGE_SESSION_ID'),
      CLAUDE_EFFORT: await $.env.get('CLAUDE_EFFORT'),
      CLAUDECODE: await $.env.get('CLAUDECODE'),
      CLAUDE_CODE_SESSION_ID: await $.env.get('CLAUDE_CODE_SESSION_ID'),
      CLAUDE_CODE_ENTRYPOINT: await $.env.get('CLAUDE_CODE_ENTRYPOINT'),
    },
    printenv: stdout.split('\n').filter(line => line.startsWith('CLAUDE')).map(line => line.split('=')[0]).sort(),
  }
  await $.process.run(['/bin/sh', '-c', 'cat >> "$ENV_CHECK_OUT"'], { stdin: `${JSON.stringify(seen)}\n` })
}

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    await look($, 'session.start')
    return next(e)
  })
  on('turn.complete', async ($, e, next) => {
    await look($, 'turn.complete')
    return next(e)
  })
}
