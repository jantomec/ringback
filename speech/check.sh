#!/bin/sh
# Checks the built speech node against fakehub, on the speech node itself: warming up with a
# turn and speech sent before the models are loaded, speaking, cancelling, hearing (the voice's
# own speech, a recording, the background filter, a pause, an unfinished turn, two streams at
# once), the worker exiting when idle and starting again, and a worker or link that dies,
# with timings and the memory of the node and its worker.
# Usage: speech/check.sh [INSTALL_DIR]   (default: ~/ringback-speech)
# Settings, in the environment: PORT (fakehub's, default 8799), BIN (the folder of the
# install directory with the binaries, default bin), CONFIG (a settings file instead of
# speech.toml; its hub and token aren't used) and IDLE (the worker's idle time, default 5 s).
# The node's log and the audio it made go to a new folder under INSTALL_DIR/scratch.
set -e
DIR=${1:-$HOME/ringback-speech}
cd "$DIR"
. ./env.sh
PORT=${PORT:-8799}
BIN=${BIN:-bin}
IDLE=${IDLE:-5}
TOKEN=$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')
mkdir -p scratch
OUT=$(mktemp -d scratch/check.XXXXXX)
"$BIN/ringback-speech" ${CONFIG:+--config "$CONFIG"} --hub "ws://127.0.0.1:$PORT/speech" --token "$TOKEN" --idle "$IDLE" \
  serve >"$OUT/node.log" 2>&1 &
NODE=$!
trap 'kill $NODE 2>/dev/null; wait $NODE 2>/dev/null; grep "worker [0-9]*:" "$OUT/node.log"; echo "node log: $DIR/$OUT/node.log"' EXIT
# The node finds no hub yet, so it backs off and dials again.
sleep 1
"$BIN/fakehub" --listen "127.0.0.1:$PORT" --token "$TOKEN" --out "$OUT" --pid "$NODE" --idle "${IDLE}s" \
  --wav models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-fp16/test_wavs/0.wav
