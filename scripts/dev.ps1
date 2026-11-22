# Runs the panel locally on Windows with settings from .env.local (data in .\.localdata).
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$envFile = Join-Path $root '.env.local'
if (Test-Path $envFile) {
  foreach ($line in Get-Content $envFile) {
    if ($line -match '^\s*([A-Z_][A-Z0-9_]*)\s*=\s*(.*)$') {
      [Environment]::SetEnvironmentVariable($Matches[1], $Matches[2].Trim(), 'Process')
    }
  }
}
if (-not $env:MRSMITH_DATA_DIR) { $env:MRSMITH_DATA_DIR = Join-Path $root '.localdata' }
$go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $go) { $go = 'C:\Program Files\Go\bin\go.exe' }
Set-Location (Join-Path $root 'backend')
& $go run ./cmd/mrsmith serve
exit $LASTEXITCODE
