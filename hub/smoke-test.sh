#!/bin/sh
# Checks the built hub without a phone: the phone's endpoints on localhost, with config and
# state in a throwaway HOME, and calls and sessions as the agents give them, over their link.
# Stand-ins (cmd/standin, built here) link to the hub as agents and as a speech node, and take
# plain HTTP, so curl drives them too. With --apns it also pushes to a made-up device token
# through the APNs sandbox, signed with your real config and key. Apple answers 400
# BadDeviceToken only when the signature is valid (a bad one gets 403), and nothing rings.
#
#   hub/smoke-test.sh [--apns]     after: go build -o build/ringback-hub .  (macOS or Linux)
set -eu
cd "$(dirname "$0")"
# RINGBACK_HUB runs the same checks against another build of the hub.
bin="${RINGBACK_HUB:-$PWD/build/ringback-hub}"
home=$(mktemp -d)
server= standin=
trap 'kill $server $standin 2> /dev/null || true; rm -rf "$home"' EXIT
mkdir -p "$home/.config/ringback"
port=18787
url=http://127.0.0.1:$port
agent=http://127.0.0.1:18788  # the stand-ins
printf 'listen = "127.0.0.1:%s"  # smoke test\n[tokens]\nagent = "smoke-agent-token-0001"\nspeech = "smoke-speech-token-001"\n' \
    $port > "$home/.config/ringback/hub.toml"
go build -o "$home/standin" ./cmd/standin
"$home/standin" -hub 127.0.0.1:$port -listen 127.0.0.1:18788 \
    -agent-token smoke-agent-token-0001 -speech-token smoke-speech-token-001 2> "$home/standin.log" &
standin=$!

fails=0
check() {  # name, expected, actual
    if [ "$2" = "$3" ]; then echo "ok    $1"; else echo "FAIL  $1: expected $2, got $3"; fails=$((fails + 1)); fi
}
status() { curl -s -o /dev/null -w '%{http_code}' --retry 5 --retry-connrefused --retry-delay 1 "$@"; }
body() { curl -s --retry 5 --retry-connrefused --retry-delay 1 "$@"; }
yes_if() { if "$@" > /dev/null; then echo yes; else echo no; fi; }
has() { printf '%s\n' "$1" | grep -qE "$2"; }  # text, pattern
has_text() { printf '%s\n' "$1" | grep -qF "$2"; }  # text, text it contains
perms() { if [ "$(uname -s)" = Darwin ]; then stat -f %Lp "$1"; else stat -c %a "$1"; fi; }  # a file's, in octal
days_ago() { date -u -v-"$1"d +%Y-%m-%dT%H:%M:%SZ 2> /dev/null || date -u -d "$1 days ago" +%Y-%m-%dT%H:%M:%SZ; }

HOME="$home" "$bin" serve > "$home/log" 2>&1 &
server=$!
check "GET /register is refused" 405 "$(status "$url/register")"
check "unknown path" 404 "$(status -d '{}' "$url/nope")"
check "no calls over plain HTTP: /call is 404" 404 "$(status -d '{"project": "demo", "call_id": "0123456789abcdef0123"}' "$url/call")"
check "no sessions over plain HTTP: /sessions is 404" 404 "$(status -d '{"sessions": []}' "$url/sessions")"
# A phone pairs with a one-time code from `ringback-hub pair`, which it trades at /pair for its
# key; every request that names it by its push token then carries the key ("$me").
code_of() { HOME="$home" "$bin" pair | grep -oE 'code=[A-Za-z0-9_-]+' | cut -d= -f2; }
key_of() { printf '%s' "$1" | grep -oE '"key":"[0-9a-f]+"' | cut -d'"' -f4; }
before='{"token": "aaaa1111bbbb2222", "environment": "sandbox", "device": "Test phone"}'
check "register before pairing: 401, not paired" '{"ok": false, "error": "not paired"}' "$(body -d "$before" "$url/register")"
code1=$(code_of)
pair1="{\"code\": \"$code1\", \"token\": \"aaaa1111bbbb2222\", \"environment\": \"sandbox\", \"device\": \"Test phone\", \"notification_token\": \"cccc3333\"}"
k1=$(key_of "$(body -d "$pair1" "$url/pair")")
check "pairing with a code from ringback-hub pair gives a key" 64 "${#k1}"
check "the same code again: 403" 403 "$(status -d "$pair1" "$url/pair")"
made_up=$(printf '%s' "$pair1" | sed "s/$code1/not-a-code/")
check "a made-up code: 403, not valid" '{"ok": false, "error": "pairing code not valid"}' "$(body -d "$made_up" "$url/pair")"
check "the key isn't kept, only its hash" no "$(yes_if grep -q "$k1" "$home/.local/state/ringback/devices.json")"
check "nor the codes, which only the hub's user can read" "600 no" \
    "$(perms "$home/.local/state/ringback/pairing.json") $(yes_if grep -q "$code1" "$home/.local/state/ringback/pairing.json")"
me="\"token\": \"aaaa1111bbbb2222\", \"key\": \"$k1\""
me_only="{$me}"
me_full="{$me, \"environment\": \"sandbox\", \"device\": \"Test phone\", \"notification_token\": \"cccc3333\"}"
me_again="{$me, \"environment\": \"sandbox\", \"device\": \"Test phone\"}"
no_key='{"token": "aaaa1111bbbb2222"}'
wrong_key='{"token": "aaaa1111bbbb2222", "key": "0000000000000000000000000000000000000000000000000000000000000000"}'
check "register" 200 "$(status -d "$me_full" "$url/register")"
check "register without a token" 400 "$(status -d '{"device": "Test phone"}' "$url/register")"
check "register with a wrong key: 401" 401 "$(status -d "$wrong_key" "$url/register")"
check "register again, before the notification token arrived" 200 "$(status -d "$me_again" "$url/register")"
check "event" 200 "$(status -d '{"call_id": "12345678-abcd", "event": "ended", "duration_s": "21.1", "at": "2026-10-28T13:21:02Z"}' "$url/events")"
check "event that isn't JSON" 400 "$(status -d 'not json' "$url/events")"
check "connection check" 200 "$(status -d "$me_only" "$url/check")"
check "connection check before the token arrived" 200 "$(status -d '{}' "$url/check")"
check "connection check without the key: 401" 401 "$(status -d "$no_key" "$url/check")"
check "an agent's link with a wrong token: 401" 401 "$(status -H 'Authorization: Bearer wrong' "$url/agent")"
check "an agent places a call" 200 "$(status -d '{"project": "demo", "subject": "Tests pass", "call_id": "0123456789abcdef0123"}' "$agent/place")"
status -d '{"call_id": "0123456789abcdef0123", "event": "ended"}' "$url/events" > /dev/null  # over: the line is free
check "a call without a call ID: 400" 400 "$(status -d '{"project": "demo"}' "$agent/place")"
check "a call without a project: 400" 400 "$(status -d '{"call_id": "0123456789abcdef0123"}' "$agent/place")"

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
phone="$me, \"environment\": \"sandbox\", \"device\": \"Test phone\""
hours() {  # phone, from, to[, time zone]: registers it with these work hours
    status -d "{$1, \"hours_from\": \"$2\", \"hours_to\": \"$3\", \"time_zone\": \"${4:-$zone}\"}" "$url/register"
}
check_in() {  # from, to, time zone: a connection check that brings work hours
    status -d "{$me, \"hours_from\": \"$1\", \"hours_to\": \"$2\", \"time_zone\": \"$3\"}" "$url/check"
}
# Bodies with quotes are built outside "$(...)": macOS's sh keeps the backslashes in there.
# The refused ones would hold the call if they were taken.
with_zone="{$phone, \"hours_from\": \"08:00\", \"hours_to\": \"20:00\", \"time_zone\": \"$zone\"}"
no_zone="{$phone, \"hours_from\": \"14:00\", \"hours_to\": \"16:00\"}"
only_from="{$phone, \"hours_from\": \"14:00\", \"time_zone\": \"$zone\"}"
# A call that rings is then over, as the phone says, so the next one can ring (one at a time).
call_as() {
    out=$(body -d "{\"project\": \"demo\", \"call_id\": \"$1\"}" "$agent/place")
    [ "$out" != '{"ok":true}' ] || status -d "{\"call_id\": \"$1\", \"event\": \"ended\"}" "$url/events" > /dev/null
    printf '%s' "$out"
}
held_until() { echo "{\"held\":true,\"held_until\":\"${today}T$1:00$offset\",\"ok\":true}"; }
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
check "work hours in a time zone the hub doesn't know: 400" 400 "$(hours "$phone" 14:00 16:00 Mars/Olympus)"
check "a time that isn't HH:MM: 400" 400 "$(hours "$phone" 14:00 6:00)"
check "a time past 23:59: 400" 400 "$(hours "$phone" 14:00 24:00)"
check "work hours without a time zone: 400" 400 "$(status -d "$no_zone" "$url/register")"
check "only From: 400" 400 "$(status -d "$only_from" "$url/register")"
check "with those refused, the last hours still hold: it rings" "$rings" "$(call_as ring0004-000000000000)"
check "register with 14:00–16:00 again" 200 "$(hours "$phone" 14:00 16:00)"
pair2="{\"code\": \"$(code_of)\", \"token\": \"dddd4444eeee5555\", \"environment\": \"sandbox\", \"device\": \"Second phone\", \"hours_from\": \"13:30\", \"hours_to\": \"13:45\", \"time_zone\": \"$zone\"}"
k2=$(key_of "$(body -d "$pair2" "$url/pair")")
check "a second phone pairs, with 13:30–13:45" 64 "${#k2}"
second="\"token\": \"dddd4444eeee5555\", \"key\": \"$k2\", \"environment\": \"sandbox\", \"device\": \"Second phone\""
check "and registers with them again" 200 "$(hours "$second" 13:30 13:45)"
check "outside both, held until the earlier start" "$(held_until 13:30)" "$(call_as held0003-000000000000)"
check "the second phone again, without hours" 200 "$(status -d "{$second}" "$url/register")"
check "its hours are gone, and a phone without them is always inside: it rings" "$rings" "$(call_as ring0005-000000000000)"
check "the second phone with 13:30–13:45 again" 200 "$(hours "$second" 13:30 13:45)"
check "a connection check brings a new time zone" 200 "$(check_in 14:00 16:00 "$zone2")"
check "a check with hours that can't be read still gets a 200" 200 "$(check_in 14:00 16:00 Mars/Olympus)"
check "a check without hours keeps them: still held" "$(held_until 13:30)" "$(status -d "$me_only" "$url/check" > /dev/null; call_as held0004-000000000000)"
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
check "call logged, with its Mac" yes "$(yes_if grep -qF 'call 01234567: asked by stand-in for demo: Tests pass' "$home/log")"
check "call rings with its ID" yes "$(yes_if grep -qF 'call 01234567 → Test phone: no [apns.sandbox] key in the config' "$home/log")"
check "registration logged with work hours" yes "$(yes_if grep -qF "registered Test phone (sandbox), token …bbbb2222, work hours 14:00–16:00 $zone" "$home/log")"
check "a held call logged" yes "$(yes_if grep -qF "call held0001: held until ${today}T14:00:00$offset, every phone on hold" "$home/log")"
check "held calls ring nobody" no "$(yes_if grep -qE 'call held000[0-9] →' "$home/log")"
check "calls inside the hours ring" 5 "$(grep -cE 'call ring000[1-5] → Test phone: no \[apns.sandbox\] key' "$home/log")"
check "unreadable hours from a check are logged and left out" yes "$(yes_if grep -qF 'check from …bbbb2222: work hours that can'"'"'t be read, left out' "$home/log")"
devices=$(HOME="$home" "$bin" devices)
check "devices shows the hours, in the time zone the check brought, and the switch" yes "$(yes_if has "$devices" "…bbbb2222  Test phone .*  work hours 14:00–16:00 $zone2_re  on hold\$")"
check "devices shows the second phone's hours" yes "$(yes_if has "$devices" "…eeee5555  Second phone .*  work hours 13:30–13:45 $zone_re  on hold\$")"
out=$(HOME="$home" "$bin" ring --project smoke-test)
check "ring ignores work hours: it pushes to both phones" 2 "$(printf '%s\n' "$out" | grep -c 'no \[apns.sandbox\] key')"

# Waiting calls: held ones are kept in waiting.json and listed for the app, soonest first,
# until they ring, the app calls one now, or their agent stops asking about them (after
# RINGBACK_WAITING_STALE s here instead of 25 min). Both phones are outside their hours now.
rm -f "$state/waiting.json"
serve_waiting() {
    HOME="$home" RINGBACK_WAITING_STALE=4 "$bin" serve >> "$home/log" 2>&1 &
    server=$!
}
listed() { body -d "$me_only" "$url/waiting" | grep -oE 'wait[0-9]{4}' | tr '\n' ' ' | sed 's/ $//'; }
call_about() {  # call ID, subject; one that rings is then over
    out=$(body -d "{\"project\": \"demo\", \"subject\": \"$2\", \"call_id\": \"$1\"}" "$agent/place")
    [ "$out" != '{"ok":true}' ] || status -d "{\"call_id\": \"$1\", \"event\": \"ended\"}" "$url/events" > /dev/null
    printf '%s' "$out"
}
called_now='{"call_id": "wait0002-000000000000", "event": "called now", "at": "2026-10-30T09:00:00Z"}'
serve_waiting
check "waiting calls for a token that hasn't paired: 401" 401 "$(status -d '{"token": "ffff9999"}' "$url/waiting")"
check "waiting calls without a token: 401" 401 "$(status -d '{}' "$url/waiting")"
check "waiting calls without the key: 401" 401 "$(status -d "$no_key" "$url/waiting")"
check "waiting calls with a wrong key: 401" 401 "$(status -d "$wrong_key" "$url/waiting")"
check "no calls wait yet" yes "$(yes_if has_text "$(body -d "$me_only" "$url/waiting")" '"calls":[]')"
check "a held call waits" "$(held_until 13:30)" "$(call_about wait0001-000000000000 "Docs updated")"
check "the second phone with 13:15–13:20" 200 "$(hours "$second" 13:15 13:20)"
check "another, held until sooner" "$(held_until 13:15)" "$(call_about wait0002-000000000000 "Parser fixed")"
check "both are listed, soonest first" "wait0002 wait0001" "$(listed)"
waiting=$(body -d "$me_only" "$url/waiting")
check "with project, subject, voice URL and when it rings, and no question" yes "$(yes_if has_text "$waiting" \
    "\"call_id\":\"wait0002-000000000000\",\"held_until\":\"${today}T13:15:00$offset\",\"project\":\"demo\",\"question\":false,\"subject\":\"Parser fixed\",\"voice\":\"http:")"
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

# A held call with a question: the phone that gave a notification token gets one ordinary
# notification for it (without an APNs key, the log says so instead), however often the call
# is asked about, and after a restart. The list says whether a call has a question.
ask_held() {  # call ID, subject, question
    body -d "{\"project\": \"demo\", \"subject\": \"$2\", \"call_id\": \"$1\", \"question\": $3}" "$agent/place"
}
fields() {  # call ID: its question, as listed
    body -d "$me_only" "$url/waiting" | grep -oE "\"call_id\":\"$1[^}]*" \
        | grep -oE '"question":(true|false)'
}
told() {  # call ID: how many times the phone was told of its question (the push goes after the answer)
    sleep 0.5
    grep -c "call $1: question → Test phone: no \[apns.sandbox\] key in the config" "$home/log"
}
claude_app='{"call_id": "wait0004-000000000000", "event": "claude app", "at": "2026-10-30T09:00:00Z"}'
check "a held call with a question" "$(held_until 13:15)" "$(ask_held wait0004-000000000000 "Which branch?" true)"
check "the phone is told, once" 1 "$(told wait0004)"
check "a phone without a notification token isn't" yes "$(yes_if grep -qF 'call wait0004: question → Second phone: no notification token' "$home/log")"
check "listed with its question" '"question":true' "$(fields wait0004)"
check "asked about again" "$(held_until 13:15)" "$(ask_held wait0004-000000000000 "Which branch?" true)"
check "it isn't told again" 1 "$(told wait0004)"
check "waiting.json keeps when it was told" yes "$(yes_if grep -qE '"notified" ?: "20' "$state/waiting.json")"
ask_held wait0004-000000000000 "Which branch?" true > /dev/null  # fresh before the restart
kill $server
wait $server 2> /dev/null || true
serve_waiting
check "after a restart, asked about again, it isn't told again" 1 "$(ask_held wait0004-000000000000 "Which branch?" true > /dev/null; told wait0004)"
check "a held call without a question" "$(held_until 13:15)" "$(ask_held wait0005-000000000000 "Docs updated" false)"
check "isn't told" 0 "$(told wait0005)"
check "and is listed without one" '"question":false' "$(fields wait0005)"
check "a question joins it: the phone is told" 1 "$(ask_held wait0005-000000000000 "Docs updated" true > /dev/null; told wait0005)"
check "the phone reports it sent a question to the Claude app" 200 "$(status -d "$claude_app" "$url/events")"
check "that call no longer waits" "wait0005" "$(listed)"
sleep 1
kill $server
wait $server 2> /dev/null || true
check "a waiting call that rings is logged" yes "$(yes_if grep -qF 'call wait0001: no longer waiting, it rings' "$home/log")"
check "one called from the app is logged" yes "$(yes_if grep -qF 'call wait0002: no longer waiting, called from the app' "$home/log")"
check "a dropped one is logged" yes "$(yes_if grep -qE 'call wait0003: no longer waiting, nothing has asked about it since [0-9:]{8}' "$home/log")"
check "a held call's question is logged" yes "$(yes_if grep -qF "call wait0004: held until ${today}T13:15:00$offset, every phone on hold, with a question" "$home/log")"
check "one whose question went to the Claude app is logged" yes "$(yes_if grep -qF 'call wait0004: no longer waiting, its question went to the Claude app' "$home/log")"

# Sessions: each agent says which of its sessions listen, and the app lists them all, by
# project, each with the URL it calls them at (the hub's). An agent's list replaces the one
# before, and one not heard from for RINGBACK_CONTACTS_STALE s (here 5, instead of 90) is
# dropped. A held call keeps the session that placed it.
serve_contacts() {
    HOME="$home" RINGBACK_CONTACTS_STALE=5 "$bin" serve >> "$home/log" 2>&1 &
    server=$!
}
contact_ids() { body -d "$me_only" "$url/contacts" | grep -oE 'sess[0-9]{4}' | tr '\n' ' ' | sed 's/ $//'; }
two="{\"sessions\": [{\"session_id\": \"sess0001-aaaa\", \"project\": \"zeta\", \"since\": \"2026-10-30T09:00:00+01:00\"}, {\"session_id\": \"sess0002-aaaa\", \"project\": \"Alpha\", \"since\": \"2026-10-30T10:00:00+01:00\", \"subject\": \"Parser fixed\", \"briefed_at\": \"2026-10-30T10:30:00+01:00\"}]}"
one_b="{\"sessions\": [{\"session_id\": \"sess0003-bbbb\", \"project\": \"alpha\", \"since\": \"2026-10-30T08:00:00+01:00\"}]}"
one_a="{\"sessions\": [{\"session_id\": \"sess0002-aaaa\", \"project\": \"Alpha\", \"since\": \"2026-10-30T10:00:00+01:00\"}]}"
no_project="{\"sessions\": [{\"session_id\": \"sess0009-aaaa\"}]}"
held_with_session='{"project": "demo", "subject": "Docs", "call_id": "wait0006-000000000000", "session_id": "sess0004-cccc"}'
serve_contacts
check "contacts for a token that hasn't paired: 401" 401 "$(status -d '{"token": "ffff9999"}' "$url/contacts")"
check "contacts without a token: 401" 401 "$(status -d '{}' "$url/contacts")"
check "contacts without the key: 401" 401 "$(status -d "$no_key" "$url/contacts")"
check "no contacts yet" '{"contacts":[]}' "$(body -d "$me_only" "$url/contacts")"
check "a session without a project is refused" yes "$(status -d "$no_project" "$agent/sessions?agent=mac-a" > /dev/null; yes_if grep -qF 'agent mac-a: sessions refused: bad request' "$home/log")"
check "nothing is listed for it" '{"contacts":[]}' "$(body -d "$me_only" "$url/contacts")"
check "an agent says two sessions listen" 200 "$(status -d "$two" "$agent/sessions?agent=mac-a")"
check "another agent, one" 200 "$(status -d "$one_b" "$agent/sessions?agent=mac-b")"
check "all three are listed, by project, then since when" "sess0003 sess0002 sess0001" "$(contact_ids)"
listed=$(body -d "$me_only" "$url/contacts")
check "with project, latest subject and when, since when, and the hub's URL" yes "$(yes_if has_text "$listed" \
    '{"briefed_at":"2026-10-30T10:30:00+01:00","project":"Alpha","session_id":"sess0002-aaaa","since":"2026-10-30T10:00:00+01:00","subject":"Parser fixed","voice":"http:')"
check "the first agent again, now with one: it replaces its two" 200 "$(status -d "$one_a" "$agent/sessions?agent=mac-a")"
check "listed: that one and the other agent's" "sess0003 sess0002" "$(contact_ids)"
kill $server
wait $server 2> /dev/null || true
serve_contacts
check "after a restart, both still listed" "sess0003 sess0002" "$(contact_ids)"
sleep 3
check "the second agent says again" 200 "$(status -d "$one_b" "$agent/sessions?agent=mac-b")"
sleep 3
check "the first, not heard from in time, is dropped" "sess0003" "$(contact_ids)"
check "a held call names its session" "$(held_until 13:15)" "$(body -d "$held_with_session" "$agent/place")"
check "listed as waiting with it" yes "$(yes_if has_text "$(body -d "$me_only" "$url/waiting")" '"session_id":"sess0004-cccc"')"
kill $server
wait $server 2> /dev/null || true
check "an agent's sessions are logged" yes "$(yes_if grep -qF 'sessions listening on agent:mac-a: zeta (sess0001), Alpha (sess0002)' "$home/log")"
check "said again unchanged, they aren't logged again" 1 "$(grep -cF 'sessions listening on agent:mac-b: alpha (sess0003)' "$home/log")"
check "a dropped agent is logged" yes "$(yes_if grep -qE 'sessions listening on agent:mac-a: dropped, not heard from since [0-9:]{8}' "$home/log")"

# The hold switch: work hours set it at their edges, on hold when they end and taking calls
# when they start, and the phone sets it in between (/hold). A call is held while every phone
# is on hold, until the first comes off hold by its hours, or with no time when none will.
# It's between 12:00 and 13:00 in $zone. set_at moves when a switch was set back in time, to
# before an edge.
first=aaaa1111bbbb2222 other=dddd4444eeee5555
key_for() { if [ "$1" = "$first" ]; then echo "$k1"; else echo "$k2"; fi; }
hold_of() { body -d "{\"token\": \"$1\", \"key\": \"$(key_for "$1")\"}" "$url/hold"; }
set_hold() { body -d "{\"token\": \"$1\", \"key\": \"$(key_for "$1")\", \"hold\": $2}" "$url/hold"; }
set_at() {  # token, ISO 8601
    python3 -c 'import json, sys; p, t, at = sys.argv[1:]; d = json.load(open(p)); d[t]["hold_set"] = at; json.dump(d, open(p, "w"))' \
        "$state/devices.json" "$1" "$2"
}
hold_json() { echo "{\"hold\":$1,\"until\":\"$2:00$offset\"}"; }  # true|false, date and time
tomorrow=$(TZ=$zone date -v+1d +%Y-%m-%d 2> /dev/null || TZ=$zone date -d tomorrow +%Y-%m-%d)
HOME="$home" "$bin" serve >> "$home/log" 2>&1 &
server=$!
check "the switch without a token: 401" 401 "$(status -d '{}' "$url/hold")"
check "for a token that hasn't paired: 401" 401 "$(status -d '{"token": "ffff9999"}' "$url/hold")"
check "without the key: 401" 401 "$(status -d "$no_key" "$url/hold")"
check "the first phone with 14:00–16:00" 200 "$(hours "$phone" 14:00 16:00)"
check "the second with 13:15–13:20" 200 "$(hours "$second" 13:15 13:20)"
check "outside its hours, a phone is on hold until they start" "$(hold_json true "${today}T14:00")" "$(hold_of $first)"
check "both on hold: a call is held until the earlier start" "$(held_until 13:15)" "$(call_as hold0001-000000000000)"
check "the first takes calls, set on the phone: until its hours end" "$(hold_json false "${today}T16:00")" "$(set_hold $first false)"
check "outside its hours, a call rings" "$rings" "$(call_as hold0002-000000000000)"
check "on hold again" "$(hold_json true "${today}T14:00")" "$(set_hold $first true)"
check "and calls are held again" "$(held_until 13:15)" "$(call_as hold0003-000000000000)"
check "with 08:00–20:00, inside them, it stays on hold" 200 "$(hours "$phone" 08:00 20:00)"
check "until they next start, tomorrow" "$(hold_json true "${tomorrow}T08:00")" "$(hold_of $first)"
check "paused before they started today, they let calls through" "$(hold_json false "${today}T20:00")" \
    "$(set_at $first "$(days_ago 1)"; hold_of $first)"
check "so a call rings" "$rings" "$(call_as hold0004-000000000000)"
check "with 14:00–16:00 again" 200 "$(hours "$phone" 14:00 16:00)"
check "taking calls since before they last ended: on hold" "$(hold_json true "${today}T14:00")" \
    "$(set_hold $first false > /dev/null; set_at $first "$(days_ago 2)"; hold_of $first)"
check "both phones without hours" 200 "$(status -d "{$phone}" "$url/register"; status -d "{$second}" "$url/register" > /dev/null)"
check "without hours, a phone takes calls" '{"hold":false}' "$(hold_of $first)"
check "on hold, it has no time to come off it" '{"hold":true}' "$(set_hold $first true)"
check "the other takes calls: a call rings" "$rings" "$(call_as hold0005-000000000000)"
check "both on hold: held until you take calls" '{"held":true,"ok":true}' "$(set_hold $other true > /dev/null; call_as hold0006-000000000000)"
waiting=$(body -d "$me_only" "$url/waiting")
check "listed without a time" yes "$(yes_if has_text "$waiting" '"call_id":"hold0006-000000000000","project":"demo","question":false')"
check "with the phone's switch" yes "$(yes_if has_text "$waiting" '"hold":{"hold":true}')"
check "taking calls again: it rings" "$rings" "$(set_hold $first false > /dev/null; call_as hold0006-000000000000)"

# One call at a time: a second waits in turn; the first missed, it rings again 5 min after it
# first rang, and nothing else rings meanwhile; called now, it no longer waits, and the next rings.
place_raw() { body -d "{\"project\": \"demo\", \"call_id\": \"$1\"}" "$agent/place"; }
check "line: the first call rings" "$rings" "$(place_raw line0001-000000000000)"
check "line: a second waits in turn" '{"held":true,"ok":true,"reason":"in turn"}' "$(place_raw line0002-000000000000)"
status -d '{"call_id": "line0001-000000000000", "event": "missed"}' "$url/events" > /dev/null
check "line: the first missed, the second still waits" '{"held":true,"ok":true,"reason":"in turn"}' "$(place_raw line0002-000000000000)"
check "line: the missed call waits to ring again, at a time" yes "$(yes_if has "$(place_raw line0001-000000000000)" '^\{"held":true,"held_until":"[^"]+","ok":true,"reason":"missed"\}$')"
check "line: listed as waiting, missed" yes "$(yes_if has_text "$(body -d "$me_only" "$url/waiting")" '"call_id":"line0001-000000000000","held_until":')"
status -d '{"call_id": "line0001-000000000000", "event": "called now"}' "$url/events" > /dev/null
check "line: called now, the next rings" "$rings" "$(place_raw line0002-000000000000)"
# Cancelled on the phone: a waiting call leaves the list at once, and its agent hears so when it
# asks about it again (cancel.go).
check "cancel: a third waits in turn" '{"held":true,"ok":true,"reason":"in turn"}' "$(place_raw line0003-000000000000)"
check "cancel: from the phone" 200 "$(status -d '{"call_id": "line0003-000000000000"}' "$url/cancel")"
check "cancel: no longer listed" no "$(yes_if has_text "$(body -d "$me_only" "$url/waiting")" 'line0003')"
check "cancel: again, it doesn't wait" 403 "$(status -d '{"call_id": "line0003-000000000000"}' "$url/cancel")"
check "cancel: a call that rings can't be" 403 "$(status -d '{"call_id": "line0002-000000000000"}' "$url/cancel")"
check "cancel: asked about again, its agent hears it was cancelled" '{"by":"phone","cancelled":true,"ok":true}' "$(place_raw line0003-000000000000)"
status -d '{"call_id": "line0002-000000000000", "event": "ended"}' "$url/events" > /dev/null
sleep 1
kill $server
wait $server 2> /dev/null || true
check "devices shows the switch and when it was set" yes "$(yes_if has "$(HOME="$home" "$bin" devices)" '…bbbb2222  Test phone .*  taking calls, set 20[^ ]+$')"
check "setting it is logged" yes "$(yes_if grep -qF '…bbbb2222 Test phone: on hold, set on the phone' "$home/log")"
check "a call held until you take calls is logged" yes "$(yes_if grep -qF 'call hold0006: held until you take calls, every phone on hold' "$home/log")"
check "held calls ring nobody" no "$(yes_if grep -qE 'call hold000[13] →' "$home/log")"
check "the others ring, the last once you take calls" 4 "$(grep -cE 'call hold000[2456] → Test phone' "$home/log")"

# Over TLS ([tls]), with a certificate for 127.0.0.1 that's its own CA, made here as an ACME
# client would write it; then renewed while the hub serves.
tls="$home/.config/ringback/tls"
mkdir -p "$tls"
make_cert() { (cd "$tls" && go run "$(go env GOROOT)/src/crypto/tls/generate_cert.go" --host 127.0.0.1 --ca \
    --ecdsa-curve P256 --duration "$1" 2> /dev/null && mv cert.pem fullchain.pem); }
make_cert 2160h
tls_url=https://127.0.0.1:18790
printf 'listen = "127.0.0.1:18790"\n[tls]\ncert = "%s"\nkey = "%s"\n[tokens]\nagent = "smoke-agent-token-0001"\nspeech = "smoke-speech-token-001"\n' \
    "$tls/fullchain.pem" "$tls/key.pem" > "$home/.config/ringback/hub.toml"
HOME="$home" "$bin" serve > "$home/tls.log" 2>&1 &
server=$!
cp "$tls/fullchain.pem" "$tls/first.pem"
check "TLS: /ping over HTTPS" 200 "$(status --cacert "$tls/first.pem" "$tls_url/ping")"
check "TLS: a certificate it doesn't trust fails" 000 "$(curl -s -o /dev/null -w '%{http_code}' "$tls_url/ping")"
check "TLS: plain HTTP on its port is refused" 400 "$(status "http://127.0.0.1:18790/ping")"
check "TLS: the agent link without a token gets 401" 401 "$(status --cacert "$tls/first.pem" "$tls_url/agent")"
# Pairing a Mac, over TLS: the command `pair mac` prints carries a code the running hub takes
# once, for a key the agent link opens with, as the token's does.
upgrade() {  # the link's status with a key or token: 101 is opened
    curl -s -o /dev/null -w '%{http_code}' --max-time 2 --cacert "$tls/first.pem" -H 'Connection: Upgrade' \
        -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
        -H "Authorization: Bearer $1" "$tls_url/agent" || true
}
pairing=$(HOME="$home" "$bin" pair mac)
check "pair mac: the command to run on the Mac" yes "$(yes_if has "$pairing" "^~/\.local/bin/ringback-agent pair $tls_url [A-Za-z0-9_-]{43}$")"
code=$(printf '%s\n' "$pairing" | tail -1 | awk '{print $4}')
as_mac=$(printf '{"code": "%s", "kind": "agent", "name": "smoke-mac"}' "$code")
paired=$(body --cacert "$tls/first.pem" -d "$as_mac" "$tls_url/pair-link")
key=$(printf '%s' "$paired" | sed -n 's/.*"key":"\([0-9a-f]*\)".*/\1/p')
check "pair-link: the code makes the Mac a key" 64 "${#key}"
check "pair-link: once" 403 "$(status --cacert "$tls/first.pem" -d "$as_mac" "$tls_url/pair-link")"
node_code=$(printf '{"code": "%s", "kind": "agent", "name": "x"}' "$(HOME="$home" "$bin" pair node | tail -1 | awk '{print $4}')")
check "pair-link: a node's code doesn't pair a Mac" 403 "$(status --cacert "$tls/first.pem" -d "$node_code" "$tls_url/pair-link")"
check "the agent link opens with its key" 101 "$(upgrade "$key")"
check "and with the token" 101 "$(upgrade smoke-agent-token-0001)"
check "not with another key" 401 "$(upgrade 0000000000000000000000000000000000000000000000000000000000000000)"
check "the keys are kept as hashes" no "$(yes_if grep -qF "$key" "$home/.local/state/ringback/parts.json")"
make_cert 24h
check "TLS: renewed, the new certificate is served" 200 "$(status --cacert "$tls/fullchain.pem" "$tls_url/ping")"
check "TLS: and the first one no longer" 000 "$(curl -s -o /dev/null -w '%{http_code}' --cacert "$tls/first.pem" "$tls_url/ping")"
kill $server
wait $server 2> /dev/null || true
check "TLS: it says it serves HTTPS" yes "$(yes_if grep -qF 'hub listening on 127.0.0.1:18790, HTTPS and WSS' "$home/tls.log")"
check "TLS: each certificate is logged" 2 "$(grep -c 'TLS: certificate for 127.0.0.1, valid until' "$home/tls.log")"
cat "$home/tls.log" >> "$home/log"

if [ "${1:-}" = --apns ]; then
    cp ~/.config/ringback/hub.toml "$home/.config/ringback/hub.toml"
    cp ~/.config/ringback/AuthKey_*.p8 "$home/.config/ringback/"
    printf '{"%s": {"environment": "sandbox", "device": "made-up phone", "key": "made-up"}}' \
        0000000000000000000000000000000000000000000000000000000000000000 > "$state/devices.json"
    out=$(HOME="$home" "$bin" ring --project smoke-test)
    echo "      $out" | head -1
    check "APNs accepts the signature" yes "$(yes_if has "$out" 'APNs 400 BadDeviceToken')"
    check "the bad token is forgotten" 0 "$(HOME="$home" "$bin" devices | wc -l | tr -d ' ')"
fi

if [ $fails -gt 0 ]; then
    echo "--- hub log"
    cat "$home/log"
    echo "--- stand-ins"
    cat "$home/standin.log"
    exit 1
fi
