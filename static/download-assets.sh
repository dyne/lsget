#!/bin/bash
# Download external JavaScript dependencies for offline/privacy use
# Run this script to update vendored dependencies

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "Downloading JavaScript dependencies to static/..."

# marked.js - Markdown parser
echo "Downloading marked.js..."
curl -fsSL "https://cdn.jsdelivr.net/npm/marked/marked.min.js" -o marked.min.js

# datastar.js - Reactivity framework
echo "Downloading datastar.js..."
curl -fsSL "https://cdn.jsdelivr.net/gh/starfederation/datastar@main/bundles/datastar.js" -o datastar.js

echo ""
echo "✅ Downloads complete!"
echo ""
echo "Files:"
ls -lh marked.min.js datastar.js
echo ""
echo "Next steps:"
echo "  1. Build the binary: go build ."
echo "  2. The assets will be embedded in the binary"
echo "  3. Served locally at /static/marked.min.js and /static/datastar.js"
