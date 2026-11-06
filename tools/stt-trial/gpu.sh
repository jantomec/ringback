#!/bin/bash
# Runs one GPU job under the speech node's rules: the GPU serves live calls, so wait until no
# call is on, take the shared lock for the one job, and stop it at once if a call comes. Then
# it runs again when the call is over. A call's worker shows up as ringback-speech.
#
#   gpu.sh COMMAND...
LOCK=${GPU_LOCK:-$HOME/ringback-models-test/gpu.lock}
LOG=${GPU_LOG:-$HOME/ringback-models-test/stt/logs/gpu-runs.log}
STOPPED=75

call_on() {
    nvidia-smi --query-compute-apps=pid,process_name,used_memory --format=csv,noheader | grep -q ringback-speech
}

if [ "$1" = --locked ]; then
    shift
    call_on && exit $STOPPED
    echo "$(date +%T) start $*" >>"$LOG"
    setsid "$@" &
    job=$!
    while kill -0 $job 2>/dev/null; do
        if call_on; then
            kill -KILL -- -$job
            wait $job
            echo "$(date +%T) STOPPED: a call came" >>"$LOG"
            exit $STOPPED
        fi
        sleep 1
    done
    wait $job
    rc=$?
    echo "$(date +%T) done ($rc)" >>"$LOG"
    exit $rc
fi

while :; do
    while call_on; do sleep 5; done
    flock "$LOCK" "$0" --locked "$@"
    rc=$?
    [ $rc -eq $STOPPED ] || exit $rc
    echo "A call came; waiting for it to end." >&2
done
