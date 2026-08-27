$ErrorActionPreference = 'Stop'

$Root = Split-Path -Parent $PSScriptRoot
$InstallerPath = Join-Path $Root 'install.ps1'
$Work = Join-Path ([IO.Path]::GetTempPath()) ('talento-installer-tests-' + [Guid]::NewGuid().ToString('N'))
$Fixture = Join-Path $Work 'fixture'
$StubDir = Join-Path $Work 'stubs'
$InstallDir = Join-Path $Work 'bin'
$Publisher = 'CN=TalentoHQ Test, O=TalentoHQ Test'
New-Item -ItemType Directory -Path $Fixture, $StubDir, $InstallDir | Out-Null

function Assert-True([bool]$Condition, [string]$Message) {
  if (-not $Condition) { throw "installer test: $Message" }
}

function Build-FixtureBinary([string]$Version, [string]$OutputPath) {
  $SourcePath = Join-Path $Work ([IO.Path]::GetFileNameWithoutExtension($OutputPath) + '.go')
  $Source = @'
package main

import (
  "encoding/json"
  "os"
  "path/filepath"
)

func main() {
  if os.Getenv("TALENTO_TEST_FAIL_INSTALLED") == "1" && filepath.Base(os.Args[0]) == "talento.exe" {
    os.Exit(23)
  }
  if len(os.Args) == 3 && os.Args[1] == "--agent" && os.Args[2] == "version" {
    _ = json.NewEncoder(os.Stdout).Encode(map[string]string{"version": os.Getenv("TALENTO_FIXTURE_VERSION")})
    return
  }
  os.Exit(2)
}
'@
  [IO.File]::WriteAllText($SourcePath, $Source)
  & go build -o $OutputPath $SourcePath
  if ($LASTEXITCODE -ne 0) { throw 'could not build Windows installer fixture executable' }
  $env:TALENTO_FIXTURE_VERSION = $Version
}

function Write-FixtureRelease([string]$Version, [string]$BinaryPath, [string]$EntryName) {
  $Asset = "talento_${Version}_windows_$($env:TALENTO_TEST_ARCH).zip"
  $ArchivePath = Join-Path $Fixture $Asset
  Remove-Item -LiteralPath $ArchivePath -Force -ErrorAction SilentlyContinue
  Add-Type -AssemblyName System.IO.Compression.FileSystem
  $Archive = [IO.Compression.ZipFile]::Open($ArchivePath, [IO.Compression.ZipArchiveMode]::Create)
  try {
    $Entry = $Archive.CreateEntry($EntryName)
    $Input = [IO.File]::OpenRead($BinaryPath)
    try {
      $Output = $Entry.Open()
      try { $Input.CopyTo($Output) } finally { $Output.Dispose() }
    } finally { $Input.Dispose() }
  } finally { $Archive.Dispose() }
  $Hash = (Get-FileHash -Algorithm SHA256 $ArchivePath).Hash.ToLowerInvariant()
  [IO.File]::WriteAllText((Join-Path $Fixture 'checksums.txt'), "$Hash  $Asset`n")
  [IO.File]::WriteAllText((Join-Path $Fixture 'checksums.txt.sigstore.json'), "fixture-bundle`n")
  return $ArchivePath
}

function global:Invoke-WebRequest {
  param(
    [string]$Uri,
    [switch]$UseBasicParsing,
    [string]$ErrorAction,
    [hashtable]$Headers,
    [int]$MaximumRedirection,
    [int]$TimeoutSec,
    [string]$OutFile
  )
  $Name = [IO.Path]::GetFileName(([Uri]$Uri).AbsolutePath)
  $Source = Join-Path $env:TALENTO_TEST_FIXTURE $Name
  if ($OutFile) { Copy-Item -LiteralPath $Source -Destination $OutFile }
  return [pscustomobject]@{
    Content = if ($OutFile) { '' } else { [IO.File]::ReadAllText($Source) }
    BaseResponse = [pscustomobject]@{ ResponseUri = [Uri]$Uri }
  }
}

function global:Get-AuthenticodeSignature {
  param([string]$FilePath, [string]$ErrorAction)
  return [pscustomobject]@{
    Status = $env:TALENTO_TEST_AUTH_STATUS
    SignerCertificate = [pscustomobject]@{ Subject = $env:TALENTO_TEST_AUTH_PUBLISHER }
  }
}

$CosignPath = Join-Path $StubDir 'cosign.cmd'
[IO.File]::WriteAllText($CosignPath, @'
@echo off
echo %*>> "%TALENTO_TEST_COSIGN_LOG%"
exit /b %TALENTO_TEST_COSIGN_EXIT%
'@)

$RawInstaller = [IO.File]::ReadAllText($InstallerPath)
$StampedInstaller = $RawInstaller.Replace('__TALENTO_WINDOWS_AUTHENTICODE_PUBLISHER__', $Publisher.Replace("'", "''"))
$script:LastOutput = ''
$script:LastError = ''

function Invoke-InstallerCase([string]$ScriptText, [string]$Version) {
  $script:LastOutput = ''
  $script:LastError = ''
  $env:TALENTO_VERSION = $Version
  $env:TALENTO_INSTALL_DIR = $InstallDir
  $env:TALENTO_TEST_FIXTURE = $Fixture
  $env:TALENTO_TEST_COSIGN_EXIT = '0'
  $env:TALENTO_TEST_AUTH_STATUS = 'Valid'
  $env:TALENTO_TEST_AUTH_PUBLISHER = $Publisher
  $env:TALENTO_TEST_COSIGN_LOG = Join-Path $Work 'cosign.log'
  try {
    $script:LastOutput = (& ([ScriptBlock]::Create($ScriptText)) 6>&1 | Out-String)
    return $true
  } catch {
    $script:LastError = $_.Exception.Message
    return $false
  }
}

try {
  $env:TALENTO_TEST_ARCH = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
  $env:Path = "$StubDir;$env:Path"
  $Candidate = Join-Path $Work 'candidate.exe'
  Build-FixtureBinary '1.2.3' $Candidate
  Write-FixtureRelease '1.2.3' $Candidate 'talento.exe' | Out-Null

  Assert-True (Invoke-InstallerCase $StampedInstaller '1.2.3') "stable happy path failed: $script:LastError"
  $Target = Join-Path $InstallDir 'talento.exe'
  Assert-True (Test-Path -LiteralPath $Target) 'happy path did not install talento.exe'
  Assert-True ($script:LastOutput.Contains('Installed Talento CLI 1.2.3 from release v1.2.3')) 'installed version/source guidance is missing'
  Assert-True ($script:LastOutput.Contains("Add $InstallDir to PATH")) 'PATH guidance is missing'
  $CosignLog = [IO.File]::ReadAllText((Join-Path $Work 'cosign.log'))
  Assert-True ($CosignLog.Contains('--certificate-identity https://github.com/talentohq/talento-cli/.github/workflows/release.yml@refs/tags/v1.2.3')) 'Sigstore identity is not pinned to the exact tag'

  [IO.File]::AppendAllText($Target, 'old-installation-marker')
  $MarkedHash = (Get-FileHash -Algorithm SHA256 $Target).Hash
  Assert-True (Invoke-InstallerCase $StampedInstaller '1.2.3') "existing-binary happy path failed: $script:LastError"
  Assert-True ((Get-FileHash -Algorithm SHA256 $Target).Hash -ne $MarkedHash) 'successful replacement retained the old executable'

  Assert-True (-not (Invoke-InstallerCase $RawInstaller '1.2.3')) 'unstamped stable installer unexpectedly succeeded'
  Assert-True ($script:LastError.Contains('no expected Authenticode publisher policy')) 'unstamped publisher failure is not actionable'

  [IO.File]::AppendAllText($Target, 'rollback-marker')
  $OriginalHash = (Get-FileHash -Algorithm SHA256 $Target).Hash
  $env:TALENTO_TEST_COSIGN_EXIT = '1'
  $script:LastOutput = ''
  $script:LastError = ''
  try { & ([ScriptBlock]::Create($StampedInstaller)) 6>&1 | Out-String | Out-Null; $Succeeded = $true } catch { $Succeeded = $false; $script:LastError = $_.Exception.Message }
  Assert-True (-not $Succeeded) 'Sigstore mismatch unexpectedly installed'
  Assert-True ((Get-FileHash -Algorithm SHA256 $Target).Hash -eq $OriginalHash) 'Sigstore failure changed the installed executable'
  $env:TALENTO_TEST_COSIGN_EXIT = '0'

  Rename-Item -LiteralPath $CosignPath -NewName 'cosign.disabled'
  Assert-True (-not (Invoke-InstallerCase $StampedInstaller '1.2.3')) 'missing cosign unexpectedly installed'
  Assert-True ($script:LastError.Contains('cosign is required')) 'missing-verifier failure is not actionable'
  Rename-Item -LiteralPath (Join-Path $StubDir 'cosign.disabled') -NewName 'cosign.cmd'

  $env:TALENTO_TEST_AUTH_STATUS = 'NotSigned'
  $script:LastError = ''
  try { & ([ScriptBlock]::Create($StampedInstaller)) 6>&1 | Out-String | Out-Null; $Succeeded = $true } catch { $Succeeded = $false; $script:LastError = $_.Exception.Message }
  Assert-True (-not $Succeeded) 'unsigned stable executable unexpectedly installed'
  Assert-True ($script:LastError.Contains('Authenticode signature is not valid')) 'invalid-signature error is not actionable'
  Assert-True ((Get-FileHash -Algorithm SHA256 $Target).Hash -eq $OriginalHash) 'invalid signature changed the installed executable'
  $env:TALENTO_TEST_AUTH_STATUS = 'Valid'

  $env:TALENTO_TEST_AUTH_PUBLISHER = 'CN=Unexpected Publisher'
  $script:LastError = ''
  try { & ([ScriptBlock]::Create($StampedInstaller)) 6>&1 | Out-String | Out-Null; $Succeeded = $true } catch { $Succeeded = $false; $script:LastError = $_.Exception.Message }
  Assert-True (-not $Succeeded) 'wrong Authenticode publisher unexpectedly installed'
  Assert-True ($script:LastError.Contains('does not match the expected publisher')) 'publisher mismatch error is not actionable'
  Assert-True ((Get-FileHash -Algorithm SHA256 $Target).Hash -eq $OriginalHash) 'publisher mismatch changed the installed executable'
  $env:TALENTO_TEST_AUTH_PUBLISHER = $Publisher

  [IO.File]::WriteAllText((Join-Path $Fixture 'checksums.txt'), (('0' * 64) + "  talento_1.2.3_windows_$($env:TALENTO_TEST_ARCH).zip`n"))
  Assert-True (-not (Invoke-InstallerCase $StampedInstaller '1.2.3')) 'checksum mismatch unexpectedly installed'
  Assert-True ((Get-FileHash -Algorithm SHA256 $Target).Hash -eq $OriginalHash) 'checksum mismatch changed the installed executable'

  Write-FixtureRelease '1.2.3' $Candidate '../talento.exe' | Out-Null
  Assert-True (-not (Invoke-InstallerCase $StampedInstaller '1.2.3')) 'traversal archive unexpectedly installed'
  Assert-True ($script:LastError.Contains('unsafe path')) 'traversal archive error is not actionable'
  Assert-True ((Get-FileHash -Algorithm SHA256 $Target).Hash -eq $OriginalHash) 'unsafe archive changed the installed executable'

  Write-FixtureRelease '1.2.3' $Candidate 'talento.exe' | Out-Null
  $env:TALENTO_TEST_FAIL_INSTALLED = '1'
  Assert-True (-not (Invoke-InstallerCase $StampedInstaller '1.2.3')) 'post-install validation failure unexpectedly succeeded'
  $env:TALENTO_TEST_FAIL_INSTALLED = '0'
  Assert-True ((Get-FileHash -Algorithm SHA256 $Target).Hash -eq $OriginalHash) 'post-install failure did not restore the original executable'
  Assert-True (-not (Get-ChildItem -LiteralPath $InstallDir -Filter '.talento-*')) 'staging or rollback files remain'

  $env:TALENTO_FIXTURE_VERSION = '0.1.0'
  Write-FixtureRelease '0.1.0' $Candidate 'talento.exe' | Out-Null
  $env:TALENTO_VERSION = '0.1.0'
  $env:TALENTO_TEST_AUTH_STATUS = 'NotSigned'
  $script:LastError = ''
  try { & ([ScriptBlock]::Create($RawInstaller)) 6>&1 | Out-String | Out-Null; $Succeeded = $true } catch { $Succeeded = $false; $script:LastError = $_.Exception.Message }
  Assert-True $Succeeded "signed-manifest preview incorrectly required Authenticode: $script:LastError"

  Write-Host 'PowerShell installer offline tests passed.'
} finally {
  Remove-Item -LiteralPath $Work -Recurse -Force -ErrorAction SilentlyContinue
}
