#!/usr/bin/env bash
# Packages the static FFmpeg and subtitle libraries built for ios and
# iossimulator into xcframeworks that Xcode can link, under build/xcframeworks.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
B="$HERE/build"
OUT="$HERE/../build/xcframeworks"
rm -rf "$OUT"; mkdir -p "$OUT"

for lib in avcodec avdevice avfilter avformat avutil swresample swscale; do
  xcodebuild -create-xcframework \
    -library "$B/ios/lib/lib$lib.a" \
    -library "$B/iossimulator/lib/lib$lib.a" \
    -output "$OUT/lib$lib.xcframework" >/dev/null
  echo "lib$lib.xcframework"
done
for lib in ass freetype fribidi harfbuzz; do
  xcodebuild -create-xcframework \
    -library "$B/deps-ios/lib/lib$lib.a" \
    -library "$B/deps-iossimulator/lib/lib$lib.a" \
    -output "$OUT/lib$lib.xcframework" >/dev/null
  echo "lib$lib.xcframework"
done
echo "xcframeworks in $OUT"
