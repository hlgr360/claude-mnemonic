# Tests of plugin/lib/ensure-binaries.ps1 on Windows. Run with Windows PowerShell 5.1 and with pwsh:
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test_ensure_binaries.ps1
#
# Everything happens in a temporary directory: the data directory is MNEMONIC_DATA_DIR, the "release" is a fixture
# served by python's http.server on a private port, and the binaries are placeholders that are never started
# (except ping.exe copies, to prove that a running executable can be replaced). The real %USERPROFILE%\.claude-mnemonic
# is never touched.
$ErrorActionPreference = 'Stop'

$Repo = Split-Path -Parent $PSScriptRoot
$Script = Join-Path $Repo 'plugin\lib\ensure-binaries.ps1'
$Packer = Join-Path $Repo 'scripts\package_release.py'
$Python = (Get-Command python -ErrorAction SilentlyContinue)
if (-not $Python) { $Python = Get-Command python3 }
$PowerShellExe = (Get-Process -Id $PID).Path
$Root = Join-Path ([IO.Path]::GetTempPath()) ('mnemonic-ps-' + [guid]::NewGuid().ToString('N').Substring(0, 8))
New-Item -ItemType Directory -Force -Path $Root | Out-Null

$failures = New-Object System.Collections.ArrayList
$passed = 0
function Check([bool]$Condition, [string]$Message) {
    if ($Condition) { $script:passed++ } else { [void]$failures.Add($Message); Write-Host "FAIL: $Message" }
}

function New-Plugin([string]$Name, [string]$Version) {
    $dir = Join-Path $Root $Name
    New-Item -ItemType Directory -Force -Path (Join-Path $dir '.claude-plugin'), (Join-Path $dir 'lib') | Out-Null
    [IO.File]::WriteAllText((Join-Path $dir '.claude-plugin\plugin.json'), "{`"name`": `"claude-mnemonic`", `"version`": `"$Version`"}")
    Copy-Item $Script (Join-Path $dir 'lib\ensure-binaries.ps1')
    return $dir
}

# A release directory holding the zip and checksums.txt for $Version; returns the directory.
function New-Release([string]$Name, [string]$Version, [string]$Marker = 'v1', [switch]$WrongChecksum) {
    $stage = Join-Path $Root "$Name-stage"
    $release = Join-Path $Root "$Name-release"
    New-Item -ItemType Directory -Force -Path (Join-Path $stage 'hooks'), $release | Out-Null
    foreach ($n in 'worker.exe', 'mcp-server.exe') { [IO.File]::WriteAllText((Join-Path $stage $n), "$n $Marker") }
    foreach ($h in 'session-start', 'stop') { [IO.File]::WriteAllText((Join-Path $stage "hooks\$h.exe"), "$h $Marker") }
    [IO.File]::WriteAllText((Join-Path $stage 'hooks\hooks.json'), '{}')
    $zip = Join-Path $release "claude-mnemonic_${Version}_windows_amd64.zip"
    & $Python.Source $Packer $stage $zip
    if ($LASTEXITCODE -ne 0) { throw 'packing the fixture release failed' }
    $hash = if ($WrongChecksum) { '0' * 64 } else { (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLower() }
    [IO.File]::WriteAllText((Join-Path $release 'checksums.txt'), "$hash  claude-mnemonic_${Version}_windows_amd64.zip`n")
    return $release
}

$port = Get-Random -Minimum 20000 -Maximum 40000
$releases = Join-Path $Root 'www'
New-Item -ItemType Directory -Force -Path $releases | Out-Null
$server = Start-Process -FilePath $Python.Source -ArgumentList @('-m', 'http.server', "$port", '--bind', '127.0.0.1') `
    -WorkingDirectory $releases -WindowStyle Hidden -PassThru

function Publish([string]$Release, [string]$Path) {
    $dest = Join-Path $releases $Path
    New-Item -ItemType Directory -Force -Path $dest | Out-Null
    Copy-Item (Join-Path $Release '*') $dest -Force
}

function Invoke-Installer([string]$Plugin, [string]$Data, [string]$ReleasePath, [string[]]$ExtraArgs = @()) {
    $env:MNEMONIC_DATA_DIR = $Data
    $env:MNEMONIC_RELEASE_BASE = "http://127.0.0.1:$port/$ReleasePath"
    # Windows PowerShell 5.1 raises on a native command's stderr under 'Stop'; the installer reports on stderr.
    $ErrorActionPreference = 'Continue'
    $out = & $PowerShellExe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File (Join-Path $Plugin 'lib\ensure-binaries.ps1') @ExtraArgs 2>&1
    $code = $LASTEXITCODE
    $ErrorActionPreference = 'Stop'
    return [pscustomobject]@{ Code = $code; Text = (($out | ForEach-Object { "$_" }) -join "`n") }
}

function Leftovers([string]$Data) {
    if (-not (Test-Path $Data)) { return @() }
    return @(Get-ChildItem -Force -Path $Data | Where-Object { $_.Name -like '.install*' })
}

try {
    Start-Sleep -Seconds 2   # the fixture server needs a moment

    # 1. A first install: the binaries, the hooks, the marker; no hooks.json, no leftovers.
    $plugin = New-Plugin 'p1' '1.2.3'
    Publish (New-Release 'r123' '1.2.3') 'v1.2.3'
    $data = Join-Path $Root 'data1'
    $r = Invoke-Installer $plugin $data 'v1.2.3'
    Check ($r.Code -eq 0) "first install exits 0 (got $($r.Code)): $($r.Text)"
    $bin = Join-Path $data 'bin'
    foreach ($n in 'worker.exe', 'mcp-server.exe', 'hooks\session-start.exe', 'hooks\stop.exe') {
        Check (Test-Path (Join-Path $bin $n)) "installed $n"
    }
    Check (-not (Test-Path (Join-Path $bin 'hooks\hooks.json'))) 'hooks.json stays in the plugin'
    $markerText = [IO.File]::ReadAllText((Join-Path $bin '.plugin-version'))
    Check ($markerText -eq "1.2.3`n") "the marker is the version and one LF (got '$markerText')"
    Check ((Leftovers $data).Count -eq 0) 'no temporary directory or lock is left behind'
    Check ($r.Text -match 'cosign is not installed|installed 1\.2\.3') 'it says what it did'

    # 2. Up to date: nothing is downloaded (the server has no such release, so a download would fail).
    $r = Invoke-Installer $plugin $data 'v-does-not-exist'
    Check ($r.Code -eq 0 -and $r.Text -notmatch 'installing') "an up-to-date install does nothing (code $($r.Code)): $($r.Text)"

    # 3. A newer marker (the in-app updater moved on) is left alone.
    [IO.File]::WriteAllText((Join-Path $bin '.plugin-version'), "9.9.9`n")
    $r = Invoke-Installer $plugin $data 'v-does-not-exist'
    Check ($r.Code -eq 0 -and $r.Text -notmatch 'installing') 'a newer installation is left alone'

    # 4. A wrong checksum installs nothing.
    $plugin2 = New-Plugin 'p2' '2.0.0'
    Publish (New-Release 'r200bad' '2.0.0' -WrongChecksum) 'v2.0.0'
    $data2 = Join-Path $Root 'data2'
    $r = Invoke-Installer $plugin2 $data2 'v2.0.0'
    Check ($r.Code -ne 0 -and $r.Text -match 'checksum mismatch') "a wrong checksum is refused (code $($r.Code)): $($r.Text)"
    Check (-not (Test-Path (Join-Path $data2 'bin\worker.exe'))) 'nothing is installed after a checksum failure'
    Check ((Leftovers $data2).Count -eq 0) 'a failed install leaves no lock or temporary directory'

    # 5. A missing release fails with a message and exit 1.
    $r = Invoke-Installer $plugin2 (Join-Path $Root 'data3') 'v-missing'
    Check ($r.Code -ne 0 -and $r.Text -match 'download of') "a failed download is reported (code $($r.Code)): $($r.Text)"

    # 6. An upgrade replaces a worker.exe that is running (Windows cannot overwrite it, but can rename it).
    $plugin4 = New-Plugin 'p4' '1.0.1'
    Publish (New-Release 'r101' '1.0.1' 'v2') 'v1.0.1'
    $data4 = Join-Path $Root 'data4'
    $bin4 = Join-Path $data4 'bin'
    New-Item -ItemType Directory -Force -Path (Join-Path $bin4 'hooks') | Out-Null
    Copy-Item (Join-Path $env:SystemRoot 'System32\PING.EXE') (Join-Path $bin4 'worker.exe')
    [IO.File]::WriteAllText((Join-Path $bin4 'mcp-server.exe'), 'old mcp')
    [IO.File]::WriteAllText((Join-Path $bin4 '.plugin-version'), "1.0.0`n")
    $running = Start-Process -FilePath (Join-Path $bin4 'worker.exe') -ArgumentList @('-n', '60', '127.0.0.1') -WindowStyle Hidden -PassThru
    try {
        Start-Sleep -Milliseconds 500
        $r = Invoke-Installer $plugin4 $data4 'v1.0.1'
        Check ($r.Code -eq 0) "an upgrade over a running worker.exe succeeds (code $($r.Code)): $($r.Text)"
        Check (([IO.File]::ReadAllText((Join-Path $bin4 'worker.exe'))) -eq 'worker.exe v2') 'the new worker.exe is in place'
        Check ((([IO.File]::ReadAllText((Join-Path $bin4 '.plugin-version'))).Trim()) -eq '1.0.1') 'the marker moved to the new version'
    } finally {
        if ($running -and -not $running.HasExited) { Stop-Process -Id $running.Id -Force -ErrorAction SilentlyContinue }
    }
    Start-Sleep -Milliseconds 300
    $r = Invoke-Installer $plugin4 $data4 'v-does-not-exist'
    Check ((@(Get-ChildItem -Recurse -Force -Path $bin4 -Filter '*.old-*')).Count -eq 0) 'the next run leaves no renamed old executable behind'

    # 7. -Background returns at once and the install finishes in a hidden process that writes plugin-install.log.
    $plugin5 = New-Plugin 'p5' '1.2.3'
    $data5 = Join-Path $Root 'data5'
    $watch = [Diagnostics.Stopwatch]::StartNew()
    $r = Invoke-Installer $plugin5 $data5 'v1.2.3' @('-Background')
    $watch.Stop()
    Check ($r.Code -eq 0) "-Background exits 0 (code $($r.Code)): $($r.Text)"
    $deadline = (Get-Date).AddSeconds(60)
    while ((Get-Date) -lt $deadline -and -not (Test-Path (Join-Path $data5 'bin\.plugin-version'))) { Start-Sleep -Milliseconds 300 }
    Check (Test-Path (Join-Path $data5 'bin\.plugin-version')) 'the background install finished'
    $logPath = Join-Path $data5 'plugin-install.log'
    Check ((Test-Path $logPath) -and ((Get-Content -Raw $logPath) -match 'installed 1\.2\.3')) 'the background install wrote plugin-install.log'

    # 8. -Background on an up-to-date install starts nothing.
    $r = Invoke-Installer $plugin5 $data5 'v-does-not-exist' @('-Background')
    Check ($r.Code -eq 0 -and $r.Text -eq '') '-Background on a current install is silent'
} finally {
    if ($server -and -not $server.HasExited) { Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue }
    Remove-Item Env:MNEMONIC_DATA_DIR, Env:MNEMONIC_RELEASE_BASE -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 500
    Remove-Item -Recurse -Force -Path $Root -ErrorAction SilentlyContinue
}

Write-Host "$passed checks passed, $($failures.Count) failed"
if ($failures.Count -gt 0) { exit 1 }
