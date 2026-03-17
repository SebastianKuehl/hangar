# install.ps1 – build hangar and add this directory to PATH
$ErrorActionPreference = "Stop"

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path

Write-Host "Building hangar..."
Set-Location $ScriptDir
go build -o hangar.exe ./cmd/hangar
Write-Host "Binary built at: $ScriptDir\hangar.exe"

# Add to user PATH persistently
$CurrentPath = [Environment]::GetEnvironmentVariable("PATH", "User")

if ($CurrentPath -like "*$ScriptDir*") {
    Write-Host "PATH already contains $ScriptDir"
} else {
    $NewPath = "$ScriptDir;$CurrentPath"
    [Environment]::SetEnvironmentVariable("PATH", $NewPath, "User")
    Write-Host "Added $ScriptDir to user PATH"
}

# Also update current session
$env:PATH = "$ScriptDir;$env:PATH"

Write-Host ""
Write-Host "Done! You can now invoke: hangar"
Write-Host "(New terminals will have it on PATH automatically)"
