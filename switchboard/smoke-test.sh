#!/bin/sh
# Checks the built switchboard without a phone: its endpoints on localhost, with config and
# state in a throwaway HOME. With --apns it also pushes to a made-up device token through
# the APNs sandbox, signed with your real config and key. Apple answers 400 BadDeviceToken
# only when the signature is valid (a bad one gets 403), and nothing rings.
#
#   switchboard/smoke-test.sh [--apns]     after: swift build -c release --arch arm64 --arch x86_64
set -eu
cd "$(dirname "$0")"
bin="$(swift build -c release --arch arm64 --arch x86_64 --show-bin-path)/ringback-switchboard"
home=$(mktemp -d)
server=
trap 'kill $server 2> /dev/null || true; rm -rf "$home"' EXIT
mkdir -p "$home/.config/ringback"
port=18787
url=http://127.0.0.1:$port
printf 'listen = "127.0.0.1:%s"  # smoke test\n' $port > "$home/.config/ringback/switchboard.toml"

fails=0
check() {  # name, expected, actual
    if [ "$2" = "$3" ]; then echo "ok    $1"; else echo "FAIL  $1: expected $2, got $3"; fails=$((fails + 1)); fi
}
status() { curl -s -o /dev/null -w '%{http_code}' --retry 5 --retry-connrefused --retry-delay 1 "$@"; }
yes_if() { if "$@" > /dev/null; then echo yes; else echo no; fi; }
has() { printf '%s\n' "$1" | grep -qE "$2"; }  # text, pattern

HOME="$home" "$bin" serve > "$home/log" 2>&1 &
server=$!
check "GET /register is refused" 405 "$(status "$url/register")"
check "unknown path" 404 "$(status -d '{}' "$url/nope")"
check "register" 200 "$(status -d '{"token": "aaaa1111bbbb2222", "environment": "sandbox", "device": "Test phone", "notification_token": "cccc3333"}' "$url/register")"
check "register without a token" 400 "$(status -d '{"device": "Test phone"}' "$url/register")"
check "register again, before the notification token arrived" 200 "$(status -d '{"token": "aaaa1111bbbb2222", "environment": "sandbox", "device": "Test phone"}' "$url/register")"
check "event" 200 "$(status -d '{"call_id": "12345678-abcd", "event": "ended", "duration_s": "21.1", "at": "2026-10-28T13:21:02Z"}' "$url/events")"
check "event that isn't JSON" 400 "$(status -d 'not json' "$url/events")"
check "connection check" 200 "$(status -d '{"token": "aaaa1111bbbb2222"}' "$url/check")"
check "connection check before the token arrived" 200 "$(status -d '{}' "$url/check")"
check "call" 200 "$(status -d '{"project": "demo", "subject": "Tests pass", "voice": "http://127.0.0.1:1", "call_id": "0123456789abcdef0123"}' "$url/call")"
check "call without a call ID" 400 "$(status -d '{"project": "demo", "voice": "http://127.0.0.1:1"}' "$url/call")"
check "call with a voice that isn't a URL" 400 "$(status -d '{"project": "demo", "voice": "nope", "call_id": "0123456789abcdef0123"}' "$url/call")"
sleep 1  # the call's push goes out after the answer
kill $server
wait $server 2> /dev/null || true

state="$home/.local/state/ringback"
check "devices lists the phone" yes "$(yes_if has "$(HOME="$home" "$bin" devices)" '…bbbb2222  Test phone  sandbox  registered 20[^ ]+  checked in 20')"
check "registering again keeps the notification token" yes "$(yes_if grep -q cccc3333 "$state/devices.json")"
check "check without APNs keys says so" yes "$(yes_if has "$(HOME="$home" "$bin" check)" 'check → Test phone: no \[apns.sandbox\] key in the config')"
check "registration logged with checks" yes "$(yes_if grep -qE 'registered Test phone \(sandbox\), token …bbbb2222, with connection checks' "$home/log")"
check "event stored with a time" yes "$(yes_if grep -qE '"event":"ended".*"time":"20[0-9-]+T[0-9:]+"' "$state/events.jsonl")"
check "event logged" yes "$(yes_if grep -qE 'call 12345678: ended, duration_s 21.1 \(on the phone at [0-9:]{8}\)' "$home/log")"
check "call logged" yes "$(yes_if grep -qF 'call 01234567: asked by http://127.0.0.1:1 for demo: Tests pass' "$home/log")"
check "call rings with its ID" yes "$(yes_if grep -qF 'call 01234567 → Test phone: no [apns.sandbox] key in the config' "$home/log")"

if [ "${1:-}" = --apns ]; then
    cp ~/.config/ringback/switchboard.toml ~/.config/ringback/AuthKey_*.p8 "$home/.config/ringback/"
    printf '{"%s": {"environment": "sandbox", "device": "made-up phone"}}' \
        0000000000000000000000000000000000000000000000000000000000000000 > "$state/devices.json"
    out=$(HOME="$home" "$bin" ring --project smoke-test)
    echo "      $out" | head -1
    check "APNs accepts the signature" yes "$(yes_if has "$out" 'APNs 400 BadDeviceToken')"
    check "the bad token is forgotten" 0 "$(HOME="$home" "$bin" devices | wc -l | tr -d ' ')"
fi

if [ $fails -gt 0 ]; then
    echo "--- switchboard log"
    cat "$home/log"
    exit 1
fi
