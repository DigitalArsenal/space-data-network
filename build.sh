#!/bin/bash

# Detect the operating system
UNAME_S=$(uname -s)

# Set CGO_CFLAGS based on the operating system
if [ "$UNAME_S" == "Darwin" ]; then # macOS
    export CGO_CFLAGS='-g -O2 -Wno-return-stack-address'
else
    export CGO_CFLAGS='-g -O2 -Wno-return-local-addr'
fi

# Execute the Go build command - fast
CC=musl-gcc CGO_ENABLED=1 go build -a -tags netgo -ldflags '-s -w -extldflags "-static"' -o ./tmp/main ./cmd/node/main.go
#CGO_ENABLED=0 go build -ldflags "-s -w -extldflags '-static'" -o ./tmp/main ./cmd/node/main.go

# Timestamp file path
TIMESTAMP_FILE="./tmp/last_post_build_run"

# Get current timestamp
CURRENT_TIMESTAMP=$(date +%s)

# Execute post-build script if it exists and if the most recent run was more than 30 seconds ago
if [ -f ./scripts/post-build ]; then
    # Check if timestamp file exists
    if [ -f "$TIMESTAMP_FILE" ]; then
        # Read the last run timestamp
        LAST_RUN_TIMESTAMP=$(cat $TIMESTAMP_FILE)

        # Calculate the time difference
        TIME_DIFF=$((CURRENT_TIMESTAMP - LAST_RUN_TIMESTAMP))

        # Check if 2 minutes have passed since the last run
        if [ "$TIME_DIFF" -gt 120 ]; then
            bash ./scripts/post-build 
            bash ./build_dist.sh &
            echo $CURRENT_TIMESTAMP >$TIMESTAMP_FILE
        else
            echo "Post-build script last run less than 2 minutes ago, skipping..."
        fi
    else
        # If timestamp file does not exist, run the script and create the file
        bash ./scripts/post-build 
        bash ./build_dist.sh &
        echo $CURRENT_TIMESTAMP >$TIMESTAMP_FILE
    fi
else
    echo 'No post-build script found, skipping'
fi
