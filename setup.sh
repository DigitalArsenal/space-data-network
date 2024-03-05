#!/bin/sh

# Run go mod to tidy and verify dependencies
go mod tidy
go mod verify

# Copy the pre-commit hook and set permissions
cp ./scripts/pre-commit .git/hooks/pre-commit
chmod +x .git/hooks/pre-commit

echo "Setup completed."
