# Rendering 3D models

When a session shows you a 3D model, the computer that runs the session renders it, and the
model stays there ("Showing you things" in `docs/design.md`). The agent runs one of two
renderers, which take the same commands:

| Renderer | What it is | Opens |
|---|---|---|
| `ringback-render` | OpenUSD's own renderer, Hydra Storm, headless (on Metal on a Mac) | USD, USDA, USDC, USDZ; glTF, GLB and STL through Adobe's file format plugins |
| `ringback-render-apple` | Apple's fallback, SceneKit and Model I/O, for a Mac without OpenUSD | USD, USDA, USDC, USDZ, STL, OBJ, PLY; not glTF or GLB. It mishandles some USD files |

The agent uses the config's `render` (`agent.example.toml`), or else the first of these it finds
beside itself or on the PATH: `ringback-render`, then on a Mac `ringback-render-apple`. With
neither, a session that shows a model is told that this computer can't render 3D models and
what it lacks; a renderer that can't open a file says why, and the session is told that too.

## Commands

    ringback-render still MODEL OUT.png [WIDTH HEIGHT]

renders the model whole, from three quarters in front and above, at its first frame, into a
PNG (1200 × 900 by default), and prints its facts as JSON: `{"animated", "frames", "fps",
"start", "end", "up"}`: whether it has an animation, how many frames at how many a second,
where it starts and ends in its own time, and its up axis (`Y` or `Z`; an STL is taken as Z
up). It exits 1 with why on stderr if it can't. `ringback-render-apple` also takes the frame to
render (from 0) after the size, and `info`, which prints `{"renderer", "formats"}`.

## Building

    render/build.sh          # ringback-render
    render/build.sh apple    # ringback-render-apple, with Xcode alone, in seconds

`ringback-render` builds against OpenUSD from conda-forge (`pixi.toml`, through pixi, about
1.4 GB) and Adobe's USD-Fileformat-plugins (cloned from GitHub at the tag in `build.sh`, glTF
and STL only), into `render/build`, which git ignores. The binary finds the plugins beside
itself. Link it (or `ringback-render-apple`) into the folder with `ringback-agent`, such as
`~/.local/bin`, and restart the agent (`ringback-agent install`).

Not yet: `ringback-render` on Linux, against the system's OpenUSD with an EGL context (it uses
Metal now), and the live view, where the renderer streams the model as you turn it.
