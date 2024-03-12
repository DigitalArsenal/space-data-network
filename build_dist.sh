#!/bin/bash

# Function to compile for a specific OS and Arch
compile() {
    GOOS=$1
    GOARCH=$2
    FOLDER=$3
    EXTENSION=$4

    BINARY_NAME="main${EXTENSION}"
    TARGET_FOLDER="./dist/${FOLDER}"

    # Create target folder if it doesn't exist
    mkdir -p "${TARGET_FOLDER}"

    echo "Compiling for ${GOOS}/${GOARCH}..."
    CGO_ENABLED=0 GOOS=${GOOS} GOARCH=${GOARCH} go build -ldflags "-s" -o "${TARGET_FOLDER}/${BINARY_NAME}" ./cmd/node/main.go
}

# Set CGO_CFLAGS
export CGO_CFLAGS='-g -O2'

# Compile for each platform
compile "linux" "amd64" "linux" ""
compile "windows" "amd64" "win" ".exe"
compile "darwin" "amd64" "osx_amd64" ""
compile "darwin" "arm64" "osx_arm" ""

echo "Cross-compilation completed. Binaries are in the 'dist' folder."

# Define the remote host and target path
HOST="root@Tokyo2"
REMOTE_TARGET_PATH="/opt/software/space-data-network/dist"

# Create the remote directory
ssh ${HOST} "mkdir -p ${REMOTE_TARGET_PATH}"

echo "Transferring 'dist' folder to ${HOST}:${REMOTE_TARGET_PATH}..."
scp -r ./dist/* ${HOST}:${REMOTE_TARGET_PATH}/

echo "Upload to ${HOST} completed."