#!/bin/sh
# Builds ringback-speech and fakehub on the speech node, into bin/ of the install directory.
# Usage: speech/build.sh [INSTALL_DIR]   (default: ~/ringback-speech)
# The install directory holds sherpa-onnx/ (its prebuilt libraries and headers),
# onnxruntime/include (the headers of the same ONNX Runtime) and, if Go isn't installed,
# go/ (a Go toolchain); Go's caches go there too.
set -e
DIR=${1:-$HOME/ringback-speech}
cd "$(dirname "$0")"
GO=go
[ -x "$DIR/go/bin/go" ] && GO="$DIR/go/bin/go"
export GOTOOLCHAIN=local GOPATH="$DIR/gopath" GOCACHE="$DIR/gocache" CGO_ENABLED=1
export CGO_CFLAGS="-O2 -I$DIR/sherpa-onnx/include -I$DIR/onnxruntime/include"
export CGO_LDFLAGS="-L$DIR/sherpa-onnx/lib -Wl,-rpath,\$ORIGIN/../sherpa-onnx/lib"
mkdir -p "$DIR/bin"
"$GO" build -o "$DIR/bin/ringback-speech" ./cmd/ringback-speech
"$GO" build -o "$DIR/bin/fakehub" ./cmd/fakehub
echo "built $DIR/bin/ringback-speech and $DIR/bin/fakehub"
