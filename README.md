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
- `e`: edit the selected project or service
- `s`: start the selected service when stopped, or stop it when running
- `i`: interrupt the current service check
- `r`: retry an interrupted service check
- `R`: restart the selected service, or restart all services when the Projects pane is focused
- `h` / `l` (or ← / →): move focus between panes
- `j` / `k` (or ↓ / ↑): move selection within the focused pane
- `?`: show hotkey help modal
- `q`: quit

## Project creation

The project path is optional. When you provide one, Hangar normalizes it to the current operating system, so relative paths and `~`-prefixed home paths work on macOS, Linux, and Windows.

When a project is saved with a path, Hangar scans that folder for Node and Bun services by looking for `package.json` files that define a `start` script. Each discovered service is added to the config automatically with either `npm run start` or `bun run start`.

If you leave the project path blank, Hangar creates the project without scanning the filesystem. This is useful when a project's services live under different root folders and will be added manually later.

Press `e` with the cursor on a project or service to edit it. Project and service forms open prefilled with the current values.

Service forms now include a command selector. When Hangar finds a runtime config such as a Node/Bun `package.json`, it lists the available scripts as selectable commands (for example `npm run dev`, `npm run start`, or `bun run test`).

## Service runtime panes

When you move the cursor through the Services pane, Hangar now polls the local process list and tries to match each service to a running Node/Bun process by service directory and runtime. The Services pane shows:

- `●` when Hangar found a matching running process
- `○` when no matching process is running
- `◌` while runtime detection is still refreshing

During the initial runtime pre-check for the selected project, Hangar overlays the Services/Details/Logs area with a centered loading panel so the scan is obvious before those per-service indicators settle.

If a runtime scan takes too long, you can press `i` to interrupt it. Hangar will ignore the in-flight result and stop auto-refreshing that project until you press `r` to retry or move to another selection.

The Details pane updates with the selected service's path, command, process status, PID, memory, and start time.

The Logs pane now reflects the selected service's runtime state too. For already-running external processes, Hangar can show detection details but cannot attach to arbitrary existing stdout streams cross-platform, so the pane explains that limitation instead of faking log output.

When you press `s`, Hangar starts a stopped service or force-stops the confident running targets for that service. The selected service stays locked until runtime polling confirms the requested state, but you can still move around the UI and trigger `s` for other services while that happens.

When you press `R` in the Services pane, Hangar restarts the selected service by stopping the matched process tree and launching the configured start command again. When you press `R` in the Projects pane, Hangar applies that same restart/start behavior across every service in the selected project that already has runtime information loaded.
