#!/usr/bin/env bash
# install.sh – build hangar, install to ~/hangar, and configure PATH
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="$HOME/hangar"

# Check for Go
if ! command -v go &>/dev/null; then
    echo "❌ Error: 'go' is not installed or not in PATH." >&2
    echo "Install Go from https://go.dev/dl/ and re-run this script." >&2
    exit 1
fi

# Build
echo "Building hangar..."
mkdir -p "$INSTALL_DIR"
cd "$SCRIPT_DIR"
go build -o "$INSTALL_DIR/hangar" ./cmd/hangar
chmod +x "$INSTALL_DIR/hangar"
echo "Binary installed at: $INSTALL_DIR/hangar"

# Detect shell rc file
if [[ -n "${ZSH_VERSION:-}" ]] || [[ "$SHELL" == */zsh ]]; then
    RC_FILE="$HOME/.zshrc"
elif [[ -n "${BASH_VERSION:-}" ]] || [[ "$SHELL" == */bash ]]; then
    if [[ "$(uname)" == "Darwin" ]]; then
        RC_FILE="$HOME/.bash_profile"
    else
        RC_FILE="$HOME/.bashrc"
    fi
else
    RC_FILE="$HOME/.profile"
fi

# Add INSTALL_DIR to PATH if not already present
if grep -qF "$INSTALL_DIR" "$RC_FILE" 2>/dev/null; then
    echo "PATH already contains $INSTALL_DIR (in $RC_FILE)"
else
    echo "" >> "$RC_FILE"
    echo "# hangar" >> "$RC_FILE"
    echo "export PATH=\"$INSTALL_DIR:\$PATH\"" >> "$RC_FILE"
    echo "Added $INSTALL_DIR to PATH in $RC_FILE"
fi

# Export PATH for the remainder of this script and any sourcing shell.
# Sourcing the RC file directly is not safe here because the script runs in
# bash while the RC file may contain shell-specific syntax (e.g. zsh prompt
# expansions). A plain export achieves the same effect without the risk.
export PATH="$INSTALL_DIR:$PATH"

echo ""
echo "✅ Done! hangar is ready to use."
