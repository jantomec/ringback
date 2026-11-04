#!/bin/sh
# Builds the hub. On macOS, one universal binary (Apple silicon and Intel) that needs nothing
# but macOS: build/ringback-hub-universal. On Linux, as on a server, a binary for that machine
# that needs nothing but its C library: build/ringback-hub. Either way libopus is built from its
# release, checked against a pinned SHA-256, and linked in statically. Its first run builds
# libopus into build/opus (about a minute); later runs reuse it. It needs Go, a C compiler,
# CMake, pkg-config and curl.
#
#     ./build.sh
set -eu
cd "$(dirname "$0")"

version=1.6.1
sha256=6ffcb593207be92584df15b32466ed64bbec99109f007c82205f0194572411a1
min=13.0
opus="$PWD/build/opus"
os=$(uname -s)

if [ ! -f "$opus/lib/libopus.a" ]; then
    mkdir -p build
    tarball="build/opus-$version.tar.gz"
    curl -fsSL -o "$tarball" "https://downloads.xiph.org/releases/opus/opus-$version.tar.gz"
    if [ "$os" = Darwin ]; then
        echo "$sha256  $tarball" | shasum -a 256 -c -
        set -- -DCMAKE_OSX_ARCHITECTURES="arm64;x86_64" -DCMAKE_OSX_DEPLOYMENT_TARGET=$min
    else
        echo "$sha256  $tarball" | sha256sum -c -
        set --
    fi
    rm -rf "build/opus-$version"
    tar -xzf "$tarball" -C build
    cmake -S "build/opus-$version" -B "build/opus-$version/out" -DCMAKE_BUILD_TYPE=Release "$@" \
        -DBUILD_SHARED_LIBS=OFF -DOPUS_BUILD_PROGRAMS=OFF -DOPUS_BUILD_TESTING=OFF \
        -DCMAKE_INSTALL_PREFIX="$opus" -DCMAKE_INSTALL_LIBDIR=lib > build/opus.log
    cmake --build "build/opus-$version/out" --target install -j 8 >> build/opus.log
fi

export PKG_CONFIG_PATH="$opus/lib/pkgconfig" CGO_ENABLED=1
if [ "$os" != Darwin ]; then
    # The static libopus needs the maths library, which pkg-config names only for static links.
    CGO_LDFLAGS="-lm" go build -trimpath -o build/ringback-hub .
    echo "built build/ringback-hub"
    exit
fi
export CGO_CFLAGS="-O2 -mmacosx-version-min=$min" CGO_LDFLAGS="-mmacosx-version-min=$min"
for arch in arm64 amd64; do
    clang_arch=$arch
    [ $arch = amd64 ] && clang_arch=x86_64
    GOARCH=$arch CC="clang -arch $clang_arch" go build -trimpath -o "build/ringback-hub-$arch" .
done
lipo -create -output build/ringback-hub-universal build/ringback-hub-arm64 build/ringback-hub-amd64
rm build/ringback-hub-arm64 build/ringback-hub-amd64
lipo -archs build/ringback-hub-universal
