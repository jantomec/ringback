// The ringback mod's contract. `$.state` keeps whether `/ringback` is on in this session,
// which lasts through a reload of the mod; until it's first switched, the mod reads its
// store, which keeps each session that has it on across sessions: under "on:<session ID>",
// when that session last started with it on (milliseconds since 1970).
export type RingbackRemembered = number

declare module 'claude-code' {
  interface PluginState {
    ringback: { isOn: boolean }
  }
}
