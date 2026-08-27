& {

$ErrorActionPreference = "Stop"

try {
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {
  # Newer runtimes select secure TLS defaults themselves.
}

$Repository = "talentohq/talento-cli"
$InstallDir = if ($env:TALENTO_INSTALL_DIR) { $env:TALENTO_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Talento\bin" }
$Version = $env:TALENTO_VERSION
$Channel = if ($env:TALENTO_CHANNEL) { $env:TALENTO_CHANNEL.ToLowerInvariant() } else { "auto" }
$ExpectedWindowsPublisher = '__TALENTO_WINDOWS_AUTHENTICODE_PUBLISHER__'
$MaximumDownloadBytes = 268435456
$MaximumBinaryBytes = 268435456

function Invoke-TalentoWebRequest {
  param([string]$Uri, [string]$OutFile)

  if (-not $Uri.StartsWith('https://', [StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing non-HTTPS download URL: $Uri"
  }
  $LastError = $null
  for ($Attempt = 1; $Attempt -le 3; $Attempt++) {
    try {
      $Arguments = @{
        Uri = $Uri
        UseBasicParsing = $true
        ErrorAction = 'Stop'
        Headers = @{ 'User-Agent' = 'talento-installer' }
        MaximumRedirection = 5
        TimeoutSec = 300
      }
      if ($OutFile) { $Arguments.OutFile = $OutFile }
      $Response = Invoke-WebRequest @Arguments
      $FinalUri = $null
      if ($Response -and $Response.BaseResponse -and $Response.BaseResponse.ResponseUri) {
        $FinalUri = $Response.BaseResponse.ResponseUri
      }
      if ($FinalUri -and $FinalUri.Scheme -ne 'https') {
        throw "Refusing redirect to non-HTTPS URL: $FinalUri"
      }
      if ($OutFile) {
        $Length = (Get-Item -LiteralPath $OutFile).Length
        if ($Length -gt $MaximumDownloadBytes) {
          throw "Downloaded file exceeds the size limit"
        }
      }
      return $Response
    } catch {
      $LastError = $_
      if ($OutFile) { Remove-Item -LiteralPath $OutFile -Force -ErrorAction SilentlyContinue }
      if ($Attempt -lt 3) { Start-Sleep -Seconds $Attempt }
    }
  }
  throw "Download failed after 3 attempts: $Uri ($($LastError.Exception.Message))"
}

function Get-TalentoReleases {
  $Response = Invoke-TalentoWebRequest -Uri "https://api.github.com/repos/$Repository/releases?per_page=100"
  return $Response.Content | ConvertFrom-Json
}

function Save-TalentoDownload([string]$Uri, [string]$Destination) {
  Invoke-TalentoWebRequest -Uri $Uri -OutFile $Destination | Out-Null
}

function ConvertFrom-TalentoReleaseTag {
  param([string]$Tag)

  if (-not $Tag -or -not $Tag.StartsWith("v")) { return $null }
  $Raw = $Tag.Substring(1)
  $BuildParts = $Raw.Split("+")
  if ($BuildParts.Count -gt 2) { return $null }
  if ($BuildParts.Count -eq 2) {
    if (-not $BuildParts[1]) { return $null }
    foreach ($Identifier in $BuildParts[1].Split(".")) {
      if (-not $Identifier -or $Identifier -notmatch '^[0-9A-Za-z-]+$') { return $null }
    }
  }

  $VersionAndPrerelease = $BuildParts[0]
  $Dash = $VersionAndPrerelease.IndexOf("-")
  $Core = if ($Dash -ge 0) { $VersionAndPrerelease.Substring(0, $Dash) } else { $VersionAndPrerelease }
  $Prerelease = if ($Dash -ge 0) { $VersionAndPrerelease.Substring($Dash + 1) } else { "" }
  $CoreParts = $Core.Split(".")
  if ($CoreParts.Count -ne 3) { return $null }
  foreach ($Part in $CoreParts) {
    if ($Part -notmatch '^[0-9]+$' -or ($Part.Length -gt 1 -and $Part.StartsWith("0"))) { return $null }
  }
  $PrereleaseParts = @()
  if ($Dash -ge 0) {
    if (-not $Prerelease) { return $null }
    $PrereleaseParts = @($Prerelease.Split("."))
    foreach ($Identifier in $PrereleaseParts) {
      if (-not $Identifier -or $Identifier -notmatch '^[0-9A-Za-z-]+$') { return $null }
      if ($Identifier -match '^[0-9]+$' -and $Identifier.Length -gt 1 -and $Identifier.StartsWith("0")) { return $null }
    }
  }
  return [pscustomobject]@{
    Tag = $Tag
    Major = $CoreParts[0]
    Minor = $CoreParts[1]
    Patch = $CoreParts[2]
    Prerelease = $PrereleaseParts
  }
}

function Compare-TalentoNumericIdentifier {
  param([string]$Left, [string]$Right)
  if ($Left.Length -lt $Right.Length) { return -1 }
  if ($Left.Length -gt $Right.Length) { return 1 }
  return [Math]::Sign([string]::CompareOrdinal($Left, $Right))
}

function Compare-TalentoSemVer {
  param($Left, $Right)

  foreach ($Property in @("Major", "Minor", "Patch")) {
    $Comparison = Compare-TalentoNumericIdentifier $Left.$Property $Right.$Property
    if ($Comparison -ne 0) { return $Comparison }
  }
  if ($Left.Prerelease.Count -eq 0 -and $Right.Prerelease.Count -eq 0) { return 0 }
  if ($Left.Prerelease.Count -eq 0) { return 1 }
  if ($Right.Prerelease.Count -eq 0) { return -1 }
  $Limit = [Math]::Min($Left.Prerelease.Count, $Right.Prerelease.Count)
  for ($Index = 0; $Index -lt $Limit; $Index++) {
    $LeftIdentifier = $Left.Prerelease[$Index]
    $RightIdentifier = $Right.Prerelease[$Index]
    if ($LeftIdentifier -ceq $RightIdentifier) { continue }
    $LeftNumeric = $LeftIdentifier -match '^[0-9]+$'
    $RightNumeric = $RightIdentifier -match '^[0-9]+$'
    if ($LeftNumeric -and $RightNumeric) { return Compare-TalentoNumericIdentifier $LeftIdentifier $RightIdentifier }
    if ($LeftNumeric) { return -1 }
    if ($RightNumeric) { return 1 }
    return [Math]::Sign([string]::CompareOrdinal($LeftIdentifier, $RightIdentifier))
  }
  return [Math]::Sign($Left.Prerelease.Count - $Right.Prerelease.Count)
}

if (-not $Version) {
  if ($Channel -notin @("auto", "preview", "stable")) { throw "TALENTO_CHANNEL must be auto, preview, or stable" }
  $Releases = Get-TalentoReleases
  $BestStable = $null
  $BestPreview = $null
  foreach ($Release in $Releases) {
    if ($Release.draft) { continue }
    $Candidate = ConvertFrom-TalentoReleaseTag $Release.tag_name
    if ($null -eq $Candidate) { continue }
    $IsStable = -not $Release.prerelease -and $Candidate.Prerelease.Count -eq 0 -and $Candidate.Major -ne "0"
    $IsPreview = [bool]$Release.prerelease
    if ($IsStable -and ($null -eq $BestStable -or (Compare-TalentoSemVer $Candidate $BestStable) -gt 0)) { $BestStable = $Candidate }
    if ($IsPreview -and ($null -eq $BestPreview -or (Compare-TalentoSemVer $Candidate $BestPreview) -gt 0)) { $BestPreview = $Candidate }
  }
  $Selected = if ($Channel -eq "stable") { $BestStable } elseif ($Channel -eq "preview") { $BestPreview } elseif ($null -ne $BestStable) { $BestStable } else { $BestPreview }
  if ($null -eq $Selected) { throw "No compatible $Channel Talento CLI release is available" }
  $Version = $Selected.Tag.Substring(1)
}
$Version = $Version -replace '^v', ''
$ParsedVersion = ConvertFrom-TalentoReleaseTag "v$Version"
if ($null -eq $ParsedVersion) { throw "TALENTO_VERSION must be a valid semantic version" }
$IsStable = $ParsedVersion.Prerelease.Count -eq 0 -and $ParsedVersion.Major -ne '0'

$MachineArchitecture = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
$Arch = switch -Regex ($MachineArchitecture) {
  '^(AMD64|x86_64)$' { 'amd64' }
  '^ARM64$' { 'arm64' }
  default { throw "Unsupported Windows architecture: $MachineArchitecture" }
}
$Asset = "talento_${Version}_windows_${Arch}.zip"
$Base = "https://github.com/$Repository/releases/download/v$Version"
$Work = Join-Path ([System.IO.Path]::GetTempPath()) ("talento-" + [System.Guid]::NewGuid())
New-Item -ItemType Directory -Path $Work | Out-Null

function Assert-TalentoSigstoreIdentity([string]$BaseUrl, [string]$WorkDir) {
  if (-not (Get-Command cosign -ErrorAction SilentlyContinue)) {
    throw "cosign is required to verify Talento release identity; install cosign and retry"
  }
  $Bundle = Join-Path $WorkDir 'checksums.txt.sigstore.json'
  $Checksums = Join-Path $WorkDir 'checksums.txt'
  Save-TalentoDownload "$BaseUrl/checksums.txt.sigstore.json" $Bundle
  $Identity = "https://github.com/talentohq/talento-cli/.github/workflows/release.yml@refs/tags/v$Version"
  & cosign verify-blob --certificate-identity $Identity --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' --bundle $Bundle $Checksums
  if ($LASTEXITCODE -ne 0) { throw "Sigstore verification failed for checksums.txt" }
}

function Get-TalentoExpectedChecksum([string]$ChecksumsPath, [string]$ArchiveName) {
  $MatchesForAsset = @()
  foreach ($ChecksumLine in Get-Content -LiteralPath $ChecksumsPath) {
    if ($ChecksumLine -match '^(?<hash>[0-9a-fA-F]{64})\s+\*?(?<name>.+)$' -and $Matches.name -ceq $ArchiveName) {
      $MatchesForAsset += $Matches.hash.ToLowerInvariant()
    }
  }
  if ($MatchesForAsset.Count -ne 1) {
    throw "Release checksum must contain exactly one entry for $ArchiveName"
  }
  return $MatchesForAsset[0]
}

function Expand-TalentoCandidate([string]$ArchivePath, [string]$CandidatePath) {
  Add-Type -AssemblyName System.IO.Compression.FileSystem
  $Archive = [IO.Compression.ZipFile]::OpenRead($ArchivePath)
  try {
    $BinaryEntries = @()
    foreach ($Entry in $Archive.Entries) {
      $Name = $Entry.FullName
      if (-not $Name -or $Name.StartsWith('/') -or $Name.StartsWith('\') -or $Name.Contains('\') -or $Name -match '^[A-Za-z]:') {
        throw "Release archive contains an unsafe path: $Name"
      }
      foreach ($Segment in $Name.Split('/')) {
        if ($Segment -eq '..') { throw "Release archive contains an unsafe path: $Name" }
      }
      if ($Name -ceq 'talento.exe') { $BinaryEntries += $Entry }
    }
    if ($BinaryEntries.Count -ne 1) {
      throw "Release archive must contain exactly one top-level talento.exe"
    }
    $BinaryEntry = $BinaryEntries[0]
    $UnixFileType = (($BinaryEntry.ExternalAttributes -shr 16) -band 0xF000)
    if ($UnixFileType -eq 0xA000 -or $BinaryEntry.FullName.EndsWith('/')) {
      throw "The talento.exe archive entry must be a regular file, not a link or directory"
    }
    if ($BinaryEntry.Length -le 0 -or $BinaryEntry.Length -gt $MaximumBinaryBytes) {
      throw "The talento.exe archive entry is empty or exceeds the size limit"
    }
    $InputStream = $BinaryEntry.Open()
    try {
      $OutputStream = [IO.File]::Open($CandidatePath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
      try { $InputStream.CopyTo($OutputStream) } finally { $OutputStream.Dispose() }
    } finally {
      $InputStream.Dispose()
    }
  } finally {
    $Archive.Dispose()
  }
}

function Assert-TalentoVersion([string]$Path) {
  $Output = @(& $Path --agent version)
  if ($LASTEXITCODE -ne 0) { throw "Executable $Path failed its version command with exit code $LASTEXITCODE" }
  try { $Reported = (($Output -join "`n") | ConvertFrom-Json).version } catch { throw "Executable $Path returned invalid version JSON" }
  if ($Reported -cne $Version) { throw "Executable $Path reports version '$Reported', expected '$Version'" }
}

function Assert-TalentoAuthenticode([string]$Path) {
  if (-not $IsStable) { return }
  if (-not $ExpectedWindowsPublisher -or $ExpectedWindowsPublisher.StartsWith('__TALENTO_')) {
    throw "This stable installer has no expected Authenticode publisher policy; use a release-stamped install.ps1"
  }
  $Signature = Get-AuthenticodeSignature -FilePath $Path -ErrorAction Stop
  if ($Signature.Status -ne 'Valid') { throw "Authenticode signature is not valid: $($Signature.Status)" }
  if (-not $Signature.SignerCertificate -or $Signature.SignerCertificate.Subject -cne $ExpectedWindowsPublisher) {
    $ActualPublisher = if ($Signature.SignerCertificate) { $Signature.SignerCertificate.Subject } else { '<none>' }
    throw "Authenticode publisher '$ActualPublisher' does not match the expected publisher '$ExpectedWindowsPublisher'"
  }
}

function Install-TalentoTransactional([string]$Candidate, [string]$Target) {
  New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Target) | Out-Null
  $Existing = Get-Item -LiteralPath $Target -Force -ErrorAction SilentlyContinue
  if ($null -ne $Existing) {
    if ($Existing.PSIsContainer -or ($Existing.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
      throw "Refusing to replace non-file or reparse-point installation target $Target"
    }
  }
  $Directory = Split-Path -Parent $Target
  $Stage = Join-Path $Directory ('.talento-install-' + [Guid]::NewGuid().ToString('N') + '.exe')
  $Backup = Join-Path $Directory ('.talento-rollback-' + [Guid]::NewGuid().ToString('N') + '.exe')
  $TransactionStarted = $false
  $HadOriginal = $null -ne $Existing
  $Succeeded = $false
  try {
    Copy-Item -LiteralPath $Candidate -Destination $Stage
    Assert-TalentoVersion $Stage
    Assert-TalentoAuthenticode $Stage
    if ($HadOriginal) { Move-Item -LiteralPath $Target -Destination $Backup }
    $TransactionStarted = $true
    Move-Item -LiteralPath $Stage -Destination $Target
    Assert-TalentoVersion $Target
    Assert-TalentoAuthenticode $Target
    $Succeeded = $true
  } catch {
    $InstallError = $_
    if ($TransactionStarted) {
      Remove-Item -LiteralPath $Target -Force -ErrorAction SilentlyContinue
      if ($HadOriginal -and (Test-Path -LiteralPath $Backup)) {
        try {
          Move-Item -LiteralPath $Backup -Destination $Target -ErrorAction Stop
        } catch {
          throw "Installation failed and automatic rollback failed; the previous executable remains at $Backup. Original error: $($InstallError.Exception.Message)"
        }
      }
    }
    throw "Installation failed; the previous executable was preserved or restored. $($InstallError.Exception.Message)"
  } finally {
    Remove-Item -LiteralPath $Stage -Force -ErrorAction SilentlyContinue
    if ($Succeeded) { Remove-Item -LiteralPath $Backup -Force -ErrorAction SilentlyContinue }
  }
}

function Test-TalentoPathContains([string]$Directory) {
  $Expected = [IO.Path]::GetFullPath($Directory).TrimEnd('\')
  foreach ($Entry in @($env:Path -split ';')) {
    if (-not $Entry) { continue }
    try { $Candidate = [IO.Path]::GetFullPath($Entry.Trim()).TrimEnd('\') } catch { continue }
    if ($Candidate -ieq $Expected) { return $true }
  }
  return $false
}

try {
  $ArchivePath = Join-Path $Work $Asset
  $ChecksumsPath = Join-Path $Work 'checksums.txt'
  $CandidatePath = Join-Path $Work 'talento.exe'
  Save-TalentoDownload "$Base/$Asset" $ArchivePath
  Save-TalentoDownload "$Base/checksums.txt" $ChecksumsPath
  Assert-TalentoSigstoreIdentity $Base $Work
  $Expected = Get-TalentoExpectedChecksum $ChecksumsPath $Asset
  $Actual = (Get-FileHash -Algorithm SHA256 (Join-Path $Work $Asset)).Hash.ToLowerInvariant()
  if ($Expected -cne $Actual) { throw "Checksum verification failed for $Asset" }

  Expand-TalentoCandidate $ArchivePath $CandidatePath
  Assert-TalentoVersion $CandidatePath
  Assert-TalentoAuthenticode $CandidatePath
  $Target = Join-Path $InstallDir 'talento.exe'
  Install-TalentoTransactional $CandidatePath $Target
  Write-Host "Installed Talento CLI $Version from release v$Version at $Target"
  if (-not (Test-TalentoPathContains $InstallDir)) {
    Write-Host "Add $InstallDir to PATH to run talento from a new shell."
  }
} finally {
  Remove-Item -Recurse -Force $Work -ErrorAction SilentlyContinue
}

}
