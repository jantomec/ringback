#!/bin/sh
# Builds ringback-render (README.md) into render/build: OpenUSD from conda-forge through pixi
# (pixi.toml), Adobe's glTF and STL plugins (USD-Fileformat-plugins, at ADOBE_TAG) into
# build/plugins, and the renderer beside them. Needs pixi and Xcode; the plugins' source comes
# from GitHub the first time. About a minute and a half the first time, seconds after.
# With `apple`, Apple's fallback instead, ringback-render-apple: Xcode alone, seconds.
#
#   render/build.sh [apple]
set -eu
cd "$(dirname "$0")"
here=$PWD
ADOBE_TAG=2026.07

if [ "${1:-}" = apple ]; then
    mkdir -p build
    swiftc -O -o build/ringback-render-apple apple/main.swift
    echo "built $here/build/ringback-render-apple"
    exit 0
fi

pixi install --quiet
# The environment's folder: pixi can keep it outside the project (detached-environments).
env=$(pixi run --quiet printenv CONDA_PREFIX)
run() { pixi run --quiet "$@"; }
mkdir -p build

if [ ! -d build/adobe-src ]; then
    git clone --quiet --depth 1 --branch "$ADOBE_TAG" https://github.com/adobe/USD-Fileformat-plugins build/adobe-src
fi
if [ ! -f build/plugins/plugin/usd/plugInfo.json ]; then
    # Only glTF and STL; their libraries find each other, and OpenUSD's, without DYLD paths.
    run cmake -S build/adobe-src -B build/adobe -G Ninja -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_PREFIX_PATH="$env" -DPython3_EXECUTABLE="$env/bin/python" \
        -DCMAKE_INSTALL_PREFIX="$here/build/plugins" -DCMAKE_INSTALL_RPATH="@loader_path/../../lib;$env/lib" \
        -DUSD_FILEFORMATS_ENABLE_GLTF=ON -DUSD_FILEFORMATS_ENABLE_STL=ON \
        -DUSD_FILEFORMATS_ENABLE_FBX=OFF -DUSD_FILEFORMATS_ENABLE_OBJ=OFF -DUSD_FILEFORMATS_ENABLE_PLY=OFF \
        -DUSD_FILEFORMATS_ENABLE_SBSAR=OFF -DUSD_FILEFORMATS_ENABLE_SPZ=OFF -DUSD_FILEFORMATS_ENABLE_DRACO=OFF \
        -DUSD_FILEFORMATS_ENABLE_MTLX=OFF -DUSD_FILEFORMATS_ENABLE_ASM=OFF -DUSD_FILEFORMATS_BUILD_TESTS=OFF \
        > build/adobe-configure.log
    run cmake --build build/adobe > build/adobe-build.log
    run cmake --install build/adobe > build/adobe-install.log
fi

run cmake -S . -B build/cmake -G Ninja -DCMAKE_BUILD_TYPE=Release -DCMAKE_PREFIX_PATH="$env" \
    -DPython3_EXECUTABLE="$env/bin/python" > build/configure.log
run cmake --build build/cmake > build/build.log
cp build/cmake/ringback-render build/ringback-render
echo "built $here/build/ringback-render"
