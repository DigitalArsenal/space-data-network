#!/bin/bash
#
HOSTS=() #"root@Tokyo2" "root@deathstar" "root@api.spaceaware" "root@celestrak.eth")
REMOTE_PATH="/opt/software/space-data-network/space-data-network"
LOCAL_PATH="./tmp/spacedatanetwork"
TIMESTAMP_FILE="./tmp/last_post_build_run"

deploy_and_restart() {
    # Define the remote hosts and the path to the executable

    # Define the start and stop commands for your service
    # Replace these with the actual commands you use to start/stop your executable
    STOP_COMMAND="systemctl stop space-data-network"
    START_COMMAND="systemctl start space-data-network"

    # Loop through each host and perform the operations
    for HOST in "${HOSTS[@]}"; do
        echo "Stopping service on ${HOST}..."
        ssh ${HOST} "${STOP_COMMAND}"

        echo "Transferring executable to ${HOST}..."
        scp $LOCAL_PATH ${HOST}:${REMOTE_PATH}

        echo "Starting service on ${HOST}..."
        ssh ${HOST} "${START_COMMAND}"

        echo "Operation completed on ${HOST}."
    done

    echo "All done."
}

# Detect the operating system
UNAME_S=$(uname -s)

# Set CGO_CFLAGS based on the operating system
if [ "$UNAME_S" == "Darwin" ]; then # macOS
    export CGO_CFLAGS='-g -O2 -Wno-return-stack-address'
else
    export CGO_CFLAGS='-g -O2 -Wno-return-local-addr'
fi

# Execute the Go build command - fast by removing '-a' and only building what has changed
#CC=musl-gcc CGO_ENABLED=1 go build -a -tags netgo -ldflags '-s -w -extldflags "-static"' -o ./tmp/main ./cmd/node/main.go
if [ "$UNAME_S" == "Darwin" ]; then # macOS
    go build -o ./tmp/spacedatanetwork ./cmd/node/main.go
else
    CC=musl-gcc CGO_ENABLED=1 go build -tags netgo -ldflags '-s -w -extldflags "-static"' -o ./tmp/spacedatanetwork ./cmd/node/main.go
fi
# Timestamp file path
TIMESTAMP_FILE="./tmp/last_post_build_run"

# Get current timestamp
CURRENT_TIMESTAMP=$(date +%s)

# Execute post-build script if it exists and if the most recent run was more than 30 seconds ago
# Check if timestamp file exists
if [ -f "$TIMESTAMP_FILE" ]; then
    # Read the last run timestamp
    LAST_RUN_TIMESTAMP=$(cat $TIMESTAMP_FILE)

    # Calculate the time difference
    TIME_DIFF=$((CURRENT_TIMESTAMP - LAST_RUN_TIMESTAMP))

    # Check if time has passed since the last run
    if [ "$TIME_DIFF" -gt 600 ]; then
        deploy_and_restart
        echo $CURRENT_TIMESTAMP >$TIMESTAMP_FILE
    else
        echo "Post-build script last run less than 10 minutes ago, skipping..."
    fi
else
    # If timestamp file does not exist, run the script and create the file
    if [ "$UNAME_S" != "Darwin" ]; then
        deploy_and_restart
    else
        echo "Build completed on macOS, skipping deployment and restart."
    fi
    echo $CURRENT_TIMESTAMP >$TIMESTAMP_FILE
fi