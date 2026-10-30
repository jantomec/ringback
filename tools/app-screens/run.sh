#!/bin/sh
# Screenshots the app in a simulator, with sample calls, in light and dark. See README.md.
#
#   tools/app-screens/run.sh [SIMULATOR] [OUT_DIR]     e.g. "iPhone 17 Pro" (the default)
set -eu
cd "$(dirname "$0")"
simulator=${1:-iPhone 17 Pro}
out=${2:-$(mktemp -d)}
mkdir -p "$out"
device=$(xcrun simctl list devices available | grep -F "    $simulator (" | head -1 | grep -oE '[0-9A-F-]{36}')
[ -n "$device" ] || { echo "no simulator named $simulator"; exit 1; }
xcodegen generate --spec project.yml --project "$out" --quiet
xcrun simctl boot "$device" 2> /dev/null || true
# The app's package, from its own build if there is one, so it isn't downloaded again.
packages=../../app/build/SourcePackages
[ -d "$packages" ] && reuse="-clonedSourcePackagesDirPath $(cd "$packages" && pwd)" || reuse=
for look in light dark; do
    xcrun simctl ui "$device" appearance $look
    # shellcheck disable=SC2086
    TEST_RUNNER_APP_SCREENS_OUT="$out" TEST_RUNNER_APP_SCREENS_LOOK=$look xcodebuild test \
        -project "$out/AppScreens.xcodeproj" -scheme AppScreens -destination "id=$device" \
        -derivedDataPath "$out/build" $reuse > "$out/xcodebuild-$look.log" 2>&1 \
        || { tail -30 "$out/xcodebuild-$look.log"; exit 1; }
    grep -h 'APP-SCREENS' "$out/xcodebuild-$look.log" | sed 's/^.*APP-SCREENS /    /' | awk '!seen[$0]++'
done
xcrun simctl ui "$device" appearance light
echo "screenshots and logs: $out"
