#!/bin/sh

# Run go mod to tidy and verify dependencies
go mod tidy
go mod verify

echo "Setup completed."
