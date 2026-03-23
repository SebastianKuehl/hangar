# hangar

A Go TUI (Bubble Tea) scaffold.

## Install

Clone the repo, then run the install script for your platform. It builds the binary inside the repo directory and adds that directory to your `PATH` so you can invoke `hangar` from anywhere.

**Prerequisites:** [Go](https://go.dev/dl/) must be installed.

### macOS / Linux

```bash
git clone https://github.com/SebastianKuehl/hangar.git
cd hangar
bash install.sh
source ~/.zshrc   # or ~/.bash_profile / ~/.bashrc depending on your shell
hangar
```

The script auto-detects your shell (`zsh`, `bash`, or POSIX `sh`) and appends the export to the appropriate rc file. Re-running it is safe — it won't add a duplicate entry.

### Windows (PowerShell)

```powershell
git clone https://github.com/SebastianKuehl/hangar.git
cd hangar
.\install.ps1
hangar
```

The script updates your **user** `PATH` permanently via `[Environment]::SetEnvironmentVariable`. New terminal sessions will have `hangar` available automatically; the current session is patched immediately.

### Updating

```bash
git pull
bash install.sh   # rebuilds the binary in-place
```

## Run (without installing)

## Hotkeys

- `p`: toggle Projects pane
- `d`: toggle Details pane
- `a`: toggle Logs pane
- `h` / `l` (or ← / →): move focus between panes
- `j` / `k` (or ↓ / ↑): move selection within the focused pane
- `?`: show hotkey help modal
- `q`: quit

## Project creation

Creating a project now requires a project folder. The entered path is normalized to the current operating system, so relative paths and `~`-prefixed home paths work on macOS, Linux, and Windows.

When a project is saved, Hangar scans that folder for Node and Bun services by looking for `package.json` files that define a `start` script. Each discovered service is added to the config automatically with either `npm run start` or `bun run start`.

All pane contents outside project and service config are still intentionally **placeholder** data right now so we can validate navigation and layout.
