#!/bin/bash
cd "$(dirname "$0")"
echo "Building..."
go build -o bot ./cmd/bot/ || exit 1
echo "Starting react2issue..."
exec ./bot -config config.yaml
