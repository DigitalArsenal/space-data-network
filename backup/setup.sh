#!/bin/bash

# Define the project name and directories
PROJECT_NAME="my-go-project"
CMD_DIR="cmd/myapp"
PKG_DIR="pkg/mypackage"
INTERNAL_DIR="internal/myinternal"
TEST_DIR="test"

# Create the project directory structure
mkdir -p {${CMD_DIR},${PKG_DIR},${INTERNAL_DIR},${TEST_DIR}}

# Create a simple main.go file
cat <<EOF > ${CMD_DIR}/main.go
package main

import (
    "fmt"
    "${PKG_DIR}"
)

func main() {
    fmt.Println("Hello from main!")
    mypackage.MyFunction()
}
EOF

# Create a simple mypackage.go file in the pkg directory
cat <<EOF > ${PKG_DIR}/mypackage.go
package mypackage

import "fmt"

func MyFunction() {
    fmt.Println("Hello from mypackage!")
}
EOF

# Create a simple test file for mypackage
cat <<EOF > ${TEST_DIR}/mypackage_test.go
package mypackage_test

import (
    "testing"
    "${PKG_DIR}"
)

func TestMyFunction(t *testing.T) {
    mypackage.MyFunction()
}
EOF

# Initialize a new Go module
go mod init ${PROJECT_NAME}

# Create a nodemon.json configuration file
cat <<EOF > nodemon.json
{
  "watch": ["."],
  "ext": "go",
  "ignore": ["*.test.go", "tmp/*", ".git/*"],
  "exec": "go test ./... && go build -o ./tmp/main ./cmd/myapp && ./tmp/main"
}
EOF

echo "Project ${PROJECT_NAME} setup complete."
