#!/usr/bin/env bash
# Packages the LGPL libraries built for ios and iossimulator (FFmpeg and
# FriBidi) as dynamic frameworks inside xcframeworks, under build/xcframeworks.
# The app embeds and signs them, so they stay separate, replaceable binaries
# rather than being linked into the app's executable. See docs/LICENSING.md.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
B="$HERE/build"
OUT="$HERE/../build/xcframeworks"
STAGE="$B/frameworks"
MIN_IOS=17.0
FFMPEG_VERSION=9.0.2
FRIBIDI_VERSION=1.0.16
rm -rf "$OUT" "$STAGE"; mkdir -p "$OUT"

# frameworkize <target> <name> <dylib> <bundle id> <version>
frameworkize() {
  local target="$1" name="$2" src="$3" bundle="$4" version="$5"
  local platform=iPhoneOS; [ "$target" = iossimulator ] && platform=iPhoneSimulator
  local fw="$STAGE/$target/$name.framework"
  mkdir -p "$fw"
  cp -L "$src" "$fw/$name"
  chmod u+w "$fw/$name"
  install_name_tool -id "@rpath/$name.framework/$name" "$fw/$name" 2>/dev/null
  # Point references to sibling libraries at their frameworks.
  otool -L "$fw/$name" | awk 'NR>1 {print $1}' | grep -E '^@rpath/lib' | while read -r dep; do
    local base depname
    base="$(basename "$dep")"; depname="${base%%.*}"
    if [ "$dep" != "@rpath/$depname.framework/$depname" ]; then
      install_name_tool -change "$dep" "@rpath/$depname.framework/$depname" "$fw/$name" 2>/dev/null
    fi
  done
  cat > "$fw/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key><string>en</string>
	<key>CFBundleExecutable</key><string>$name</string>
	<key>CFBundleIdentifier</key><string>$bundle</string>
	<key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
	<key>CFBundleName</key><string>$name</string>
	<key>CFBundlePackageType</key><string>FMWK</string>
	<key>CFBundleShortVersionString</key><string>$version</string>
	<key>CFBundleVersion</key><string>1</string>
	<key>MinimumOSVersion</key><string>$MIN_IOS</string>
	<key>CFBundleSupportedPlatforms</key><array><string>$platform</string></array>
</dict>
</plist>
PLIST
}

package() { # <name> <bundle id> <version> <ios dylib> <simulator dylib>
  frameworkize ios "$1" "$4" "$2" "$3"
  frameworkize iossimulator "$1" "$5" "$2" "$3"
  xcodebuild -create-xcframework \
    -framework "$STAGE/ios/$1.framework" \
    -framework "$STAGE/iossimulator/$1.framework" \
    -output "$OUT/$1.xcframework" >/dev/null
  echo "$1.xcframework: $(otool -L "$STAGE/ios/$1.framework/$1" | awk 'NR>1 {print $1}' | grep -c '^@rpath/' | tr -d ' ') framework dependencies"
}

for lib in avutil swresample swscale avcodec avformat avfilter avdevice; do
  package "lib$lib" "org.ffmpeg.lib$lib" "$FFMPEG_VERSION" "$B/ios/lib/lib$lib.dylib" "$B/iossimulator/lib/lib$lib.dylib"
done
package libfribidi org.fribidi.libfribidi "$FRIBIDI_VERSION" "$B/deps-ios/lib/libfribidi.dylib" "$B/deps-iossimulator/lib/libfribidi.dylib"

# Nothing may still point at a build-time path or an unpackaged library.
# (Dependency lines are the tab-indented ones; the others name the file.)
if otool -L "$STAGE"/*/*.framework/lib* | grep "^	" | awk '{print $1}' \
    | grep -vE '^(@rpath/lib[a-z]+\.framework/lib[a-z]+|/usr/lib/|/System/Library/)'; then
  echo "ERROR: a framework still references a non-framework path"; exit 1
fi
echo "xcframeworks in $OUT"
