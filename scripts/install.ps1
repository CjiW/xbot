<#
.SYNOPSIS
    xbot-cli installer for Windows (no admin required)
.DESCRIPTION
    Downloads and installs xbot-cli from GitHub Releases.
    Supports standalone and server-client modes.
    Server installs via Startup folder by default (no admin needed).
    If running as Administrator, offers Scheduled Task or nssm as alternatives.
.PARAMETER Version
    Specific version to install (defaults to latest release).
.PARAMETER InstallPath
    Installation directory (defaults to $env:USERPROFILE\.local\bin).
.PARAMETER Mode
    Install mode: "standalone" (default) or "server-client".
.PARAMETER Port
    Server port for server-client mode (default 8082).
.EXAMPLE
    irm https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.ps1 | iex
.EXAMPLE
    .\install.ps1 -Version v0.1.0
.EXAMPLE
    .\install.ps1 -Mode server-client -Port 9090
#>

param(
    [string]$Version = "",
    [string]$InstallPath = "",
    [string]$Mode = "",
    [int]$Port = 0,
    [switch]$NonInteractive
)

$ErrorActionPreference = "Stop"

$REPO = "ai-pivot/xbot"
$BINARY = "xbot-cli.exe"
$SERVICE_NAME = "xbot-server"
$DEFAULT_PORT = 8082

# Env var fallback for parameters (GitHub Actions uses env vars)
if (-not $Mode)    { $Mode = $env:MODE }
if (-not $Version) { $Version = $env:VERSION }

if (-not $InstallPath) {
    if ($env:INSTALL_PATH) {
        $InstallPath = $env:INSTALL_PATH
    } else {
        $InstallPath = Join-Path $env:USERPROFILE ".local\bin"
    }
}
if ($Port -le 0) { [int]::TryParse($env:PORT, [ref]$Port) | Out-Null }

$XbotHome = if ($env:XBOT_HOME) { $env:XBOT_HOME } else { Join-Path $env:USERPROFILE ".xbot" }
$ConfigPath = Join-Path $XbotHome "config.json"

function Write-Info  { param([string]$Msg) Write-Host "[INFO] $Msg" -ForegroundColor Green }
function Write-Warn  { param([string]$Msg) Write-Host "[WARN] $Msg" -ForegroundColor Yellow }
function Write-Err   { param([string]$Msg) Write-Host "[ERROR] $Msg" -ForegroundColor Red; throw $Msg }

function ConvertTo-Ht {
    param([Parameter(ValueFromPipeline)]$InputObject)
    if ($InputObject -is [System.Collections.IDictionary]) {
        $ht = @{}
        foreach ($key in $InputObject.Keys) { $ht[$key] = ConvertTo-Ht $InputObject[$key] }
        return $ht
    }
    if ($InputObject -is [PSCustomObject]) {
        $ht = @{}
        foreach ($prop in $InputObject.PSObject.Properties) { $ht[$prop.Name] = ConvertTo-Ht $prop.Value }
        return $ht
    }
    if ($InputObject -is [System.Collections.IList]) {
        $list = @()
        foreach ($item in $InputObject) { $list += ConvertTo-Ht $item }
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

function Get-LatestVersion {
    if ($Version) { return $Version }
    try {
        $response = Invoke-RestMethod -Uri "https://api.github.com/repos/$REPO/releases/latest" -Headers @{"User-Agent"="PowerShell"}
        return $response.tag_name
    } catch {
        Write-Err "Failed to determine latest version. Set -Version explicitly."
    }
}

function New-RandomToken {
    $bytes = New-Object byte[] 16
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    return -join ($bytes | ForEach-Object { $_.ToString("x2") })
}

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

function Ask-Port {
    if ($Port -gt 0) { return $Port }
    if ($selectedMode -ne "server-client") { return $DEFAULT_PORT }
    $portInput = Read-Host "Server port [HTTP + WebSocket + Web UI] [$DEFAULT_PORT]"
    if ($portInput -match '^\d+$') { return [int]$portInput }
    return $DEFAULT_PORT
}

function Backup-Config {
    if (Test-Path $ConfigPath) {
        $ts = Get-Date -Format "yyyyMMdd-HHmmss"
        $backup = "$ConfigPath.bak.$ts"
        Copy-Item $ConfigPath $backup -Force
        Write-Info "Backed up existing config to $backup"
    }
}

function Write-Config {
    param([string]$Mode, [int]$Port, [string]$Token)
    if (-not (Test-Path $XbotHome)) {
        New-Item -ItemType Directory -Path $XbotHome -Force | Out-Null
    }
    $cfg = @{}
    if (Test-Path $ConfigPath) {
        try {
            $raw = Get-Content $ConfigPath -Raw -Encoding UTF8
            $cfg = $raw | ConvertFrom-Json | ConvertTo-Ht
        } catch { $cfg = @{} }
    }
    foreach ($section in @("server", "web", "cli", "admin", "agent", "llm")) {
        if (-not $cfg.ContainsKey($section)) { $cfg[$section] = @{} }
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
    Set-IfMissing "agent" "work_dir" $env:USERPROFILE
    Set-IfMissing "llm" "provider" "openai"
    Set-IfMissing "llm" "model" "gpt-4o-mini"
    Set-IfMissing "llm" "api_key" ""
    Set-IfMissing "llm" "base_url" ""
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
    $json = $cfg | ConvertTo-Json -Depth 10
    Set-Content -Path $ConfigPath -Value $json -Encoding UTF8
    foreach ($item in $changes) { Write-Info "Config set: $item" }
    foreach ($item in $preserved) { Write-Warn "Config preserved: $item" }
}

function Test-IsAdmin {
    try {
        $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
        $principal = New-Object Security.Principal.WindowsPrincipal($identity)
        return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    } catch { return $false }
}

# --- Startup folder autostart (no admin required) ---
function Install-UserAutostart {
    param([string]$BinPath, [string]$CfgPath)
    $workDir = $env:USERPROFILE

    # Create wrapper batch in .xbot\scripts\
    $wrapperDir = Join-Path $XbotHome "scripts"
    if (-not (Test-Path $wrapperDir)) {
        New-Item -ItemType Directory -Path $wrapperDir -Force | Out-Null
    }
    $wrapperBat = Join-Path $wrapperDir "run-server.bat"
    Set-Content -Path $wrapperBat -Value "@echo off`r`nset XBOT_HOME=$XbotHome`r`ncd /d `"$workDir`"`r`n`"$BinPath`" serve --config `"$CfgPath`"" -Encoding ASCII

    # Create VBS launcher (runs batch hidden, no console window flash)
    $vbsLauncher = Join-Path $wrapperDir "start-xbot-hidden.vbs"
    Set-Content -Path $vbsLauncher -Value "Set WshShell = CreateObject(`"WScript.Shell`")`r`nWshShell.Run chr(34) & `"$wrapperBat`" & Chr(34), 0, False" -Encoding ASCII

    # Place launcher in user's Startup folder (auto-runs at login, no admin needed)
    $startupFolder = [Environment]::GetFolderPath("Startup")
    $useShortcut = $false
    try {
        $shortcutPath = Join-Path $startupFolder "xbot-server.lnk"
        $shell = New-Object -ComObject WScript.Shell
        $shortcut = $shell.CreateShortcut($shortcutPath)
        $shortcut.TargetPath = "wscript.exe"
        $shortcut.Arguments = "`"$vbsLauncher`""
        $shortcut.WorkingDirectory = $wrapperDir
        $shortcut.Description = "xbot AI Agent Server"
        $shortcut.WindowStyle = 7  # Minimized
        $shortcut.Save()
        $useShortcut = $true
        Write-Info "Autostart shortcut created in Startup folder (no admin needed)"
    } catch {
        Write-Warn "COM shortcut creation failed, using batch file in Startup folder"
    }

    # Fallback: copy batch file directly to Startup folder (shows console window briefly)
    if (-not $useShortcut) {
        $startupBat = Join-Path $startupFolder "xbot-server.bat"
        Copy-Item $wrapperBat $startupBat -Force
        Write-Info "Autostart batch copied to Startup folder (no admin needed)"
    }

    # Remove any leftover scheduled task from previous installs
    Unregister-ScheduledTask -TaskName "xbot-server" -Confirm:$false -ErrorAction SilentlyContinue

    if (-not $NonInteractive) {
        try {
            Start-Process -FilePath "wscript.exe" -ArgumentList "`"$vbsLauncher`"" -WindowStyle Hidden
            Write-Info "Server started (background)"
        } catch {
            Write-Warn "Could not auto-start. It will start at next login."
        }
    } else {
        Write-Info "NONINTERACTIVE: skipped auto-start"
    }
}

# --- Scheduled Task (requires admin or relaxed policy) ---
function Install-ScheduledTask {
    param([string]$BinPath, [string]$CfgPath)
    $taskName = "xbot-server"
    $workDir = $env:USERPROFILE

    # Remove old Startup folder shortcut if migrating to Scheduled Task
    $startupFolder = [Environment]::GetFolderPath("Startup")
    $oldShortcut = Join-Path $startupFolder "xbot-server.lnk"
    if (Test-Path $oldShortcut) { Remove-Item $oldShortcut -Force -ErrorAction SilentlyContinue }

    $wrapperDir = Join-Path $XbotHome "scripts"
    if (-not (Test-Path $wrapperDir)) {
        New-Item -ItemType Directory -Path $wrapperDir -Force | Out-Null
    }
    $wrapperScript = Join-Path $wrapperDir "run-server.bat"
    Set-Content -Path $wrapperScript -Value "@echo off`r`nset XBOT_HOME=$XbotHome`r`ncd /d `"$workDir`"`r`n`"$BinPath`" serve --config `"$CfgPath`"" -Encoding ASCII

    $taskCreated = $false
    try {
        $action = New-ScheduledTaskAction -Execute $wrapperScript -WorkingDirectory $workDir
        $trigger = New-ScheduledTaskTrigger -AtLogOn
        $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
        Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Settings $settings -Description "xbot AI Agent Server" -Force
        Write-Info "Scheduled Task '$taskName' registered"
        $taskCreated = $true
    } catch {
        # Fallback to schtasks.exe
        schtasks.exe /Create /SC ONLOGON /TN $taskName /TR "`"$wrapperScript`"" /F 2>$null
        if ($LASTEXITCODE -eq 0) {
            Write-Info "Scheduled Task '$taskName' created (via schtasks.exe)"
            $taskCreated = $true
        }
    }
    if (-not $taskCreated) {
        return $false
    }
    if (-not $NonInteractive) {
        try {
            Start-ScheduledTask -TaskName $taskName
            Write-Info "Server started"
        } catch {
            Write-Warn "Could not auto-start. It will start at next logon."
        }
    } else {
        Write-Info "NONINTERACTIVE: skipped auto-start"
    }
    return $true
}

function Ensure-Nssm {
    $nssmExe = (Get-Command nssm -ErrorAction SilentlyContinue).Source
    if ($nssmExe -and (Test-Path $nssmExe)) { return $nssmExe }
    $commonPaths = @(
        (Join-Path $env:ProgramFiles "NSSM\nssm.exe"),
        (Join-Path ${env:ProgramFiles(x86)} "NSSM\nssm.exe"),
        (Join-Path $InstallPath "nssm.exe")
    )
    foreach ($p in $commonPaths) {
        if (Test-Path $p) { return $p }
    }
    Write-Info "Downloading nssm..."
    $nssmZip = Join-Path $env:TEMP "nssm.zip"
    $nssmDir = Join-Path $env:TEMP "nssm"
    try {
        Invoke-WebRequest -Uri "https://nssm.cc/release/nssm-2.24.zip" -OutFile $nssmZip -UseBasicParsing
        if (Test-Path $nssmDir) { Remove-Item $nssmDir -Recurse -Force }
        Expand-Archive -Path $nssmZip -DestinationPath $nssmDir -Force
        $nssmBin = Join-Path $nssmDir "nssm-2.24\win64\nssm.exe"
        if (-not (Test-Path $nssmBin)) { $nssmBin = Join-Path $nssmDir "nssm-2.24\win32\nssm.exe" }
        if (-not (Test-Path $nssmBin)) { Write-Err "Failed to extract nssm" }
        $dest = Join-Path $InstallPath "nssm.exe"
        Copy-Item $nssmBin $dest -Force
        return $dest
    } catch {
        Write-Warn "Failed to download nssm: $_"
        return $null
    } finally {
        Remove-Item $nssmZip -Force -ErrorAction SilentlyContinue
        Remove-Item $nssmDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Install-ServiceNssm {
    param([string]$NssmPath, [string]$BinPath, [string]$CfgPath)
    $workDir = $env:USERPROFILE
    & $NssmPath stop $SERVICE_NAME 2>$null
    & $NssmPath remove $SERVICE_NAME confirm 2>$null
    & $NssmPath install $SERVICE_NAME $BinPath "serve --config $CfgPath"
    if ($LASTEXITCODE -ne 0) { Write-Err "nssm install failed" }
    & $NssmPath set $SERVICE_NAME AppDirectory $workDir
    & $NssmPath set $SERVICE_NAME DisplayName "xbot Agent Server"
    & $NssmPath set $SERVICE_NAME Description "xbot AI Agent Server"
    & $NssmPath set $SERVICE_NAME Start SERVICE_AUTO_START
    & $NssmPath set $SERVICE_NAME AppEnvironmentExtra "XBOT_HOME=$XbotHome"
    $logDir = Join-Path $XbotHome "logs"
    if (-not (Test-Path $logDir)) { New-Item -ItemType Directory -Path $logDir -Force | Out-Null }
    & $NssmPath set $SERVICE_NAME AppStdout (Join-Path $logDir "xbot-server.log")
    & $NssmPath set $SERVICE_NAME AppStderr (Join-Path $logDir "xbot-server.err")
    & $NssmPath set $SERVICE_NAME AppRotateFiles 1
    & $NssmPath set $SERVICE_NAME AppRotateBytes 10485760
    & $NssmPath start $SERVICE_NAME
    if ($LASTEXITCODE -eq 0) {
        Write-Info "Windows service '$SERVICE_NAME' installed and started (nssm)"
    } else {
        Write-Warn "Service installed but failed to start. Check $logDir"
    }
}

function Install-WindowsService {
    param([string]$BinPath, [string]$CfgPath)

    # Non-admin: always use Startup folder (guaranteed no-elevated-privilege method)
    if (-not (Test-IsAdmin)) {
        Install-UserAutostart -BinPath $BinPath -CfgPath $CfgPath
        return
    }

    # Admin + NonInteractive: Scheduled Task (works when admin)
    if ($NonInteractive) {
        $ok = Install-ScheduledTask -BinPath $BinPath -CfgPath $CfgPath
        if (-not $ok) {
            Write-Warn "Scheduled Task failed, falling back to Startup folder..."
            Install-UserAutostart -BinPath $BinPath -CfgPath $CfgPath
        }
        return
    }

    # Admin + Interactive: offer full choice
    Write-Host ""
    Write-Host "Choose service method:" -ForegroundColor Cyan
    Write-Host "  1) Scheduled Task (recommended) - Starts at logon, robust restart" -ForegroundColor Cyan
    Write-Host "  2) nssm service               - Real Windows service, auto-start at boot" -ForegroundColor Cyan
    Write-Host "  3) Startup folder             - No special permissions needed" -ForegroundColor Cyan
    Write-Host "  4) Skip" -ForegroundColor Cyan
    $svcChoice = Read-Host "Select [1/2/3/4] (default 1)"
    switch ($svcChoice) {
        "2" {
            $nssmPath = Ensure-Nssm
            if ($nssmPath) {
                Install-ServiceNssm -NssmPath $nssmPath -BinPath $BinPath -CfgPath $CfgPath
            } else {
                Write-Warn "nssm not available, falling back to Startup folder"
                Install-UserAutostart -BinPath $BinPath -CfgPath $CfgPath
            }
            return
        }
        "3" {
            Install-UserAutostart -BinPath $BinPath -CfgPath $CfgPath
            return
        }
        "4" {
            Write-Info "Skipping service install. Start manually: $BinPath serve --config $CfgPath"
            return
        }
        default {
            # Try Scheduled Task first; fall back to Startup folder if access denied
            $ok = Install-ScheduledTask -BinPath $BinPath -CfgPath $CfgPath
            if (-not $ok) {
                Write-Warn "Scheduled Task failed (access denied). Falling back to Startup folder..."
                Install-UserAutostart -BinPath $BinPath -CfgPath $CfgPath
            }
            return
        }
    }
}

# ============================================================
# Main
# ============================================================

try {
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

$selectedMode = Ask-Mode
$selectedPort = Ask-Port

if ($selectedMode -eq "server-client") {
    Write-Info "Mode:      server-client (port $selectedPort)"
} else {
    Write-Info "Mode:      standalone"
}

if (-not (Test-Path $InstallPath)) {
    New-Item -ItemType Directory -Path $InstallPath -Force | Out-Null
    Write-Info "Created directory: $InstallPath"
}

Write-Info "Downloading..."
$tmpFile = Join-Path ([System.IO.Path]::GetTempPath()) "xbot-cli-download.exe"
try {
    Invoke-WebRequest -Uri $downloadUrl -OutFile $tmpFile -UseBasicParsing
} catch {
    Write-Err "Download failed: $_"
}

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

# Stop running xbot-cli processes and scheduled task before overwriting the binary
$binPath = Join-Path $InstallPath $BINARY
if (Test-Path $binPath) {
    Write-Info "Checking for running xbot-cli..."
    # Stop and disable scheduled task to prevent auto-restart
    try {
        Stop-ScheduledTask -TaskName "xbot-server" -ErrorAction SilentlyContinue
        Disable-ScheduledTask -TaskName "xbot-server" -ErrorAction SilentlyContinue
    } catch {}
    # Kill ALL xbot-cli processes (by full path match for accuracy)
    $procs = Get-Process -Name "xbot-cli" -ErrorAction SilentlyContinue
    if ($procs) {
        Write-Info "Stopping running xbot-cli process(es)..."
        $procs | Stop-Process -Force -ErrorAction SilentlyContinue
        # Wait up to 5 seconds for process to fully exit and release file handles
        $waited = 0
        while ((Get-Process -Name "xbot-cli" -ErrorAction SilentlyContinue) -and ($waited -lt 5000)) {
            Start-Sleep -Milliseconds 500
            $waited += 500
        }
    }
}

# Copy with retry — Windows may hold the file handle briefly after process exit
$copied = $false
for ($attempt = 1; $attempt -le 5; $attempt++) {
    try {
        Copy-Item $tmpFile $binPath -Force -ErrorAction Stop
        $copied = $true
        break
    } catch {
        if ($attempt -lt 5) {
            Write-Warn "File locked, retrying ($attempt/5)..."
            Start-Sleep -Seconds 2
        }
    }
}
if (-not $copied) {
    # Last resort: rename old file and copy new one
    Write-Warn "File still locked, renaming old binary..."
    $oldFile = "$binPath.old"
    Move-Item $binPath $oldFile -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 1
    try {
        Copy-Item $tmpFile $binPath -Force -ErrorAction Stop
        Remove-Item $oldFile -Force -ErrorAction SilentlyContinue
    } catch {
        # Put old file back if copy still fails
        if (Test-Path $oldFile) { Move-Item $oldFile $binPath -Force -ErrorAction SilentlyContinue }
        throw "Cannot replace xbot-cli.exe after 5 retries: $_"
    }
}
Remove-Item $tmpFile -Force -ErrorAction SilentlyContinue

Write-Host ""
Write-Host "[OK] xbot-cli $tag installed to $InstallPath\$BINARY" -ForegroundColor Green

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$InstallPath*") {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$InstallPath", "User")
    $env:Path = "$env:Path;$InstallPath"
    Write-Info "Added $InstallPath to user PATH"
    Write-Warn "Please restart your terminal for PATH changes to take effect."
}

$token = New-RandomToken
Backup-Config
Write-Config -Mode $selectedMode -Port $selectedPort -Token $token

if ($selectedMode -eq "server-client") {
    $binFullPath = Join-Path $InstallPath $BINARY
    Install-WindowsService -BinPath $binFullPath -CfgPath $ConfigPath
}

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
    if (Test-IsAdmin) {
        Write-Host "  Manage the server:" -ForegroundColor Cyan
        Write-Host "    Stop:   schtasks.exe /End /TN xbot-server  OR  net stop xbot-server" -ForegroundColor DarkGray
        Write-Host "    Start:  Start-ScheduledTask -TaskName xbot-server  OR  net start xbot-server" -ForegroundColor DarkGray
        Write-Host "    Remove: Unregister-ScheduledTask -TaskName xbot-server" -ForegroundColor DarkGray
    } else {
        Write-Host "  Manage the server:" -ForegroundColor Cyan
        Write-Host "    Stop:   Task Manager > xbot-cli.exe > End task" -ForegroundColor DarkGray
        $vbsPath = Join-Path $XbotHome "scripts\start-xbot-hidden.vbs"
        Write-Host "    Start:  wscript `"$vbsPath`"" -ForegroundColor DarkGray
        $startupFolder = [Environment]::GetFolderPath("Startup")
        Write-Host "    Remove: del `"$startupFolder\xbot-server.lnk`"" -ForegroundColor DarkGray
    }
    Write-Host ""
    Write-Info "Web UI: http://localhost:$selectedPort"
    Write-Info "Logs: $XbotHome\logs\xbot-server.log"
} else {
    Write-Host ""
    Write-Info "Run 'xbot-cli' to start."
}

Write-Host ""
Write-Host "  Project:  https://github.com/$REPO" -ForegroundColor DarkGray
Write-Host "  License:  MIT" -ForegroundColor DarkGray
Write-Host ""

} catch {
    Write-Host ""
    Write-Host "[ERROR] Installation failed: $_" -ForegroundColor Red
    Write-Host ""
}

# Keep terminal open so user can read the output
if (-not $NonInteractive) {
    Write-Host "Press Enter to close..." -ForegroundColor DarkGray
    Read-Host | Out-Null
}

# Ensure clean exit code (native exe calls like schtasks.exe may leave $LASTEXITCODE non-zero)
$global:LASTEXITCODE = 0
exit 0
