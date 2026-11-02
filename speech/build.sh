#!/bin/sh
# Builds ringback-speech and fakehub on the speech node, into bin/ of the install directory.
# Usage: speech/build.sh [INSTALL_DIR [BIN]]   (default: ~/ringback-speech, bin)
# BIN is another folder of the install directory to build into, such as bin-next, to try a
# build beside the one that runs. The install directory holds sherpa-onnx/ (its prebuilt
# libraries and headers), onnxruntime/include (the headers of the same ONNX Runtime) and, if
# Go isn't installed, go/ (a Go toolchain); Go's caches go there too.
set -e
DIR=${1:-$HOME/ringback-speech}
BIN=${2:-bin}
cd "$(dirname "$0")"
GO=go
[ -x "$DIR/go/bin/go" ] && GO="$DIR/go/bin/go"
export GOTOOLCHAIN=local GOPATH="$DIR/gopath" GOCACHE="$DIR/gocache" CGO_ENABLED=1
export CGO_CFLAGS="-O2 -I$DIR/sherpa-onnx/include -I$DIR/onnxruntime/include"
export CGO_LDFLAGS="-L$DIR/sherpa-onnx/lib -Wl,-rpath,\$ORIGIN/../sherpa-onnx/lib"
mkdir -p "$DIR/$BIN"
"$GO" build -o "$DIR/$BIN/ringback-speech" ./cmd/ringback-speech
"$GO" build -o "$DIR/$BIN/fakehub" ./cmd/fakehub
echo "built $DIR/$BIN/ringback-speech and $DIR/$BIN/fakehub"
