#!/usr/bin/env bash
# Builds a minimal, LGPL-only FFmpeg with VideoToolbox/AudioToolbox codecs
# and libass for subtitle burn-in. No --enable-gpl, no --enable-nonfree, so
# the result can be linked into a closed-source app (see docs/LICENSING.md).
#
#   third_party/build-ffmpeg.sh macos          # host build, used by the CLI and tests
#   third_party/build-ffmpeg.sh ios            # arm64 device
#   third_party/build-ffmpeg.sh iossimulator   # arm64 simulator
#
# Outputs to third_party/build/<target>/{lib,include}. Network I/O is done by
# Go through a custom AVIO callback, so no HTTP/TLS is compiled in.
set -euo pipefail

TARGET="${1:-macos}"
HERE="$(cd "$(dirname "$0")" && pwd)"
SRC="$HERE/src/ffmpeg-9.0.2"
OUT="$HERE/build/$TARGET"
JOBS="$(sysctl -n hw.ncpu 2>/dev/null || echo 4)"

[ -d "$SRC" ] || { echo "missing $SRC (download ffmpeg-9.0.2.tar.xz into third_party/src and unpack)"; exit 1; }

COMMON=(
  --prefix="$OUT"
  --disable-programs --disable-doc --disable-debug --disable-network
  --disable-autodetect
  --disable-everything
  --enable-pic
  # containers
  --enable-demuxer=matroska,mov,mpegts,avi,ass,srt,webvtt,image2,mp3,aac,flac,ogg,wav
  --enable-muxer=mp4,mov,ass,srt,webvtt,image2,mjpeg,null
  # decoders: video via hardware where possible, audio and text subs in software
  --enable-decoder=h264,hevc,vp9,av1,mpeg4,mpeg2video,mjpeg,png
  --enable-decoder=aac,aac_latm,ac3,eac3,dca,truehd,mlp,mp3,mp2,opus,flac,vorbis,alac,pcm_s16le,pcm_s24le,pcm_s16be,pcm_s24be,pcm_f32le
  --enable-decoder=ass,ssa,srt,subrip,webvtt,mov_text,text
  --enable-encoder=mjpeg,png,ass,srt,webvtt,mov_text,aac,pcm_s16le
  --enable-parser=h264,hevc,aac,aac_latm,ac3,mpegaudio,opus,vp9,av1,flac,vorbis,mjpeg,png,dca,mlp
  --enable-bsf=h264_mp4toannexb,hevc_mp4toannexb,aac_adtstoasc,extract_extradata,null
  --enable-protocol=file,pipe
  --enable-filter=scale,format,subtitles,ass,aformat,aresample,anull,null,fps,hwdownload,hwupload,hwmap,setpts,asetpts,trim,atrim,buffer,buffersink,abuffer,abuffersink,pad,crop,transpose,volume,amix,anullsrc,color,copy,settb,asettb
  --enable-swscale --enable-swresample --enable-avfilter
  --enable-zlib --enable-iconv
  --enable-libass
)

APPLE=(
  --enable-videotoolbox --enable-audiotoolbox
  --enable-hwaccel=h264_videotoolbox,hevc_videotoolbox,vp9_videotoolbox,av1_videotoolbox
  --enable-encoder=h264_videotoolbox,hevc_videotoolbox,aac_at
  --enable-decoder=aac_at,ac3_at,eac3_at,mp3_at,alac_at
)

case "$TARGET" in
  macos)
    export PKG_CONFIG_PATH="${PKG_CONFIG_PATH:-}:/opt/homebrew/lib/pkgconfig:/opt/homebrew/opt/libass/lib/pkgconfig"
    EXTRA=(--enable-shared --disable-static --install-name-dir="$OUT/lib" --extra-libs=-liconv)
    ;;
  ios|iossimulator)
    if [ "$TARGET" = ios ]; then
      SDK=iphoneos; MINVER="-miphoneos-version-min=17.0"; PLAT=ios-arm64
    else
      SDK=iphonesimulator; MINVER="-mios-simulator-version-min=17.0"; PLAT=ios-arm64-simulator
    fi
    SYSROOT="$(xcrun --sdk $SDK --show-sdk-path)"
    DEPS="$HERE/build/deps-$TARGET"
    export PKG_CONFIG_PATH="$DEPS/lib/pkgconfig"
    export PKG_CONFIG_LIBDIR="$DEPS/lib/pkgconfig"
    EXTRA=(
      --enable-cross-compile --target-os=darwin --arch=arm64
      --cc="$(xcrun --sdk $SDK -f clang)" --as="$(xcrun --sdk $SDK -f clang)"
      --sysroot="$SYSROOT"
      --extra-cflags="-arch arm64 $MINVER -fembed-bitcode-marker"
      --extra-ldflags="-arch arm64 $MINVER" --extra-libs=-liconv
      --enable-static --disable-shared
      --disable-asm
      --pkg-config="$(command -v pkg-config)"
    )
    [ -d "$DEPS" ] || { echo "missing $DEPS: run third_party/build-deps.sh $TARGET first"; exit 1; }
    ;;
  *) echo "unknown target $TARGET"; exit 1;;
esac

cd "$SRC"
make distclean >/dev/null 2>&1 || true
./configure "${COMMON[@]}" "${APPLE[@]}" "${EXTRA[@]}"
if grep -q "^CONFIG_GPL=yes" ffbuild/config.mak; then echo "ERROR: GPL got enabled"; exit 1; fi
if grep -q "^CONFIG_NONFREE=yes" ffbuild/config.mak; then echo "ERROR: nonfree got enabled"; exit 1; fi
echo "license check: LGPL only"
make -j"$JOBS"
make install
echo "installed to $OUT"
