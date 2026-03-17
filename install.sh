#!/usr/bin/env bash
# install.sh – build hangar and add this directory to PATH
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "Building hangar..."
cd "$SCRIPT_DIR"
go build -o hangar ./cmd/hangar
chmod +x hangar
echo "Binary built at: $SCRIPT_DIR/hangar"

# Detect shell config file
if [[ -n "${ZSH_VERSION:-}" ]] || [[ "$SHELL" == */zsh ]]; then
    RC_FILE="$HOME/.zshrc"
elif [[ -n "${BASH_VERSION:-}" ]] || [[ "$SHELL" == */bash ]]; then
    RC_FILE="$HOME/.bashrc"
    [[ "$(uname)" == "Darwin" ]] && RC_FILE="$HOME/.bash_profile"
else
    RC_FILE="$HOME/.profile"
fi

EXPORT_LINE="export PATH=\"$SCRIPT_DIR:\$PATH\""

if grep -qF "$SCRIPT_DIR" "$RC_FILE" 2>/dev/null; then
    echo "PATH already contains $SCRIPT_DIR (in $RC_FILE)"
else
    echo "" >> "$RC_FILE"
    echo "# hangar" >> "$RC_FILE"
    echo "$EXPORT_LINE" >> "$RC_FILE"
    echo "Added $SCRIPT_DIR to PATH in $RC_FILE"
fi

echo ""
echo "✅ Done! Restart your terminal or run:"
echo "   source $RC_FILE"
echo "Then invoke: hangar"
