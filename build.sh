#!/bin/bash
set -e
echo "Building m-cpg-go to deploy/ folder..."
go build -o deploy/m-cpg-go .
echo "Build complete. Executable located at deploy/m-cpg-go"
