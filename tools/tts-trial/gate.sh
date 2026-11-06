#!/bin/sh
# Runs one command on the speech node's GPU under the shared lock, and only while no call is on.
# Usage: gate.sh COMMAND...
# It takes ~/ringback-models-test/gpu.lock, waits until no ringback-speech worker runs, and starts
# COMMAND in its own process group. If a worker appears during the run (a call is starting), it
# kills COMMAND at once and exits 75; the run's results don't count, and it can be run again.
LOCK=$HOME/ringback-models-test/gpu.lock
exec flock "$LOCK" sh -c '
call_on() {
    pgrep -f "[r]ingback-speech work" >/dev/null && return 0
    nvidia-smi --query-compute-apps=process_name --format=csv,noheader 2>/dev/null | grep -q "[r]ingback-speech"
}
waited=0
while call_on; do
    [ $waited = 0 ] && echo "gate: a call is on, waiting" >&2
    waited=1
    sleep 5
done
setsid "$@" &
pid=$!
while kill -0 $pid 2>/dev/null; do
    if call_on; then
        kill -KILL -- -$pid 2>/dev/null
        wait $pid 2>/dev/null
        echo "gate: a call started, run stopped" >&2
        exit 75
    fi
    sleep 1
done
wait $pid
' gate "$@"
