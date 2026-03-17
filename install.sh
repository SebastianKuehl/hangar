#!/usr/bin/env bash
# install.sh – build hangar, install to ~/hangar, and configure PATH
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="$HOME/hangar"

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

# Source the rc file so hangar is available immediately in the current shell
# (effective when this script is run via: source install.sh)
# shellcheck disable=SC1090
source "$RC_FILE"

echo ""
echo "✅ Done! hangar is ready to use."
