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
body() { curl -s --retry 5 --retry-connrefused --retry-delay 1 "$@"; }
yes_if() { if "$@" > /dev/null; then echo yes; else echo no; fi; }
has() { printf '%s\n' "$1" | grep -qE "$2"; }  # text, pattern
has_text() { printf '%s\n' "$1" | grep -qF "$2"; }  # text, text it contains

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

# Work hours, in a time zone where it's between 12:00 and 13:00 now, so the hours below hold
# or ring a call whenever this runs. Etc/GMT-N is N hours ahead of UTC.
zone_ahead() {  # hours ahead of UTC: the zone's name
    if [ "$1" -gt 0 ]; then echo "Etc/GMT-$1"; elif [ "$1" -lt 0 ]; then echo "Etc/GMT+$((-$1))"; else echo Etc/GMT; fi
}
utc_hour=$(date -u +%H)
ahead=$((12 - ${utc_hour#0}))
zone=$(zone_ahead $ahead)
zone2=$(zone_ahead $((ahead - 1)))  # an hour behind, where 14:00 hasn't come yet either
# In a pattern, the + of Etc/GMT+2 must be a plus.
zone_re=$(printf '%s' "$zone" | sed 's/+/\\+/g') zone2_re=$(printf '%s' "$zone2" | sed 's/+/\\+/g')
today=$(TZ=$zone date +%Y-%m-%d)
offset=$(TZ=$zone date +%z | sed -E 's/^\+0000$/Z/; s/([0-9]{2})([0-9]{2})$/\1:\2/')
phone='"token": "aaaa1111bbbb2222", "environment": "sandbox", "device": "Test phone"'
second='"token": "dddd4444eeee5555", "environment": "sandbox", "device": "Second phone"'
hours() {  # phone, from, to[, time zone]: registers it with these work hours
    status -d "{$1, \"hours_from\": \"$2\", \"hours_to\": \"$3\", \"time_zone\": \"${4:-$zone}\"}" "$url/register"
}
check_in() {  # from, to, time zone: a connection check that brings work hours
    status -d "{\"token\": \"aaaa1111bbbb2222\", \"hours_from\": \"$1\", \"hours_to\": \"$2\", \"time_zone\": \"$3\"}" "$url/check"
}
# Bodies with quotes are built outside "$(...)": macOS's sh keeps the backslashes in there.
# The refused ones would hold the call if they were taken.
with_zone="{$phone, \"hours_from\": \"08:00\", \"hours_to\": \"20:00\", \"time_zone\": \"$zone\"}"
no_zone="{$phone, \"hours_from\": \"14:00\", \"hours_to\": \"16:00\"}"
only_from="{$phone, \"hours_from\": \"14:00\", \"time_zone\": \"$zone\"}"
call_as() { body -d "{\"project\": \"demo\", \"voice\": \"http://127.0.0.1:1\", \"call_id\": \"$1\"}" "$url/call"; }
held_until() { echo "{\"held_until\":\"${today}T$1:00$offset\",\"ok\":true}"; }
rings='{"ok":true}'
echo "      work hours in $zone, where it's $(TZ=$zone date +%H:%M)"
check "register with work hours 14:00–16:00" 200 "$(hours "$phone" 14:00 16:00)"
check "outside them, a call is held until they start" "$(held_until 14:00)" "$(call_as held0001-000000000000)"
check "register with 22:00–06:00, past midnight" 200 "$(hours "$phone" 22:00 06:00)"
check "outside 22:00–06:00, held until 22:00" "$(held_until 22:00)" "$(call_as held0002-000000000000)"
check "register with 10:00–02:00, past midnight" 200 "$(hours "$phone" 10:00 02:00)"
check "inside 10:00–02:00, it rings" "$rings" "$(call_as ring0001-000000000000)"
check "register with 08:00–20:00" 200 "$(hours "$phone" 08:00 20:00)"
check "inside 08:00–20:00, it rings" "$rings" "$(call_as ring0002-000000000000)"
check "register with 09:00–09:00, any time" 200 "$(hours "$phone" 09:00 09:00)"
check "with From and To equal, it rings" "$rings" "$(call_as ring0003-000000000000)"
check "register with 08:00–20:00, from a variable" 200 "$(status -d "$with_zone" "$url/register")"
check "work hours in a time zone the switchboard doesn't know: 400" 400 "$(hours "$phone" 14:00 16:00 Mars/Olympus)"
check "a time that isn't HH:MM: 400" 400 "$(hours "$phone" 14:00 6:00)"
check "a time past 23:59: 400" 400 "$(hours "$phone" 14:00 24:00)"
check "work hours without a time zone: 400" 400 "$(status -d "$no_zone" "$url/register")"
check "only From: 400" 400 "$(status -d "$only_from" "$url/register")"
check "with those refused, the last hours still hold: it rings" "$rings" "$(call_as ring0004-000000000000)"
check "register with 14:00–16:00 again" 200 "$(hours "$phone" 14:00 16:00)"
check "a second phone with 13:30–13:45" 200 "$(hours "$second" 13:30 13:45)"
check "outside both, held until the earlier start" "$(held_until 13:30)" "$(call_as held0003-000000000000)"
check "the second phone again, without hours" 200 "$(status -d "{$second}" "$url/register")"
check "its hours are gone, and a phone without them is always inside: it rings" "$rings" "$(call_as ring0005-000000000000)"
check "the second phone with 13:30–13:45 again" 200 "$(hours "$second" 13:30 13:45)"
check "a connection check brings a new time zone" 200 "$(check_in 14:00 16:00 "$zone2")"
check "a check with hours that can't be read still gets a 200" 200 "$(check_in 14:00 16:00 Mars/Olympus)"
check "a check without hours keeps them: still held" "$(held_until 13:30)" "$(status -d '{"token": "aaaa1111bbbb2222"}' "$url/check" > /dev/null; call_as held0004-000000000000)"
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
check "registration logged with work hours" yes "$(yes_if grep -qF "registered Test phone (sandbox), token …bbbb2222, work hours 14:00–16:00 $zone" "$home/log")"
check "a held call logged" yes "$(yes_if grep -qF "call held0001: held until ${today}T14:00:00$offset, outside the work hours of every phone" "$home/log")"
check "held calls ring nobody" no "$(yes_if grep -qE 'call held000[0-9] →' "$home/log")"
check "calls inside the hours ring" 5 "$(grep -cE 'call ring000[1-5] → Test phone: no \[apns.sandbox\] key' "$home/log")"
check "unreadable hours from a check are logged and left out" yes "$(yes_if grep -qF 'check from …bbbb2222: work hours that can'"'"'t be read, left out' "$home/log")"
devices=$(HOME="$home" "$bin" devices)
check "devices shows the hours, in the time zone the check brought" yes "$(yes_if has "$devices" "…bbbb2222  Test phone .*  work hours 14:00–16:00 $zone2_re\$")"
check "devices shows the second phone's hours" yes "$(yes_if has "$devices" "…eeee5555  Second phone .*  work hours 13:30–13:45 $zone_re\$")"
out=$(HOME="$home" "$bin" ring --project smoke-test)
check "ring ignores work hours: it pushes to both phones" 2 "$(printf '%s\n' "$out" | grep -c 'no \[apns.sandbox\] key')"

# Waiting calls: held ones are kept in waiting.json and listed for the app, soonest first,
# until they ring, the app calls one now, or their voice server stops asking about them (after
# RINGBACK_WAITING_STALE s here instead of 25 min). Both phones are outside their hours now.
rm -f "$state/waiting.json"
serve_waiting() {
    HOME="$home" RINGBACK_WAITING_STALE=4 "$bin" serve >> "$home/log" 2>&1 &
    server=$!
}
listed() { body -d '{"token": "aaaa1111bbbb2222"}' "$url/waiting" | grep -oE 'wait[0-9]{4}' | tr '\n' ' ' | sed 's/ $//'; }
call_about() {  # call ID, subject
    body -d "{\"project\": \"demo\", \"subject\": \"$2\", \"voice\": \"http://127.0.0.1:1\", \"call_id\": \"$1\"}" "$url/call"
}
called_now='{"call_id": "wait0002-000000000000", "event": "called now", "at": "2026-10-30T09:00:00Z"}'
serve_waiting
check "waiting calls for a token that isn't registered: 403" 403 "$(status -d '{"token": "ffff9999"}' "$url/waiting")"
check "waiting calls without a token: 403" 403 "$(status -d '{}' "$url/waiting")"
check "no calls wait yet" '{"calls":[]}' "$(body -d '{"token": "aaaa1111bbbb2222"}' "$url/waiting")"
check "a held call waits" "$(held_until 13:30)" "$(call_about wait0001-000000000000 "Docs updated")"
check "the second phone with 13:15–13:20" 200 "$(hours "$second" 13:15 13:20)"
check "another, held until sooner" "$(held_until 13:15)" "$(call_about wait0002-000000000000 "Parser fixed")"
check "both are listed, soonest first" "wait0002 wait0001" "$(listed)"
waiting=$(body -d '{"token": "aaaa1111bbbb2222"}' "$url/waiting")
check "with project, subject, voice server and when it rings" yes "$(yes_if has_text "$waiting" \
    "\"call_id\":\"wait0002-000000000000\",\"held_until\":\"${today}T13:15:00$offset\",\"project\":\"demo\",\"subject\":\"Parser fixed\",\"voice\":\"http:")"
kill $server
wait $server 2> /dev/null || true
serve_waiting
check "after a restart, both still wait" "wait0002 wait0001" "$(listed)"
check "asked again, a call still held stays" "$(held_until 13:15)" "$(call_about wait0002-000000000000 "Parser fixed")"
check "the first phone with any hours" 200 "$(hours "$phone" 09:00 09:00)"
check "a waiting call that rings" "$rings" "$(call_about wait0001-000000000000 "Docs updated")"
check "no longer waits" "wait0002" "$(listed)"
check "the phone reports it called one now" 200 "$(status -d "$called_now" "$url/events")"
check "which no longer waits" "" "$(listed)"
check "the first phone with 14:00–16:00 again" 200 "$(hours "$phone" 14:00 16:00)"
check "a held call waits" "wait0003" "$(call_about wait0003-000000000000 "Tests pass" > /dev/null; listed)"
sleep 5
check "not asked about again in time, it's dropped" "" "$(listed)"
sleep 1
kill $server
wait $server 2> /dev/null || true
check "a waiting call that rings is logged" yes "$(yes_if grep -qF 'call wait0001: no longer waiting, it rings' "$home/log")"
check "one called from the app is logged" yes "$(yes_if grep -qF 'call wait0002: no longer waiting, called from the app' "$home/log")"
check "a dropped one is logged" yes "$(yes_if grep -qE 'call wait0003: no longer waiting, its voice server hasn.t asked about it since [0-9:]{8}' "$home/log")"

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
