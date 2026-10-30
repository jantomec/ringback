#!/bin/sh
# Runs the swipe check in a simulator: what `.disabled` does to a SwiftUI swipe action. See
# README.md.
#
#   tools/swipe-check/run.sh [SIMULATOR] [OUT_DIR]     e.g. "iPhone 17,OS=26.5" (the default)
set -eu
cd "$(dirname "$0")"
simulator=${1:-iPhone 17,OS=26.5}
out=${2:-$(mktemp -d)}
mkdir -p "$out"
xcodegen generate --spec project.yml --project "$out" --quiet
TEST_RUNNER_SWIPE_CHECK_OUT="$out" xcodebuild test -project "$out/SwipeCheck.xcodeproj" -scheme SwipeCheck \
    -destination "platform=iOS Simulator,name=$simulator" -derivedDataPath "$out/build" > "$out/xcodebuild.log" 2>&1 \
    || { tail -30 "$out/xcodebuild.log"; exit 1; }
grep -h 'SWIPE-CHECK' "$out/xcodebuild.log" | sed 's/^.*SWIPE-CHECK /    /' | sort -u
echo "screenshots and the log: $out"
