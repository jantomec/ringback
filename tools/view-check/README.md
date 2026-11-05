# View check

A model's live view ("Showing you things" in `docs/design.md`) through the real hub, agent and
renderer on this computer, with a stand-in phone (aiortc) that opens the view as the app does
and decodes its video. The hub and the agent run with their config and state in a throwaway
HOME, the agent linked to the hub with a token.

It checks, and prints the times:
- a session's agent shows a small animated model (a box that moves across a floor), its
  picture rendered, and the phone pairs;
- an image has no view (404), and a model gone from its computer is refused, saying where it
  was (410);
- the phone's view opens; when its first frame comes, and how long gathering its addresses
  and the hub's answer took; the renderer's ready reaches the phone with the model's frames
  and frame rate; the frames are as big as asked (640 × 480);
- turning the camera changes the picture, and how soon; still, no more frames come;
- it plays, and goes to the last frame, paused;
- closing the view stops the renderer.

Build the hub, the agent and the renderer first, then run from the repo root (about 20 s):

    (cd hub && go build -o build/ringback-hub .) && (cd agent && go build -o build/ringback-agent .)
    render/build.sh apple      # on a Mac without OpenUSD's renderer; render/build.sh for it
    uv run --project tools tools/view-check/view_check.py [--keep DIR]

`RINGBACK_HUB`, `RINGBACK_AGENT` and `RINGBACK_RENDER` name other builds. With `--keep`, the
logs, the state and the first and turned frames (`first.ppm`, `turned.ppm`) stay in DIR.

Result on 2026-11-05, on a recent Apple silicon Mac with Apple's fallback renderer, three runs,
every check passing in each: a turn of the camera changed the picture 0.069–0.073 s after it
was sent, all on one computer, so with no network between; the decoded frames show the model,
turned. The first frame came 5.1 s after the phone opened the view in the second run, all of
it the stand-in's own address gathering trying a STUN server, which the app doesn't use; with
none, in the third, 0.14 s.
