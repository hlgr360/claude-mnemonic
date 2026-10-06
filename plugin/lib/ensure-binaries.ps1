# Fetch, verify and install the claude-mnemonic binaries that belong to this plugin version into
# %USERPROFILE%\.claude-mnemonic\bin: the Windows twin of ensure-binaries.sh, with the same rules.
#
# Usage: powershell -NoProfile -ExecutionPolicy Bypass -File ensure-binaries.ps1 [-Background] [-LogFile <path>]
#   -Background  do the work in a hidden process and return at once (hooks must not wait for a download)
#   -LogFile     also append every message to this file (the background process uses it)
#
# The release archive is claude-mnemonic_<version>_windows_amd64.zip. It is checked against the release's checksums.txt,
# and against its cosign signature when cosign is installed. A file that fails a check is never installed.
# Windows PowerShell 5.1 (the one built into Windows) runs this script; it uses nothing newer.
#
# When to install (same as the shell script):
#   - worker.exe or mcp-server.exe is missing                  -> install
#   - no marker (binaries from install.ps1 or a source build)  -> leave them alone
#   - marker older than the plugin version                     -> install
#   - marker equal or newer (the in-app updater moved on)      -> leave them alone
#
# Environment (for forks and tests):
#   MNEMONIC_REPO          release repository, default below
#   MNEMONIC_RELEASE_BASE  base URL of the release's files, default https://github.com/<repo>/releases/download/v<version>
#   MNEMONIC_DATA_DIR      the data directory, default %USERPROFILE%\.claude-mnemonic
param(
    [switch]$Background,
    [string]$LogFile = ''
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$DefaultRepo = "hlgr360/claude-mnemonic"
$Repo = if ($env:MNEMONIC_REPO) { $env:MNEMONIC_REPO } else { $DefaultRepo }

$PluginRoot = Split-Path -Parent $PSScriptRoot
$Manifest = Join-Path $PluginRoot '.claude-plugin\plugin.json'
$Data = if ($env:MNEMONIC_DATA_DIR) { $env:MNEMONIC_DATA_DIR } else { Join-Path $env:USERPROFILE '.claude-mnemonic' }
$Bin = Join-Path $Data 'bin'
$Marker = Join-Path $Bin '.plugin-version'
$Lock = Join-Path $Data '.install.lock'
$Log = Join-Path $Data 'plugin-install.log'

function Say([string]$Message) {
    $line = "claude-mnemonic: $Message"
    [Console]::Error.WriteLine($line)
    if ($LogFile) {
        try { Add-Content -LiteralPath $LogFile -Value $line -Encoding UTF8 } catch { }
    }
}

function Fail([string]$Message) {
    Say $Message
    exit 1
}

function Get-PluginVersion {
    try {
        $v = (Get-Content -LiteralPath $Manifest -Raw -Encoding UTF8 | ConvertFrom-Json).version
        if ($v) { return [string]$v }
    } catch { }
    return ''
}

# $true when $a is a higher dotted version than $b: up to four numeric parts, a suffix such as -ci.15 is ignored.
function Test-VersionGreater([string]$a, [string]$b) {
    try {
        $va = [version](($a -split '-')[0])
        $vb = [version](($b -split '-')[0])
        return $va -gt $vb
    } catch {
        return $false
    }
}

function Test-NeedsInstall {
    if (-not ((Test-Path -LiteralPath (Join-Path $Bin 'worker.exe')) -and (Test-Path -LiteralPath (Join-Path $Bin 'mcp-server.exe')))) {
        return $true
    }
    if (-not (Test-Path -LiteralPath $Marker)) { return $false }
    $installed = (Get-Content -LiteralPath $Marker -TotalCount 1 -Encoding UTF8)
    return (Test-VersionGreater $script:Version ([string]$installed).Trim())
}

$Version = Get-PluginVersion
if (-not $Version) { Fail "cannot read the plugin version from $Manifest" }

if ($Background) {
    if (-not (Test-NeedsInstall)) { exit 0 }
    New-Item -ItemType Directory -Force -Path $Data | Out-Null
    $psExe = (Get-Process -Id $PID).Path
    $argList = @('-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', ('"{0}"' -f $PSCommandPath), '-LogFile', ('"{0}"' -f $Log))
    Start-Process -FilePath $psExe -ArgumentList $argList -WindowStyle Hidden | Out-Null
    exit 0
}

if (-not (Test-NeedsInstall)) { exit 0 }

# The release has one Windows build, for x86-64. Windows on ARM runs it through its x64 emulation.
$arch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
if ($arch -ne 'AMD64' -and $arch -ne 'ARM64') {
    Fail "no release build for Windows $arch; use scripts\install.ps1 from $Repo instead"
}
$Archive = "claude-mnemonic_${Version}_windows_amd64.zip"
$Base = if ($env:MNEMONIC_RELEASE_BASE) { $env:MNEMONIC_RELEASE_BASE.TrimEnd('/') } else { "https://github.com/$Repo/releases/download/v$Version" }

# GitHub needs TLS 1.2, which Windows PowerShell 5.1 does not always offer by default.
try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch { }

# One installer at a time: the session-start hook and the MCP server both start on a first run. The lock is a file that
# is created exclusively; a lock older than ten minutes is a crashed installer's and is taken over.
New-Item -ItemType Directory -Force -Path $Data | Out-Null
$lockStream = $null
$waited = 0
while ($null -eq $lockStream) {
    try {
        $lockStream = [System.IO.File]::Open($Lock, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
    } catch {
        $held = Get-Item -LiteralPath $Lock -ErrorAction SilentlyContinue
        if ($held -and ((Get-Date) - $held.LastWriteTime).TotalMinutes -gt 10) {
            Remove-Item -LiteralPath $Lock -Force -ErrorAction SilentlyContinue
            continue
        }
        $waited++
        if ($waited -gt 120) { Fail 'another installation did not finish in two minutes' }
        Start-Sleep -Seconds 1
    }
}

$Tmp = Join-Path $Data ('.install.' + [guid]::NewGuid().ToString('N').Substring(0, 8))
try {
    # The installer that held the lock may have done the work.
    if (-not (Test-NeedsInstall)) { exit 0 }

    New-Item -ItemType Directory -Force -Path $Tmp | Out-Null
    Say "installing $Version (windows_amd64) from $Base"

    function Get-File([string]$Url, [string]$Dest) {
        $lastError = $null
        for ($i = 0; $i -lt 3; $i++) {
            try {
                Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $Dest
                return
            } catch {
                $lastError = $_
                Start-Sleep -Seconds 1
            }
        }
        Fail "download of $Url failed: $($lastError.Exception.Message)"
    }

    $zip = Join-Path $Tmp $Archive
    $sums = Join-Path $Tmp 'checksums.txt'
    Get-File "$Base/$Archive" $zip
    Get-File "$Base/checksums.txt" $sums

    $expected = $null
    foreach ($line in (Get-Content -LiteralPath $sums -Encoding UTF8)) {
        $parts = $line.Trim() -split '\s+'
        if ($parts.Count -ge 2 -and $parts[1].TrimStart('*') -eq $Archive) { $expected = $parts[0]; break }
    }
    if (-not $expected) { Fail "checksums.txt has no entry for $Archive" }
    $actual = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash
    if ($expected -ne $actual) {
        Fail "checksum mismatch for $Archive (expected $expected, got $actual); nothing installed"
    }

    $cosign = Get-Command cosign -ErrorAction SilentlyContinue
    if ($cosign) {
        $bundle = Join-Path $Tmp 'checksums.txt.sigstore.json'
        Get-File "$Base/checksums.txt.sigstore.json" $bundle
        # Windows PowerShell 5.1 turns a native command's stderr into a terminating error under 'Stop'.
        $ErrorActionPreference = 'Continue'
        & $cosign.Source verify-blob --bundle $bundle `
            --certificate-identity-regexp ('^https://github\.com/' + $Repo + '/.*$') `
            --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' $sums *> $null
        $verified = $LASTEXITCODE
        $ErrorActionPreference = 'Stop'
        if ($verified -ne 0) { Fail 'signature check failed for checksums.txt; nothing installed' }
    } else {
        Say 'cosign is not installed: checked the checksum only, not the signature'
    }

    $x = Join-Path $Tmp 'x'
    try { Expand-Archive -LiteralPath $zip -DestinationPath $x -Force } catch { Fail "cannot unpack ${Archive}: $($_.Exception.Message)" }
    if (-not ((Test-Path -LiteralPath (Join-Path $x 'worker.exe')) -and (Test-Path -LiteralPath (Join-Path $x 'mcp-server.exe')))) {
        Fail "$Archive has no worker.exe and mcp-server.exe"
    }

    # Windows cannot overwrite a running executable but can rename it: move the old file aside, put the new one in its
    # place, and delete the old one when nothing runs it (otherwise the next run removes it).
    function Install-File([string]$Source, [string]$Dest) {
        $new = "$Dest.new"
        Copy-Item -LiteralPath $Source -Destination $new -Force
        if (Test-Path -LiteralPath $Dest) {
            $old = "$Dest.old-$PID"
            Move-Item -LiteralPath $Dest -Destination $old -Force
            try { Move-Item -LiteralPath $new -Destination $Dest -Force } catch {
                Move-Item -LiteralPath $old -Destination $Dest -Force
                throw
            }
            Remove-Item -LiteralPath $old -Force -ErrorAction SilentlyContinue
        } else {
            Move-Item -LiteralPath $new -Destination $Dest -Force
        }
    }

    New-Item -ItemType Directory -Force -Path (Join-Path $Bin 'hooks') | Out-Null
    # Leftovers of an earlier upgrade whose old executable was still running.
    Get-ChildItem -Path $Bin -Recurse -Filter '*.old-*' -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue
    try {
        Install-File (Join-Path $x 'worker.exe') (Join-Path $Bin 'worker.exe')
        Install-File (Join-Path $x 'mcp-server.exe') (Join-Path $Bin 'mcp-server.exe')
        $hooksDir = Join-Path $x 'hooks'
        if (Test-Path -LiteralPath $hooksDir) {
            foreach ($f in (Get-ChildItem -LiteralPath $hooksDir -Filter '*.exe' -File)) {
                Install-File $f.FullName (Join-Path $Bin ('hooks\' + $f.Name))
            }
        }
    } catch {
        Fail "cannot install into ${Bin}: $($_.Exception.Message)"
    }
    $markerNew = "$Marker.new"
    # One line ending in LF and no byte order mark, so ensure-binaries.sh reads it as well.
    [System.IO.File]::WriteAllText($markerNew, "$Version`n")
    Move-Item -LiteralPath $markerNew -Destination $Marker -Force
    Say "installed $Version into $Bin"
} finally {
    if ($lockStream) { $lockStream.Dispose(); Remove-Item -LiteralPath $Lock -Force -ErrorAction SilentlyContinue }
    if (Test-Path -LiteralPath $Tmp) { Remove-Item -LiteralPath $Tmp -Recurse -Force -ErrorAction SilentlyContinue }
}
