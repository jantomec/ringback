#!/bin/sh
# Speed-tests the candidates on twelve sentences, one gate at a time, so the GPU lock is free
# between models. What comes out says which are worth a whole run.
cd "${TTS_ROOT:-$HOME/ringback-models-test/tts}"
for job in "$@"; do
    model=${job%%:*}
    extra=
    [ "$job" = "$model" ] || extra=${job#*:}
    echo "== $model $extra $(date +%T)"
    if [ -n "$extra" ]; then
        sh harness/gate.sh python3 harness/trial.py "$model" --sentences screen --set "$extra"
    else
        sh harness/gate.sh python3 harness/trial.py "$model" --sentences screen
    fi
    echo "   exit $? $(date +%T)"
done
