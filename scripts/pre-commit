#!/bin/sh

echo "Running Go tests..."
export CGO_CFLAGS='-g -O2 -Wno-return-local-addr'
go test ./internal/node/...