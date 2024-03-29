#!/bin/bash

# Function to compile for a specific OS and Arch
compile() {
    GOOS=$1
    GOARCH=$2
    FOLDER=$3
    EXTENSION=$4
    CC_COMPILER=$5
    CGO_ENABLED_FLAG=$6
    LDFLAGS=$7

    BINARY_NAME="main${EXTENSION}"
    TARGET_FOLDER="./dist/${FOLDER}"

    # Create target folder if it doesn't exist
    mkdir -p "${TARGET_FOLDER}"

    echo "Compiling for ${GOOS}/${GOARCH}..."
    CC=${CC_COMPILER} \
        CGO_ENABLED=${CGO_ENABLED_FLAG} \
        GOOS=${GOOS} GOARCH=${GOARCH} \
        go build -a -tags netgo \
        -ldflags "${LDFLAGS}" \
        -o "${TARGET_FOLDER}/${BINARY_NAME}" ./cmd/node/main.go
}

# Set CGO_CFLAGS
export CGO_CFLAGS='-g -O2'

# Compile for Linux with musl-gcc
compile "linux" "amd64" "linux" "" "musl-gcc" "1" '-s -w -extldflags "-static"'

# Compile for Windows with MinGW
compile "windows" "amd64" "win" ".exe" "x86_64-w64-mingw32-gcc" "1" '-s -w'

# Compiling for macOS typically requires a macOS environment. These commands are placeholders
# and might not work as expected in a non-macOS environment. Consider using osxcross or compiling natively on macOS.
# compile "darwin" "amd64" "osx_amd64" "" "musl-gcc" "1" '-s -w'
# compile "darwin" "arm64" "osx_arm" "" "musl-gcc" "1" '-s -w'

echo "Cross-compilation completed. Binaries are in the 'dist' folder."

cp -rf ./dist ../space-data-network/
cd ../space-data-network
git add -A
git commit -m 'updates'
git push
echo "Upload to ${HOST} completed."

# Define the remote host and target path
#HOST="root@Tokyo2"
#REMOTE_TARGET_PATH="/opt/software/space-data-network/dist"

# Create the remote directory
#ssh ${HOST} "mkdir -p ${REMOTE_TARGET_PATH}"

#echo "Transferring 'dist' folder to ${HOST}:${REMOTE_TARGET_PATH}..."
#scp -r ./dist/* ${HOST}:${REMOTE_TARGET_PATH}/
