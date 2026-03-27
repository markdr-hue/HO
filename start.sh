#!/bin/bash
echo "Starting HO..."
echo "Admin panel: http://localhost:5001"
echo "Public site:  http://localhost:5000"
echo ""

BINARY="./ho"
if [ ! -f "$BINARY" ]; then
    echo "Building..."
    go build -o "$BINARY" . || { echo "Build failed!"; exit 1; }
fi

exec "$BINARY"
