# Public Space Data Network PowerShell installer entrypoint.
#
# This stub used to download scripts/install.ps1 from the repository's MAIN
# branch and Invoke-Expression it, so any push to main ran on every new
# Windows install, and that installer trusted checksums published in the same
# release as the archive (security audit SEC-045). Releases are now trusted
# only through the owner's offline signature over spacedatanetwork-checksums.txt
# (deployment/release/RELEASE-SIGNING.md), which the PowerShell installer does
# not verify yet. Until it does, this entrypoint refuses to install rather than
# run unverified code. The shell installer (install.sh) verifies the signature
# and works under WSL.

$ErrorActionPreference = 'Stop'

throw ('The Space Data Network PowerShell installer is disabled until it verifies signed releases. ' +
  'Use WSL with: curl -fsSL https://spacedatanetwork.org/install.sh | bash. ' +
  'See https://github.com/DigitalArsenal/space-data-network/blob/main/deployment/release/RELEASE-SIGNING.md')
