<#
.SYNOPSIS
    xbot-cli installer for Windows
.DESCRIPTION
    Downloads and installs xbot-cli from GitHub Releases.
.PARAMETER Version
    Specific version to install (defaults to latest release).
.PARAMETER InstallPath
    Installation directory (defaults to $env:USERPROFILE\.local\bin).
.EXAMPLE
    irm https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.ps1 | iex
.EXAMPLE
    .\install.ps1 -Version v0.1.0
.EXAMPLE
    .\install.ps1 -InstallPath C:\Tools
#>

param(
    [string]$Version = "",
    [string]$InstallPath = ""
)

$ErrorActionPreference = "Stop"

$REPO = "CjiW/xbot"
$BINARY = "xbot-cli.exe"

if (-not $InstallPath) {
    $InstallPath = Join-Path $env:USERPROFILE ".local\bin"
}

# --- Detect platform ---
function Get-Platform {
    $arch = $env:PROCESSOR_ARCHITECTURE
    switch ($arch) {
        "AMD64" { return "windows-amd64" }
        "ARM64" { return "windows-arm64" }
        default { Write-Error "Unsupported architecture: $arch. Only AMD64 and ARM64 are supported."; exit 1 }
    }
}

# --- Resolve version ---
function Get-LatestVersion {
    if ($Version) { return $Version }

    try {
        $response = Invoke-RestMethod -Uri "https://api.github.com/repos/$REPO/releases/latest" -Headers @{"User-Agent"="PowerShell"}
        return $response.tag_name
    } catch {
        Write-Error "Failed to determine latest version. Set -Version explicitly, e.g.: .\install.ps1 -Version v0.1.0"
        exit 1
    }
}

# --- Main ---
Write-Host ""
Write-Host "  =======================================" -ForegroundColor Cyan
Write-Host "         xbot-cli Installer (Windows)" -ForegroundColor Cyan
Write-Host "  =======================================" -ForegroundColor Cyan
Write-Host ""

$platform = Get-Platform
$tag = Get-LatestVersion
$downloadUrl = "https://github.com/$REPO/releases/download/$tag/xbot-cli-$platform.exe"

Write-Host "[INFO] Platform:  $platform" -ForegroundColor Green
Write-Host "[INFO] Version:   $tag" -ForegroundColor Green
Write-Host "[INFO] URL:       $downloadUrl" -ForegroundColor Green
Write-Host "[INFO] Install:   $InstallPath\$BINARY" -ForegroundColor Green
Write-Host ""

# Create install directory
if (-not (Test-Path $InstallPath)) {
    New-Item -ItemType Directory -Path $InstallPath -Force | Out-Null
    Write-Host "[INFO] Created directory: $InstallPath" -ForegroundColor Green
}

# Download
Write-Host "[INFO] Downloading..." -ForegroundColor Green
$tmpFile = Join-Path ([System.IO.Path]::GetTempPath()) "xbot-cli-download.exe"

try {
    Invoke-WebRequest -Uri $downloadUrl -OutFile $tmpFile -UseBasicParsing
} catch {
    Write-Error "Download failed: $_"
    exit 1
}

# Verify checksum if possible
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
            Write-Error "Checksum mismatch! Expected: $expectedHash, Got: $actualHash"
            exit 1
        }
        Write-Host "[INFO] Checksum verified" -ForegroundColor Green
    }
    Remove-Item $checksumFile -Force -ErrorAction SilentlyContinue
} catch {
    Write-Host "[WARN] Checksum verification skipped" -ForegroundColor Yellow
}

# Install
Copy-Item $tmpFile (Join-Path $InstallPath $BINARY) -Force
Remove-Item $tmpFile -Force -ErrorAction SilentlyContinue

Write-Host ""
Write-Host "[OK] xbot-cli $tag installed to $InstallPath\$BINARY" -ForegroundColor Green
Write-Host ""

# Add to PATH if not already there
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$InstallPath*") {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$InstallPath", "User")
    $env:Path = "$env:Path;$InstallPath"
    Write-Host "[INFO] Added $InstallPath to user PATH" -ForegroundColor Green
    Write-Host "[INFO] Please restart your terminal for PATH changes to take effect." -ForegroundColor Yellow
}

Write-Host ""
Write-Host "  Run 'xbot-cli' to start." -ForegroundColor Green
Write-Host "  Project:  https://github.com/$REPO" -ForegroundColor DarkGray
Write-Host "  License:  MIT" -ForegroundColor DarkGray
Write-Host ""
