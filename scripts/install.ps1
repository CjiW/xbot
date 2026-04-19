<#
.SYNOPSIS
    xbot-cli installer for Windows
.DESCRIPTION
    Downloads and installs xbot-cli from GitHub Releases.
    Supports standalone and server-client modes with optional Windows service.
.PARAMETER Version
    Specific version to install (defaults to latest release).
.PARAMETER InstallPath
    Installation directory (defaults to $env:USERPROFILE\.local\bin).
.PARAMETER Mode
    Install mode: "standalone" (default) or "server-client".
.PARAMETER Port
    WebSocket port for server-client mode (default 8080).
.EXAMPLE
    irm https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.ps1 | iex
.EXAMPLE
    .\install.ps1 -Version v0.1.0
.EXAMPLE
    .\install.ps1 -InstallPath C:\Tools
.EXAMPLE
    .\install.ps1 -Mode server-client -Port 9090
#>

param(
    [string]$Version = "",
    [string]$InstallPath = "",
    [string]$Mode = "",
    [int]$Port = 0
)

$ErrorActionPreference = "Stop"

$REPO = "CjiW/xbot"
$BINARY = "xbot-cli.exe"
$SERVICE_NAME = "xbot-server"
$DEFAULT_PORT = 8080

if (-not $InstallPath) {
    $InstallPath = Join-Path $env:USERPROFILE ".local\bin"
}

$XbotHome = if ($env:XBOT_HOME) { $env:XBOT_HOME } else { Join-Path $env:USERPROFILE ".xbot" }
$ConfigPath = Join-Path $XbotHome "config.json"

# --- Color helpers ---
function Write-Info  { param([string]$Msg) Write-Host "[INFO] $Msg" -ForegroundColor Green }
function Write-Warn  { param([string]$Msg) Write-Host "[WARN] $Msg" -ForegroundColor Yellow }
function Write-Err   { param([string]$Msg) Write-Host "[ERROR] $Msg" -ForegroundColor Red; exit 1 }

# --- Detect platform ---

# --- Convert PSCustomObject to Hashtable (PowerShell 5.1 compatibility) ---
function ConvertTo-Ht {
    param([Parameter(ValueFromPipeline)]$InputObject)
    if ($InputObject -is [System.Collections.IDictionary]) {
        $ht = @{}
        foreach ($key in $InputObject.Keys) {
            $ht[$key] = ConvertTo-Ht $InputObject[$key]
        }
        return $ht
    }
    if ($InputObject -is [PSCustomObject]) {
        $ht = @{}
        foreach ($prop in $InputObject.PSObject.Properties) {
            $ht[$prop.Name] = ConvertTo-Ht $prop.Value
        }
        return $ht
    }
    if ($InputObject -is [System.Collections.IList]) {
        $list = @()
        foreach ($item in $InputObject) {
            $list += ConvertTo-Ht $item
        }
        return $list
    }
    return $InputObject
}

function Get-Platform {
    $arch = $env:PROCESSOR_ARCHITECTURE
    switch ($arch) {
        "AMD64" { return "windows-amd64" }
        "ARM64" { return "windows-arm64" }
        default { Write-Err "Unsupported architecture: $arch. Only AMD64 and ARM64 are supported." }
    }
}

# --- Resolve version ---
function Get-LatestVersion {
    if ($Version) { return $Version }

    try {
        $response = Invoke-RestMethod -Uri "https://api.github.com/repos/$REPO/releases/latest" -Headers @{"User-Agent"="PowerShell"}
        return $response.tag_name
    } catch {
        Write-Err "Failed to determine latest version. Set -Version explicitly, e.g.: .\install.ps1 -Version v0.1.0"
    }
}

# --- Generate random token (pure PowerShell, no python needed) ---
function New-RandomToken {
    $bytes = New-Object byte[] 16
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    return -join ($bytes | ForEach-Object { $_.ToString("x2") })
}

# --- Ask mode interactively ---
function Ask-Mode {
    if ($Mode) { return $Mode }

    Write-Host ""
    Write-Host "Choose install mode:" -ForegroundColor Cyan
    Write-Host "  1) standalone      - CLI runs locally in-process" -ForegroundColor Cyan
    Write-Host "  2) server-client   - install local server service, CLI connects remotely" -ForegroundColor Cyan
    $choice = Read-Host "Select [1/2] (default 1)"

    switch ($choice) {
        "1" { return "standalone" }
        "2" { return "server-client" }
        ""  { return "standalone" }
        default { Write-Err "Invalid selection: $choice" }
    }
}

# --- Ask port interactively ---
function Ask-Port {
    if ($Port -gt 0) { return $Port }
    if ($selectedMode -ne "server-client") { return $DEFAULT_PORT }

    $portInput = Read-Host "WebSocket port for local server [$DEFAULT_PORT]"
    if ($portInput -match '^\d+$') {
        return [int]$portInput
    }
    return $DEFAULT_PORT
}

# --- Backup existing config ---
function Backup-Config {
    if (Test-Path $ConfigPath) {
        $ts = Get-Date -Format "yyyyMMdd-HHmmss"
        $backup = "$ConfigPath.bak.$ts"
        Copy-Item $ConfigPath $backup -Force
        Write-Info "Backed up existing config to $backup"
    }
}

# --- Write config.json (pure PowerShell, mirrors install.sh python logic) ---
function Write-Config {
    param(
        [string]$Mode,
        [int]$Port,
        [string]$Token
    )

    if (-not (Test-Path $XbotHome)) {
        New-Item -ItemType Directory -Path $XbotHome -Force | Out-Null
    }

    # Read existing config or start fresh
    $cfg = @{}
    if (Test-Path $ConfigPath) {
        try {
            $raw = Get-Content $ConfigPath -Raw -Encoding UTF8
            $cfg = $raw | ConvertFrom-Json | ConvertTo-Ht
        } catch {
            $cfg = @{}
        }
    }

    # Ensure top-level sections exist
    foreach ($section in @("server", "web", "cli", "admin", "agent")) {
        if (-not $cfg.ContainsKey($section)) {
            $cfg[$section] = @{}
        }
    }

    $changes = [System.Collections.ArrayList]::new()
    $preserved = [System.Collections.ArrayList]::new()

    function Set-IfMissing([string]$Section, [string]$Key, [object]$Value) {
        $sectionDict = $cfg[$Section]
        if (-not $sectionDict.ContainsKey($Key) -or [string]::IsNullOrEmpty($sectionDict[$Key])) {
            $sectionDict[$Key] = $Value
            [void]$changes.Add("$Section.$Key=$Value")
        } else {
            [void]$preserved.Add("$Section.$Key=$($sectionDict[$Key])")
        }
    }

    function Set-Always([string]$Section, [string]$Key, [object]$Value) {
        $sectionDict = $cfg[$Section]
        $old = $sectionDict[$Key]
        $sectionDict[$Key] = $Value
        if ($old -ne $Value) {
            [void]$changes.Add("$Section.$Key=$Value (was $old)")
        } else {
            [void]$preserved.Add("$Section.$Key=$old")
        }
    }

    Set-IfMissing "admin" "token" $Token
    $adminToken = $cfg["admin"]["token"]
    if (-not $adminToken) { $adminToken = $Token }

    # Ensure agent.work_dir is set to user home so server has a stable working directory
    Set-IfMissing "agent" "work_dir" $env:USERPROFILE

    if ($Mode -eq "server-client") {
        Set-IfMissing "server" "host" "127.0.0.1"
        Set-Always  "server" "port" $Port
        Set-Always  "web"    "enable" $true
        Set-IfMissing "web"  "host" "127.0.0.1"
        Set-Always  "web"    "port" $Port
        Set-Always  "cli"    "server_url" "ws://127.0.0.1:$Port"
        Set-Always  "cli"    "token" $adminToken
    } else {
        Set-IfMissing "cli" "token" $adminToken
    }

    # Write JSON with proper formatting
    $json = $cfg | ConvertTo-Json -Depth 10
    Set-Content -Path $ConfigPath -Value $json -Encoding UTF8

    # Report changes
    foreach ($item in $changes) {
        Write-Info "Config set: $item"
    }
    foreach ($item in $preserved) {
        Write-Warn "Config preserved: $item"
    }
}

# --- Check if running as Administrator ---
function Test-IsAdmin {
    try {
        $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
        $principal = New-Object Security.Principal.WindowsPrincipal($identity)
        return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    } catch {
        return $false
    }
}

# --- Download nssm if not present ---
function Ensure-Nssm {
    # Check if nssm is already on PATH
    $nssmExe = (Get-Command nssm -ErrorAction SilentlyContinue).Source
    if ($nssmExe -and (Test-Path $nssmExe)) {
        Write-Info "nssm found at $nssmExe"
        return $nssmExe
    }

    # Check common locations
    $commonPaths = @(
        (Join-Path $env:ProgramFiles "NSSM\nssm.exe"),
        (Join-Path ${env:ProgramFiles(x86)} "NSSM\nssm.exe"),
        (Join-Path $InstallPath "nssm.exe"),
        (Join-Path $env:TEMP "nssm.exe")
    )
    foreach ($p in $commonPaths) {
        if (Test-Path $p) {
            Write-Info "nssm found at $p"
            return $p
        }
    }

    # Offer to download
    Write-Host ""
    Write-Warn "nssm (Non-Sucking Service Manager) is required to install xbot as a Windows service."
    $download = Read-Host "Download nssm? [Y/n]"
    if ($download -match '^[Nn]') {
        return $null
    }

    Write-Info "Downloading nssm..."
    $nssmZip = Join-Path $env:TEMP "nssm.zip"
    $nssmDir = Join-Path $env:TEMP "nssm"

    try {
        # Download latest nssm release (2.24 is the last stable, use GitHub releases)
        $nssmUrl = "https://nssm.cc/release/nssm-2.24.zip"
        Invoke-WebRequest -Uri $nssmUrl -OutFile $nssmZip -UseBasicParsing

        # Extract
        if (Test-Path $nssmDir) { Remove-Item $nssmDir -Recurse -Force }
        Expand-Archive -Path $nssmZip -DestinationPath $nssmDir -Force

        # Find the right exe (win64 or win32)
        $nssmBin = Join-Path $nssmDir "nssm-2.24\win64\nssm.exe"
        if (-not (Test-Path $nssmBin)) {
            $nssmBin = Join-Path $nssmDir "nssm-2.24\win32\nssm.exe"
        }
        if (-not (Test-Path $nssmBin)) {
            Write-Err "Failed to extract nssm binary"
        }

        # Copy to install path
        $destNssm = Join-Path $InstallPath "nssm.exe"
        Copy-Item $nssmBin $destNssm -Force
        Write-Info "nssm installed to $destNssm"
        return $destNssm
    } catch {
        Write-Warn "Failed to download nssm: $_"
        return $null
    } finally {
        # Cleanup temp files
        Remove-Item $nssmZip -Force -ErrorAction SilentlyContinue
        Remove-Item $nssmDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

# --- Install Windows service using nssm ---
function Install-ServiceNssm {
    param(
        [string]$NssmPath,
        [string]$BinPath,
        [string]$CfgPath
    )

    $workDir = $env:USERPROFILE

    # Stop and remove existing service if present
    & $NssmPath stop $SERVICE_NAME 2>$null
    & $NssmPath remove $SERVICE_NAME confirm 2>$null

    # Install the service
    & $NssmPath install $SERVICE_NAME $BinPath "serve --config $CfgPath"
    if ($LASTEXITCODE -ne 0) {
        Write-Err "nssm install failed with exit code $LASTEXITCODE"
    }

    # Configure service parameters
    & $NssmPath set $SERVICE_NAME AppDirectory $workDir
    & $NssmPath set $SERVICE_NAME DisplayName "xbot Agent Server"
    & $NssmPath set $SERVICE_NAME Description "xbot AI Agent Server - WebSocket-based AI assistant service"
    & $NssmPath set $SERVICE_NAME Start SERVICE_AUTO_START

    # Set XBOT_HOME environment variable for the service
    & $NssmPath set $SERVICE_NAME AppEnvironmentExtra "XBOT_HOME=$XbotHome"

    # Set stdout/stderr logging
    $logDir = Join-Path $XbotHome "logs"
    if (-not (Test-Path $logDir)) {
        New-Item -ItemType Directory -Path $logDir -Force | Out-Null
    }
    & $NssmPath set $SERVICE_NAME AppStdout (Join-Path $logDir "xbot-server.log")
    & $NssmPath set $SERVICE_NAME AppStderr (Join-Path $logDir "xbot-server.err")

    # Set rotation for log files
    & $NssmPath set $SERVICE_NAME AppRotateFiles 1
    & $NssmPath set $SERVICE_NAME AppRotateBytes 10485760

    # Start the service
    & $NssmPath start $SERVICE_NAME
    if ($LASTEXITCODE -eq 0) {
        Write-Info "Windows service '$SERVICE_NAME' installed and started successfully"
    } else {
        Write-Warn "Service installed but failed to start (exit code $LASTEXITCODE). Check logs at $logDir"
    }
}

# --- Install Windows service using sc.exe as fallback ---
function Install-ServiceSc {
    param(
        [string]$BinPath,
        [string]$CfgPath
    )

    Write-Warn "Using sc.exe for service management (limited functionality)."
    Write-Warn "Consider installing nssm for better service management."

    # Stop and remove existing service
    sc.exe stop $SERVICE_NAME 2>$null
    sc.exe delete $SERVICE_NAME 2>$null
    Start-Sleep -Seconds 2

    $binArgs = "serve --config $CfgPath"
    $workDir = $env:USERPROFILE

    # Create the service using sc.exe
    sc.exe create $SERVICE_NAME binPath= "`"$BinPath`" $binArgs" start= auto DisplayName= "xbot Agent Server"
    if ($LASTEXITCODE -ne 0) {
        Write-Err "sc.exe create failed with exit code $LASTEXITCODE"
    }

    # Set description
    sc.exe description $SERVICE_NAME "xbot AI Agent Server - WebSocket-based AI assistant service"

    # Set XBOT_HOME as a service-level environment variable via registry
    try {
        $regPath = "HKLM:\SYSTEM\CurrentControlSet\Services\$SERVICE_NAME"
        $currentEnv = (Get-ItemProperty -Path $regPath -Name "Environment" -ErrorAction SilentlyContinue).Environment
        if ($currentEnv) {
            $newEnv = "$currentEnv`nXBOT_HOME=$XbotHome"
        } else {
            $newEnv = "XBOT_HOME=$XbotHome"
        }
        Set-ItemProperty -Path $regPath -Name "Environment" -Value $newEnv
    } catch {
        Write-Warn "Could not set XBOT_HOME environment variable for service: $_"
    }

    # Start the service
    sc.exe start $SERVICE_NAME
    if ($LASTEXITCODE -eq 0) {
        Write-Info "Windows service '$SERVICE_NAME' installed and started successfully (via sc.exe)"
    } else {
        Write-Warn "Service installed but failed to start (exit code $LASTEXITCODE). Check Windows Event Viewer."
    }
}

# --- Install as Scheduled Task (last resort fallback) ---
function Install-ScheduledTask {
    param(
        [string]$BinPath,
        [string]$CfgPath
    )

    Write-Warn "Using Scheduled Task as fallback service mechanism."
    Write-Warn "The server will start at user logon and restart on failure."

    $taskName = "xbot-server"
    $workDir = $env:USERPROFILE

    # Remove existing task
    schtasks.exe /Delete /TN $taskName /F 2>$null

    # Create a batch wrapper script
    $wrapperDir = Join-Path $XbotHome "scripts"
    if (-not (Test-Path $wrapperDir)) {
        New-Item -ItemType Directory -Path $wrapperDir -Force | Out-Null
    }
    $wrapperScript = Join-Path $wrapperDir "run-server.bat"
    Set-Content -Path $wrapperScript -Value "@echo off`nset XBOT_HOME=$XbotHome`ncd /d `"$workDir`"`n`"$BinPath`" serve --config `"$CfgPath`"" -Encoding ASCII

    # Create scheduled task
    $action = New-ScheduledTaskAction -Execute $wrapperScript -WorkingDirectory $workDir
    $trigger = New-ScheduledTaskTrigger -AtLogOn
    $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)

    # Try to use the modern ScheduledTasks cmdlets (PowerShell 4+)
    try {
        Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Settings $settings -Description "xbot AI Agent Server" -RunLevel Highest -Force
        Write-Info "Scheduled Task '$taskName' registered successfully"
    } catch {
        # Fallback to schtasks.exe
        schtasks.exe /Create /SC ONLOGON /TN $taskName /TR "`"$wrapperScript`"" /RL HIGHEST /F
        if ($LASTEXITCODE -eq 0) {
            Write-Info "Scheduled Task '$taskName' created successfully (via schtasks.exe)"
        } else {
            Write-Err "Failed to create scheduled task: $_"
        }
    }

    # Start the task immediately
    try {
        Start-ScheduledTask -TaskName $taskName
        Write-Info "Scheduled Task started"
    } catch {
        Write-Warn "Could not auto-start the task. It will start at next logon."
    }
}

# --- Main service installation orchestrator ---
function Install-WindowsService {
    param(
        [string]$BinPath,
        [string]$CfgPath
    )

    if (-not (Test-IsAdmin)) {
        Write-Warn "Windows service installation requires Administrator privileges."
        Write-Warn "The server can still be started manually: $BinPath serve --config $CfgPath"
        Write-Warn "To install as a service, re-run this script as Administrator."
        return
    }

    Write-Host ""
    Write-Host "Choose service installation method:" -ForegroundColor Cyan
    Write-Host "  1) nssm (recommended) - Non-Sucking Service Manager, best for Go binaries" -ForegroundColor Cyan
    Write-Host "  2) sc.exe (built-in)    - Windows built-in, limited functionality" -ForegroundColor Cyan
    Write-Host "  3) Scheduled Task       - Fallback, runs at user logon" -ForegroundColor Cyan
    Write-Host "  4) Skip service install - Start server manually" -ForegroundColor Cyan
    $svcChoice = Read-Host "Select [1/2/3/4] (default 1)"

    switch ($svcChoice) {
        "1" { "" }  # nssm - proceed below
        "2" { Install-ServiceSc -BinPath $BinPath -CfgPath $CfgPath; return }
        "3" { Install-ScheduledTask -BinPath $BinPath -CfgPath $CfgPath; return }
        "4" { Write-Info "Skipping service installation."; return }
        ""  { "" }  # nssm default - proceed below
        default { Write-Err "Invalid selection: $svcChoice" }
    }

    # --- nssm path ---
    $nssmPath = Ensure-Nssm
    if ($nssmPath) {
        Install-ServiceNssm -NssmPath $nssmPath -BinPath $BinPath -CfgPath $CfgPath
    } else {
        Write-Warn "nssm not available."
        Write-Host ""
        $fallback = Read-Host "Fall back to sc.exe? [Y/n]"
        if ($fallback -notmatch '^[Nn]') {
            Install-ServiceSc -BinPath $BinPath -CfgPath $CfgPath
        } else {
            $fallback2 = Read-Host "Use Scheduled Task instead? [Y/n]"
            if ($fallback2 -notmatch '^[Nn]') {
                Install-ScheduledTask -BinPath $BinPath -CfgPath $CfgPath
            } else {
                Write-Info "Skipping service installation."
                Write-Info "Start server manually: $BinPath serve --config $CfgPath"
            }
        }
    }
}

# ============================================================
# Main
# ============================================================

Write-Host ""
Write-Host "  =======================================" -ForegroundColor Cyan
Write-Host "         xbot-cli Installer (Windows)" -ForegroundColor Cyan
Write-Host "  =======================================" -ForegroundColor Cyan
Write-Host ""

$platform = Get-Platform
$tag = Get-LatestVersion
$downloadUrl = "https://github.com/$REPO/releases/download/$tag/xbot-cli-$platform.exe"

Write-Info "Platform:  $platform"
Write-Info "Version:   $tag"
Write-Info "URL:       $downloadUrl"
Write-Info "Install:   $InstallPath\$BINARY"
Write-Info "Config:    $ConfigPath"
Write-Host ""

# --- Mode selection ---
$selectedMode = Ask-Mode
$selectedPort = Ask-Port

if ($selectedMode -eq "server-client") {
    Write-Info "Mode:      server-client (port $selectedPort)"
} else {
    Write-Info "Mode:      standalone"
}

# --- Create install directory ---
if (-not (Test-Path $InstallPath)) {
    New-Item -ItemType Directory -Path $InstallPath -Force | Out-Null
    Write-Info "Created directory: $InstallPath"
}

# --- Download ---
Write-Info "Downloading..."
$tmpFile = Join-Path ([System.IO.Path]::GetTempPath()) "xbot-cli-download.exe"

try {
    Invoke-WebRequest -Uri $downloadUrl -OutFile $tmpFile -UseBasicParsing
} catch {
    Write-Err "Download failed: $_"
}

# --- Verify checksum if possible ---
$checksumUrl = "https://github.com/$REPO/releases/download/$tag/checksums.txt"
try {
    $checksumFile = Join-Path ([System.IO.Path]::GetTempPath()) "xbot-checksums.txt"
    Invoke-WebRequest -Uri $checksumUrl -OutFile $checksumFile -UseBasicParsing
    $expectedLine = Get-Content $checksumFile | Where-Object { $_ -match "xbot-cli-$platform" }
    if ($expectedLine) {
        $expectedHash = ($expectedLine -split "\s+")[0]
        $actualHash = (Get-FileHash -Path $tmpFile -Algorithm SHA256).Hash.ToLower()
        if ($expectedHash -ne $actualHash) {
            Remove-Item $tmpFile -Force -ErrorAction SilentlyContinue
            Write-Err "Checksum mismatch! Expected: $expectedHash, Got: $actualHash"
        }
        Write-Info "Checksum verified"
    }
    Remove-Item $checksumFile -Force -ErrorAction SilentlyContinue
} catch {
    Write-Warn "Checksum verification skipped"
}

# --- Install binary ---
Copy-Item $tmpFile (Join-Path $InstallPath $BINARY) -Force
Remove-Item $tmpFile -Force -ErrorAction SilentlyContinue

Write-Host ""
Write-Host "[OK] xbot-cli $tag installed to $InstallPath\$BINARY" -ForegroundColor Green

# --- Add to PATH if not already there ---
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$InstallPath*") {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$InstallPath", "User")
    $env:Path = "$env:Path;$InstallPath"
    Write-Info "Added $InstallPath to user PATH"
    Write-Warn "Please restart your terminal for PATH changes to take effect."
}

# --- Generate config ---
$token = New-RandomToken
Backup-Config
Write-Config -Mode $selectedMode -Port $selectedPort -Token $token

# --- Install service for server-client mode ---
if ($selectedMode -eq "server-client") {
    $binFullPath = Join-Path $InstallPath $BINARY
    Install-WindowsService -BinPath $binFullPath -CfgPath $ConfigPath
}

# --- Done ---
Write-Host ""
Write-Host "  =======================================" -ForegroundColor Cyan
Write-Host "  Installation Complete" -ForegroundColor Cyan
Write-Host "  =======================================" -ForegroundColor Cyan
Write-Host ""
Write-Info "xbot-cli $tag installed to $InstallPath\$BINARY"
Write-Info "Mode: $selectedMode"
Write-Info "Config: $ConfigPath"

if ($selectedMode -eq "server-client") {
    Write-Host ""
    Write-Host "  Manage the service:" -ForegroundColor Cyan
    Write-Host "    nssm start xbot-server" -ForegroundColor DarkGray
    Write-Host "    nssm stop xbot-server" -ForegroundColor DarkGray
    Write-Host "    nssm restart xbot-server" -ForegroundColor DarkGray
    Write-Host "    nssm status xbot-server" -ForegroundColor DarkGray
    Write-Host "    nssm remove xbot-server confirm" -ForegroundColor DarkGray
    Write-Host ""
    Write-Info "CLI will connect to the configured local server (see $ConfigPath)"
    Write-Info "Use 'xbot-cli' for client, 'xbot-cli serve --config $ConfigPath' for manual server start"
} else {
    Write-Host ""
    Write-Info "Run 'xbot-cli' to start."
}

Write-Host ""
Write-Host "  Project:  https://github.com/$REPO" -ForegroundColor DarkGray
Write-Host "  License:  MIT" -ForegroundColor DarkGray
Write-Host ""
