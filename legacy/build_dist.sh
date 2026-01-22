#!/bin/bash
set -e

rm -rf ./dist/*

# Detect host platform
HOST_OS="$(uname -s)"
HOST_ARCH="$(uname -m)"

echo "Detected host: ${HOST_OS} / ${HOST_ARCH}"

# Determine Linux cross-compiler based on host.
if [[ "$HOST_OS" == "Darwin" ]]; then
    if command -v x86_64-linux-musl-gcc >/dev/null; then
        CC_LINUX="x86_64-linux-musl-gcc"
    else
        echo "Skipping Linux build: x86_64-linux-musl-gcc not found"
        CC_LINUX=""
    fi
else
    if command -v musl-gcc >/dev/null; then
        CC_LINUX="musl-gcc"
    else
        echo "Skipping Linux build: musl-gcc not found"
        CC_LINUX=""
    fi
fi

# Determine Windows cross-compiler
if command -v x86_64-w64-mingw32-gcc >/dev/null; then
    CC_WINDOWS="x86_64-w64-mingw32-gcc"
else
    echo "Skipping Windows build: x86_64-w64-mingw32-gcc not found"
    CC_WINDOWS=""
fi

# For macOS native builds, we'll use clang
if [[ "$HOST_OS" == "Darwin" ]]; then
    CC_OSX="clang"
fi

# Function to compile for a specific OS and Arch
compile() {
    GOOS=$1
    GOARCH=$2
    FOLDER=$3
    EXTENSION=$4
    CC_COMPILER=$5
    CGO_ENABLED_FLAG=$6
    LDFLAGS=$7

    BINARY_NAME="spacedatanetwork${EXTENSION}"
    TARGET_FOLDER="./dist/${FOLDER}"

    mkdir -p "${TARGET_FOLDER}"

    echo "Compiling for ${GOOS}/${GOARCH} using ${CC_COMPILER}..."
    CC=${CC_COMPILER} \
        CGO_ENABLED=${CGO_ENABLED_FLAG} \
        GOOS=${GOOS} GOARCH=${GOARCH} \
        go build -a -tags netgo \
        -ldflags "${LDFLAGS}" \
        -o "${TARGET_FOLDER}/${BINARY_NAME}" ./cmd/node/main.go
}

# Set CGO_CFLAGS
export CGO_CFLAGS='-g -O2'

# Compile for Linux target using the appropriate cross-compiler
if [[ -n "$CC_LINUX" ]]; then
    compile "linux" "amd64" "linux" "" "${CC_LINUX}" "1" '-s -w -extldflags "-static"'
fi

# Compile for Windows target using MinGW
if [[ -n "$CC_WINDOWS" ]]; then
    compile "windows" "amd64" "win" ".exe" "${CC_WINDOWS}" "1" '-s -w'
fi

# Compile native macOS binaries if running on macOS
if [[ "$HOST_OS" == "Darwin" ]]; then
    compile "darwin" "amd64" "osx_amd64" "" "${CC_OSX}" "1" '-s -w'
    compile "darwin" "arm64" "osx_arm64" "" "${CC_OSX}" "1" '-s -w'
fi

echo "Cross-compilation completed. Binaries are in the 'dist' folder."

#cp -rf ./dist ../space-data-network/
#cd ../space-data-network
#git add -A
#git commit -m 'updates'
#git push