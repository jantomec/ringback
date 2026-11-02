#!/bin/sh
# Renders the app icon's SVGs to the 1024-point PNGs in the app's asset catalog: default, dark
# and tinted (grayscale, which iOS colours). Needs rsvg-convert (brew install librsvg).
#
#   app/Icon/make.sh
set -eu
cd "$(dirname "$0")"
out=../Ringback/Assets.xcassets/AppIcon.appiconset
for name in icon icon-dark icon-tinted; do
    rsvg-convert -w 1024 -h 1024 -b black "$name.svg" -o "$out/$name.png"
done
