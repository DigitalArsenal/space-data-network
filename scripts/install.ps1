# Space Data Network native Windows installer
# Usage: irm https://spacedatanetwork.org/install.ps1 | iex
#
# Environment variables:
#   SDN_VERSION     Release tag or version to install (default: latest)
#   SDN_INSTALL_DIR Command shim directory (default: $HOME\.spacedatanetwork\bin)
#   SDN_BUNDLE_DIR  Bundle parent directory (default: $HOME\.spacedatanetwork\bundles)
#   SDN_SKIP_INIT   Set to 1 to skip first-run node identity initialization

$ErrorActionPreference = 'Stop'

$Repo = 'DigitalArsenal/space-data-network'
$PrimaryBinaryName = 'spacedatanetwork'
$AliasBinaryName = 'sdn'
$InstallDir = Join-Path $HOME '.spacedatanetwork\bin'
$BundleParentDir = Join-Path $HOME '.spacedatanetwork\bundles'
$TempDir = $null

if ($env:SDN_INSTALL_DIR) {
  $InstallDir = $env:SDN_INSTALL_DIR
}
if ($env:SDN_BUNDLE_DIR) {
  $BundleParentDir = $env:SDN_BUNDLE_DIR
}

function Write-Info {
  param([string]$Message)
  Write-Host "[INFO] $Message" -ForegroundColor Green
}

function Write-Warn {
  param([string]$Message)
  Write-Host "[WARN] $Message" -ForegroundColor Yellow
}

function Write-Fail {
  param([string]$Message)
  Write-Host "[ERROR] $Message" -ForegroundColor Red
  exit 1
}

function Invoke-WebRequestCompat {
  param(
    [string]$Uri,
    [string]$OutFile
  )

  $parameters = @{
    Uri = $Uri
    OutFile = $OutFile
  }
  if ($PSVersionTable.PSVersion.Major -lt 6) {
    $parameters.UseBasicParsing = $true
  }
  Invoke-WebRequest @parameters
}

function Invoke-RestMethodCompat {
  param([string]$Uri)

  $parameters = @{
    Uri = $Uri
  }
  if ($PSVersionTable.PSVersion.Major -lt 6) {
    $parameters.UseBasicParsing = $true
  }
  Invoke-RestMethod @parameters
}

function Get-SdnArch {
  $machine = $env:PROCESSOR_ARCHITEW6432
  if (-not $machine) {
    $machine = $env:PROCESSOR_ARCHITECTURE
  }
  switch ($machine.ToUpperInvariant()) {
    'AMD64' { return 'amd64' }
    'ARM64' { return 'amd64' }
    default { Write-Fail "Unsupported Windows architecture: $machine" }
  }
}

function Select-NodeReleaseTag {
  param([object[]]$Releases)
  # Newest node release (v<digit>... tags, GitHub's newest-first order), never
  # an sdn-js library tag; the prerelease flag is not consulted.
  $node = @($Releases | Where-Object { $_.tag_name -match '^v\d' -and -not $_.draft })
  if ($node.Count -gt 0) { return $node[0].tag_name }
  return $null
}

function Get-SdnVersion {
  if ($env:SDN_VERSION) {
    Write-Info "Using specified version: $env:SDN_VERSION"
    return $env:SDN_VERSION
  }

  Write-Info 'Fetching latest version...'
  # The repository also publishes library releases (sdn-js-v*), which
  # GitHub's latest-release endpoint happily returns; the node's own releases are the
  # v<semver> tags, newest first.
  $releases = @(Invoke-RestMethodCompat "https://api.github.com/repos/$Repo/releases?per_page=50")
  $tag = Select-NodeReleaseTag $releases
  if (-not $tag) {
    Write-Fail "Failed to fetch latest version: no v<semver> node release found among the newest 50 releases of $Repo"
  }
  Write-Info "Latest version: $tag"
  return $tag
}

function Normalize-SdnVersion {
  param([string]$Version)

  if (-not $Version) {
    Write-Fail 'Version is empty'
  }
  if ($Version -notmatch '^[A-Za-z0-9._-]+$') {
    Write-Fail "Unsupported version string: $Version"
  }
  if ($Version.StartsWith('v')) {
    return @{
      ReleaseTag = $Version
      AssetVersion = $Version.Substring(1)
    }
  }
  return @{
    ReleaseTag = "v$Version"
    AssetVersion = $Version
  }
}

function Assert-File {
  param([string]$Path, [string]$Description)

  if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
    Write-Fail "Extracted bundle is missing $Description at $Path"
  }
}

function Write-CmdShim {
  param(
    [string]$ShimPath,
    [string]$TargetPath
  )

  $content = "@echo off`r`n`"$TargetPath`" %*`r`n"
  Set-Content -LiteralPath $ShimPath -Value $content -Encoding ASCII
}

function Ensure-UserPath {
  param([string]$PathToAdd)

  $currentEntries = @()
  if ($env:Path) {
    $currentEntries = $env:Path -split ';' | Where-Object { $_ }
  }
  $alreadyCurrent = $currentEntries | Where-Object { $_.TrimEnd('\') -ieq $PathToAdd.TrimEnd('\') } | Select-Object -First 1
  if (-not $alreadyCurrent) {
    $env:Path = "$PathToAdd;$env:Path"
  }

  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $userEntries = @()
  if ($userPath) {
    $userEntries = $userPath -split ';' | Where-Object { $_ }
  }
  $alreadyUser = $userEntries | Where-Object { $_.TrimEnd('\') -ieq $PathToAdd.TrimEnd('\') } | Select-Object -First 1
  if (-not $alreadyUser) {
    $newUserPath = if ($userPath) { "$PathToAdd;$userPath" } else { $PathToAdd }
    [Environment]::SetEnvironmentVariable('Path', $newUserPath, 'User')
    Write-Info "Added $PathToAdd to the current user's PATH"
  }
}

try {
  Write-Host ''
  Write-Host '===========================================' -ForegroundColor Blue
  Write-Host '     Space Data Network Installer          ' -ForegroundColor Blue
  Write-Host '===========================================' -ForegroundColor Blue
  Write-Host ''

  if (-not $IsWindows -and $PSVersionTable.PSEdition -eq 'Core') {
    Write-Fail 'install.ps1 is for native Windows PowerShell. Use https://spacedatanetwork.org/install.sh on macOS or Linux.'
  }

  $arch = Get-SdnArch
  Write-Info "Detected platform: windows-$arch"

  $version = Get-SdnVersion
  $normalized = Normalize-SdnVersion $version
  $releaseTag = $normalized.ReleaseTag
  $assetVersion = $normalized.AssetVersion
  $bundleName = "spacedatanetwork-$assetVersion-windows-$arch"
  $archiveName = "$bundleName.zip"
  $bundleRoot = Join-Path $BundleParentDir $bundleName

  $TempDir = Join-Path ([System.IO.Path]::GetTempPath()) "sdn-install-$PID-$([guid]::NewGuid().ToString('N'))"
  New-Item -ItemType Directory -Force -Path $TempDir | Out-Null
  $archivePath = Join-Path $TempDir $archiveName
  $checksumsPath = Join-Path $TempDir 'spacedatanetwork-checksums.txt'

  $archiveUrl = "https://github.com/$Repo/releases/download/$releaseTag/$archiveName"
  $checksumsUrl = "https://github.com/$Repo/releases/download/$releaseTag/spacedatanetwork-checksums.txt"

  Write-Info "Downloading from: $archiveUrl"
  Invoke-WebRequestCompat $archiveUrl $archivePath
  if (-not (Test-Path -LiteralPath $archivePath -PathType Leaf) -or (Get-Item -LiteralPath $archivePath).Length -eq 0) {
    Write-Fail 'Download failed'
  }

  Write-Info 'Verifying checksum...'
  Invoke-WebRequestCompat $checksumsUrl $checksumsPath
  $checksumLine = Get-Content -LiteralPath $checksumsPath | Where-Object {
    $parts = $_ -split '\s+', 2
    $parts.Count -eq 2 -and $parts[1] -eq $archiveName
  } | Select-Object -First 1
  if (-not $checksumLine) {
    Write-Fail "Checksum for $archiveName not found in spacedatanetwork-checksums.txt"
  }
  $expected = (($checksumLine -split '\s+')[0]).ToLowerInvariant()
  $actual = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
  if ($expected -ne $actual) {
    Write-Fail "Checksum mismatch. Expected $expected but got $actual"
  }
  Write-Info 'Checksum verified'

  Write-Info "Extracting bundle to $BundleParentDir..."
  New-Item -ItemType Directory -Force -Path $BundleParentDir | Out-Null
  if (Test-Path -LiteralPath $bundleRoot) {
    Remove-Item -LiteralPath $bundleRoot -Recurse -Force
  }
  Expand-Archive -LiteralPath $archivePath -DestinationPath $BundleParentDir -Force

  $PrimaryExe = Join-Path $bundleRoot 'bin\spacedatanetwork.exe'
  $AliasExe = Join-Path $bundleRoot 'bin\sdn.exe'
  Assert-File $PrimaryExe 'spacedatanetwork.exe'
  Assert-File $AliasExe 'sdn.exe'
  Assert-File (Join-Path $bundleRoot 'runtime\modules\org.spacedatanetwork.updater.wasm') 'the SDN updater module'
  Assert-File (Join-Path $bundleRoot 'runtime\modules\hd-wallet-wasi.wasm') 'the SDN HD wallet module'
  Assert-File (Join-Path $bundleRoot 'manifest.json') 'manifest.json'

  Write-Info "Installing command shims into $InstallDir..."
  New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
  Write-CmdShim (Join-Path $InstallDir 'spacedatanetwork.cmd') $PrimaryExe
  Write-CmdShim (Join-Path $InstallDir 'sdn.cmd') $AliasExe
  Ensure-UserPath $InstallDir

  if ($env:SDN_SKIP_INIT -eq '1') {
    Write-Info 'Skipping node identity initialization because SDN_SKIP_INIT=1'
  } else {
    Write-Info 'Initializing local node identity...'
    & $PrimaryExe init
    Write-Info 'Verifying local node identity...'
    & $PrimaryExe show-identity 2>$null | Out-Null
  }

  Write-Info 'Installation successful!'
  & $PrimaryExe version
  & $AliasExe status | Out-Null
  Write-Info "Run '$PrimaryBinaryName start' to start the node as a persistent background service"
  Write-Info "Run '$PrimaryBinaryName daemon' for foreground/manual mode"
  Write-Info "Run '$AliasBinaryName status' to inspect the local node"
  Write-Info 'Documentation: https://spacedatanetwork.org'
  Write-Info "GitHub: https://github.com/$Repo"
} finally {
  if ($TempDir -and (Test-Path -LiteralPath $TempDir)) {
    Remove-Item -LiteralPath $TempDir -Recurse -Force -ErrorAction SilentlyContinue
  }
}
