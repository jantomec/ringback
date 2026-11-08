#!/bin/sh
# The overlap and load measurements, one gate each, so the GPU lock is free between them. A run that
# a call stops starts again once the call is over.
cd "${TTS_ROOT:-$HOME/ringback-models-test/tts}"
gated() {
    while :; do
        sh harness/gate.sh python3 harness/overlap.py "$@"
        rc=$?
        [ $rc = 75 ] || return $rc
        echo "   a call stopped it; again once it's over"
    done
}
for n in 1 2 3; do echo "== workers ming $n $(date +%T)"; gated workers ming $n; done
for n in 2 3; do echo "== cut ming $n $(date +%T)"; gated cut ming $n; done
echo "== parakeet $(date +%T)"; gated parakeet ming
for n in 1 2 3; do echo "== workers phonon $n $(date +%T)"; gated workers phonon $n; done
for n in 2 3; do echo "== cut phonon $n $(date +%T)"; gated cut phonon $n; done
for n in 3 8 24; do
    for seconds in 1 2; do
        for strategy in today anchor; do
            echo "== replay $n $seconds $strategy $(date +%T)"
            gated replay ming $n $seconds $strategy
        done
    done
done
echo "== parakeet replay $(date +%T)"; gated replay ming 24 2 anchor --parakeet
echo "== overlap done $(date +%T)"
