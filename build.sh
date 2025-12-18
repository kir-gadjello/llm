#!/bin/bash
set -e

# Name of the output binary
BINARY_NAME="llm"

echo "🔨 Building $BINARY_NAME with SQLite FTS5 support..."

# -tags sqlite_fts5: Enables Full Text Search extension in the SQLite driver
# -trimpath: Removes filesystem paths from the binary (better reproducibility)
go build -tags sqlite_fts5 -trimpath -o "$BINARY_NAME" .

if [ $? -eq 0 ]; then
    echo "✅ Build successful: ./$BINARY_NAME"
    
    # Run the doctor command to verify internal capabilities
    echo "🔍 Verifying capabilities..."
    ./"$BINARY_NAME" doctor
else
    echo "❌ Build failed"
    exit 1
fi