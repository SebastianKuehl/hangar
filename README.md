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
- `s`: start the selected service when stopped, or stop it when running
- `h` / `l` (or ← / →): move focus between panes
- `j` / `k` (or ↓ / ↑): move selection within the focused pane
- `?`: show hotkey help modal
- `q`: quit

## Project creation

Creating a project now requires a project folder. The entered path is normalized to the current operating system, so relative paths and `~`-prefixed home paths work on macOS, Linux, and Windows.

When a project is saved, Hangar scans that folder for Node and Bun services by looking for `package.json` files that define a `start` script. Each discovered service is added to the config automatically with either `npm run start` or `bun run start`.

## Service runtime panes

Hangar now manages service runtime state through durable files under `~/hangar`:

- `~/hangar/logs/` stores combined stdout/stderr logs per managed service
- `~/hangar/run/` stores runtime metadata (PID, command, log path, timestamps)

When you move the cursor through the Services pane, Hangar refreshes persisted runtime metadata and verifies whether the recorded PID is still alive. The Services pane shows:

- `●` when Hangar found a matching running process
- `○` when no matching process is running
- `◌` while runtime detection is still refreshing

The Details pane updates with the selected service's path, command, status, PID, working directory, and log file path.

The Logs pane follows the selected service's log file instead of attaching directly to process pipes. That means you can switch between services without interrupting them, quit the TUI, reopen `hangar`, and immediately reattach to the same recent log backlog and ongoing output.

When you press `s`, Hangar starts a stopped service as a detached child process, appends lifecycle markers to its log, and persists runtime metadata. Stopping a service signals its managed process group and keeps the historical log file available after exit.
