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

# THE .NET PATH, NOT THE CMDLET.
#
# The published-installer smoke on a clean Windows runner died on
#   The term 'Get-FileHash' is not recognized as the name of a cmdlet
# at the checksum step, having already downloaded the archive through
# Invoke-WebRequest — which lives in the SAME module (Microsoft.PowerShell
# .Utility), so "the module did not load" does not explain it and the real
# cause is still unknown. What is certain is that the .NET classes below need
# no module, no autoloading and no minimum PowerShell beyond 2.0, so they
# cannot fail this way on any machine a user is likely to run.
#
# Kept as a fallback rather than a replacement: where Get-FileHash does work it
# is the better-tested path, and a disagreement between the two would be worth
# knowing about rather than papering over.
function Get-SdnFileHashSha256 {
  param([string]$LiteralPath)

  # Asked for, not assumed, and not inferred from an exception type: whether a
  # missing command raises a catchable terminating error depends on how the
  # script was invoked, and this one runs inside Invoke-Expression. Get-Command
  # is Microsoft.PowerShell.Core, which is present wherever PowerShell is.
  if (Get-Command -Name Get-FileHash -ErrorAction SilentlyContinue) {
    return (Get-FileHash -LiteralPath $LiteralPath -Algorithm SHA256).Hash.ToLowerInvariant()
  }
  Write-Info 'Get-FileHash unavailable; hashing with .NET'

  $stream = [System.IO.File]::OpenRead($LiteralPath)
  try {
    $sha256 = [System.Security.Cryptography.SHA256]::Create()
    try {
      $bytes = $sha256.ComputeHash($stream)
    } finally {
      $sha256.Dispose()
    }
  } finally {
    $stream.Dispose()
  }
  return ([System.BitConverter]::ToString($bytes) -replace '-', '').ToLowerInvariant()
}

# Expand-Archive is Microsoft.PowerShell.Archive and is the NEXT line the
# installer would have reached, so it gets the same treatment before it can
# fail the same way for the same unexplained reason.
function Expand-SdnArchive {
  param(
    [string]$LiteralPath,
    [string]$DestinationPath
  )

  if (Get-Command -Name Expand-Archive -ErrorAction SilentlyContinue) {
    Expand-Archive -LiteralPath $LiteralPath -DestinationPath $DestinationPath -Force
    return
  }
  Write-Info 'Expand-Archive unavailable; extracting with .NET'

  Add-Type -AssemblyName System.IO.Compression.FileSystem
  # ExtractToDirectory refuses an existing destination on PowerShell 5.1, and
  # the installer re-runs into a directory it has already made.
  $archive = [System.IO.Compression.ZipFile]::OpenRead($LiteralPath)
  try {
    foreach ($entry in $archive.Entries) {
      $target = Join-Path $DestinationPath $entry.FullName
      $parent = Split-Path -Parent $target
      if ($parent -and -not (Test-Path -LiteralPath $parent)) {
        New-Item -ItemType Directory -Path $parent -Force | Out-Null
      }
      if ($entry.Name) {
        [System.IO.Compression.ZipFileExtensions]::ExtractToFile($entry, $target, $true)
      }
    }
  } finally {
    $archive.Dispose()
  }
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
  $actual = Get-SdnFileHashSha256 -LiteralPath $archivePath
  if ($expected -ne $actual) {
    Write-Fail "Checksum mismatch. Expected $expected but got $actual"
  }
  Write-Info 'Checksum verified'

  Write-Info "Extracting bundle to $BundleParentDir..."
  New-Item -ItemType Directory -Force -Path $BundleParentDir | Out-Null
  if (Test-Path -LiteralPath $bundleRoot) {
    Remove-Item -LiteralPath $bundleRoot -Recurse -Force
  }
  Expand-SdnArchive -LiteralPath $archivePath -DestinationPath $BundleParentDir

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
