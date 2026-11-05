# Rendering 3D models

When a session shows you a 3D model, the computer that runs the session renders it, and the
model stays there ("Showing you things" in `docs/design.md`). The agent runs one of two
renderers, which take the same commands:

| Renderer | What it is | Opens |
|---|---|---|
| `ringback-render` | OpenUSD's own renderer, Hydra Storm, headless: on Metal on a Mac, on OpenGL through EGL on Linux (no display needed) | USD, USDA, USDC, USDZ; glTF, GLB and STL through Adobe's file format plugins |
| `ringback-render-apple` | Apple's fallback, SceneKit and Model I/O, for a Mac without OpenUSD | USD, USDA, USDC, USDZ, STL, OBJ, PLY; not glTF or GLB. It mishandles some USD files |

The agent uses the config's `render` (`agent.example.toml`), or else the first of these it finds
beside itself or on the PATH: `ringback-render`, then on a Mac `ringback-render-apple`. With
neither, a session that shows a model is told that this computer can't render 3D models and
what it lacks; a renderer that can't open a file says why, and the session is told that too.

## Commands

    ringback-render still MODEL OUT.png [WIDTH HEIGHT [FRAME]]

renders the model whole, from three quarters in front and above, at its first frame (or at
FRAME, from 0, after the size), into a PNG (1200 × 900 by default), framed to hold it over its
whole animation, and prints its facts as JSON: `{"animated", "frames", "fps",
"start", "end", "up"}`: whether it has an animation, how many frames at how many a second,
where it starts and ends in its own time, and its up axis (`Y` or `Z`; an STL is taken as Z
up). It exits 1 with why on stderr if it can't. `info` prints `{"renderer", "formats"}`.
Apple's fallback frames the model as it stands at its first frame only.

## Building

    render/build.sh          # ringback-render
    render/build.sh apple    # ringback-render-apple, with Xcode alone, in seconds

`ringback-render` builds against OpenUSD, and Adobe's USD-Fileformat-plugins (cloned from
GitHub at the tag in `build.sh`, glTF and STL only), into `render/build`, which git ignores:
- on a Mac, OpenUSD from conda-forge (`pixi.toml`, through pixi, about 1.4 GB);
- on Linux, the system's (`USD_ROOT`, `/usr` by default), with CMake, Ninja and a C++
  compiler. Built on Arch Linux's `usd` 26.11 (monolithic, GCC 16) with an NVIDIA card: 23 s.

The binary finds the plugins beside itself. Link it (or `ringback-render-apple`) into the
folder with `ringback-agent`, such as `~/.local/bin`, and restart the agent
(`ringback-agent install`).

Not yet: the live view, where the renderer streams the model as you turn it.
