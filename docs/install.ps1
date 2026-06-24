# Public Space Data Network PowerShell installer entrypoint.

$ErrorActionPreference = 'Stop'

$InstallerUrl = if ($env:SDN_INSTALLER_URL) {
  $env:SDN_INSTALLER_URL
} else {
  'https://raw.githubusercontent.com/DigitalArsenal/space-data-network/main/scripts/install.ps1'
}

$parameters = @{
  Uri = $InstallerUrl
}
if ($PSVersionTable.PSVersion.Major -lt 6) {
  $parameters.UseBasicParsing = $true
}

$installer = (Invoke-WebRequest @parameters).Content
Invoke-Expression $installer
