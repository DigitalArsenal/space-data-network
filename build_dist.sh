#!/bin/bash

# Function to compile for a specific OS and Arch
compile() {
    GOOS=$1
    GOARCH=$2
    FOLDER=$3
    EXTENSION=$4
    CC_COMPILER=$5
    CGO_ENABLED_FLAG=$6

    BINARY_NAME="main${EXTENSION}"
    TARGET_FOLDER="./dist/${FOLDER}"

    # Create target folder if it doesn't exist
    mkdir -p "${TARGET_FOLDER}"

    echo "Compiling for ${GOOS}/${GOARCH}..."
    CC=${CC_COMPILER} \
        CGO_ENABLED=${CGO_ENABLED_FLAG} \
        GOOS=${GOOS} GOARCH=${GOARCH} \
        go build -a -tags netgo \
        -ldflags '-s -w -extldflags "-static"' \
        -o "${TARGET_FOLDER}/${BINARY_NAME}" ./cmd/node/main.go
}

# Set CGO_CFLAGS
export CGO_CFLAGS='-g -O2'

# Compile for Linux with musl-gcc
compile "linux" "amd64" "linux" "" "musl-gcc" "1"

# Compile for Windows and macOS using default toolchains
# Note that static linking is not common on Windows and macOS, especially for macOS due to system library dependencies.
compile "windows" "amd64" "win" ".exe" "" "0"
compile "darwin" "amd64" "osx_amd64" "" "" "0"
compile "darwin" "arm64" "osx_arm" "" "" "0"

echo "Cross-compilation completed. Binaries are in the 'dist' folder."

# Define the remote host and target path
HOST="root@Tokyo2"
REMOTE_TARGET_PATH="/opt/software/space-data-network/dist"

# Create the remote directory
ssh ${HOST} "mkdir -p ${REMOTE_TARGET_PATH}"

echo "Transferring 'dist' folder to ${HOST}:${REMOTE_TARGET_PATH}..."
scp -r ./dist/* ${HOST}:${REMOTE_TARGET_PATH}/

echo "Upload to ${HOST} completed."
