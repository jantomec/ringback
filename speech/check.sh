#!/bin/sh
# Checks the built speech node against fakehub, on the speech node itself: speaking,
# cancelling, hearing (Kokoro's own speech, a recording, the background filter, a pause, an
# unfinished turn, two streams at once), with timings and the node's memory.
# Usage: speech/check.sh [INSTALL_DIR]   (default: ~/ringback-speech)
# The node's log and the audio it made go to a new folder under INSTALL_DIR/scratch.
set -e
DIR=${1:-$HOME/ringback-speech}
cd "$DIR"
. ./env.sh
PORT=${PORT:-8799}
TOKEN=$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')
mkdir -p scratch
OUT=$(mktemp -d scratch/check.XXXXXX)
bin/ringback-speech --hub "ws://127.0.0.1:$PORT/speech" --token "$TOKEN" serve >"$OUT/node.log" 2>&1 &
NODE=$!
trap 'kill $NODE 2>/dev/null; wait $NODE 2>/dev/null; echo "node log: $DIR/$OUT/node.log"' EXIT
# The node loads its models and finds no hub yet, so it backs off and dials again.
sleep 4
bin/fakehub --listen "127.0.0.1:$PORT" --token "$TOKEN" --out "$OUT" --pid "$NODE" \
  --wav models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-fp16/test_wavs/0.wav
