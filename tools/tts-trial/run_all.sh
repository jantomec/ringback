#!/bin/sh
# The whole trial for the models named, on the speech node, one gate per run so the GPU lock is free
# between them. A run that a call stops starts again once the call is over.
# Usage: run_all.sh MODEL...
cd "${TTS_ROOT:-$HOME/ringback-models-test/tts}"
gated() {
    while :; do
        sh harness/gate.sh python3 harness/trial.py "$@"
        rc=$?
        [ $rc = 75 ] || return $rc
        echo "   a call stopped it; again once it's over"
    done
}
for m in "$@"; do
    echo "== $m $(date +%T)"
    gated "$m"                                                   # every sentence
    gated "$m" --label repeat                                    # again, for the spread of the timings
    gated "$m" --sentences ballast --ballast 3072 --label ballast  # beside 3 GB of something else
done
echo "== done $(date +%T)"
