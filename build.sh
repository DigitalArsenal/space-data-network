#!/bin/bash

# Detect the operating system
UNAME_S=$(uname -s)

# Set CGO_CFLAGS based on the operating system
if [ "$UNAME_S" == "Darwin" ]; then # macOS
    export CGO_CFLAGS='-g -O2 -Wno-return-stack-address'
else
    export CGO_CFLAGS='-g -O2 -Wno-return-local-addr'
fi

# Execute the Go build command
go build -ldflags "-s" -o ./tmp/main ./cmd/node/main.go

# Execute post-build script if it exists
if [ -f ./scripts/post-build ]; then
    bash ./scripts/post-build
else
    echo 'No post-build script found, skipping'
fi
