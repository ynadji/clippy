#!/usr/bin/env bash
# Cross-compiles the subtitle rendering stack (fribidi, freetype, harfbuzz,
# libass) as static libraries for iOS. All permissively licensed or LGPL.
#
#   third_party/build-deps.sh ios            # arm64 device
#   third_party/build-deps.sh iossimulator   # arm64 simulator
#
# Outputs to third_party/build/deps-<target>.
set -euo pipefail

TARGET="${1:-ios}"
HERE="$(cd "$(dirname "$0")" && pwd)"
SRC="$HERE/src"
DEPS="$HERE/build/deps-$TARGET"
JOBS="$(sysctl -n hw.ncpu 2>/dev/null || echo 4)"
MIN_IOS=17.0

case "$TARGET" in
  ios)          SDK=iphoneos;        MINVER="-miphoneos-version-min=$MIN_IOS";       CMAKE_SYSROOT=iphoneos ;;
  iossimulator) SDK=iphonesimulator; MINVER="-mios-simulator-version-min=$MIN_IOS";  CMAKE_SYSROOT=iphonesimulator ;;
  *) echo "unknown target $TARGET"; exit 1 ;;
esac

SYSROOT="$(xcrun --sdk $SDK --show-sdk-path)"
export CC="$(xcrun --sdk $SDK -f clang)"
export CXX="$(xcrun --sdk $SDK -f clang++)"
export AR="$(xcrun --sdk $SDK -f ar)"
export RANLIB="$(xcrun --sdk $SDK -f ranlib)"
export CFLAGS="-arch arm64 -isysroot $SYSROOT $MINVER -O2 -fPIC"
export CXXFLAGS="$CFLAGS"
export LDFLAGS="-arch arm64 -isysroot $SYSROOT $MINVER"
export PKG_CONFIG_PATH="$DEPS/lib/pkgconfig"
export PKG_CONFIG_LIBDIR="$DEPS/lib/pkgconfig"
HOST=aarch64-apple-darwin

mkdir -p "$DEPS"

echo "== fribidi ($TARGET)"
( cd "$SRC/fribidi-1.0.16" && make distclean >/dev/null 2>&1 || true
  cd "$SRC/fribidi-1.0.16" && ./configure --host=$HOST --prefix="$DEPS" --enable-static --disable-shared --disable-docs >/dev/null
  make -j"$JOBS" >/dev/null && make install >/dev/null )

echo "== freetype ($TARGET)"
( cd "$SRC/freetype-2.13.3" && make distclean >/dev/null 2>&1 || true
  cd "$SRC/freetype-2.13.3" && ./configure --host=$HOST --prefix="$DEPS" --enable-static --disable-shared \
    --with-harfbuzz=no --with-png=no --with-bzip2=no --with-brotli=no --with-zlib=yes >/dev/null
  make -j"$JOBS" >/dev/null && make install >/dev/null )

echo "== harfbuzz ($TARGET)"
( rm -rf "$SRC/harfbuzz-11.2.1/build-$TARGET"
  cmake -S "$SRC/harfbuzz-11.2.1" -B "$SRC/harfbuzz-11.2.1/build-$TARGET" -G Ninja \
    -DCMAKE_SYSTEM_NAME=iOS -DCMAKE_OSX_SYSROOT=$CMAKE_SYSROOT -DCMAKE_OSX_ARCHITECTURES=arm64 \
    -DCMAKE_OSX_DEPLOYMENT_TARGET=$MIN_IOS -DCMAKE_INSTALL_PREFIX="$DEPS" -DCMAKE_PREFIX_PATH="$DEPS" \
    -DCMAKE_FIND_ROOT_PATH="$DEPS;$SYSROOT" -DCMAKE_FIND_ROOT_PATH_MODE_LIBRARY=BOTH \
    -DCMAKE_FIND_ROOT_PATH_MODE_INCLUDE=BOTH -DCMAKE_FIND_ROOT_PATH_MODE_PACKAGE=BOTH \
    -DFREETYPE_INCLUDE_DIRS="$DEPS/include/freetype2" -DFREETYPE_LIBRARY="$DEPS/lib/libfreetype.a" \
    -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF \
    -DHB_HAVE_FREETYPE=ON -DHB_HAVE_CORETEXT=ON -DHB_HAVE_GLIB=OFF -DHB_HAVE_ICU=OFF -DHB_BUILD_SUBSET=OFF \
    -DHB_HAVE_INTROSPECTION=OFF -DHB_BUILD_UTILS=OFF -DHB_BUILD_TESTS=OFF >/dev/null
  cmake --build "$SRC/harfbuzz-11.2.1/build-$TARGET" -j"$JOBS" >/dev/null
  cmake --install "$SRC/harfbuzz-11.2.1/build-$TARGET" >/dev/null )

echo "== libass ($TARGET)"
( cd "$SRC/libass-0.17.4" && make distclean >/dev/null 2>&1 || true
  cd "$SRC/libass-0.17.4" && ./configure --host=$HOST --prefix="$DEPS" --enable-static --disable-shared \
    --disable-fontconfig --enable-coretext --disable-require-system-font-provider \
    --disable-libunibreak --disable-asm >/dev/null
  make -j"$JOBS" >/dev/null && make install >/dev/null )

echo "deps installed to $DEPS:"
ls "$DEPS/lib"/*.a
