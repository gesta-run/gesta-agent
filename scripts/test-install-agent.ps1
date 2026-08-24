[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$BuiltAgent,

    [Parameter(Mandatory = $true)]
    [string]$BuiltHook
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$testRoot = Join-Path $env:RUNNER_TEMP "gesta-windows-installer-test"
$siteRoot = Join-Path $testRoot "site"
$siteBin = Join-Path $siteRoot "bin"
$testHome = Join-Path $testRoot "home"
$installDir = Join-Path $testRoot "install\bin"
$assetName = "gesta-agent-windows-amd64.exe"
$hookAssetName = "gesta-agent-hook-launcher-windows-amd64.exe"
$siteAgent = Join-Path $siteBin $assetName
$siteHook = Join-Path $siteBin $hookAssetName
$installedAgent = Join-Path $installDir "gesta-agent.exe"
$installedHook = Join-Path $installDir "gesta-agent-hook-launcher.exe"
$baseUrl = "http://127.0.0.1:18765"
$server = $null
$controlServer = $null
$daemon = $null
$taskName = "Gesta Agent"
$controlRequestLog = Join-Path $testRoot "control-request.json"

function Set-TestChecksums([string]$AgentHash, [string]$HookHash) {
    Set-Content -LiteralPath (Join-Path $siteRoot "SHA256SUMS") -Encoding Ascii -Value @(
        "$AgentHash  bin/$assetName",
        "$HookHash  bin/$hookAssetName"
    )
}

function Get-PESubsystem([string]$Path) {
    $bytes = [IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -lt 256 -or $bytes[0] -ne 0x4d -or $bytes[1] -ne 0x5a) {
        throw "Not a PE executable: $Path"
    }
    $peOffset = [BitConverter]::ToInt32($bytes, 0x3c)
    return [BitConverter]::ToUInt16($bytes, $peOffset + 24 + 68)
}

function Assert-ChecksumFailurePreservesInstall([string]$AgentHash, [string]$HookHash) {
    $installedAgentHash = (Get-FileHash -LiteralPath $installedAgent -Algorithm SHA256).Hash
    $installedHookHash = (Get-FileHash -LiteralPath $installedHook -Algorithm SHA256).Hash
    Set-TestChecksums -AgentHash $AgentHash -HookHash $HookHash
    $checksumFailed = $false
    try {
        & "$PSScriptRoot/install-agent.ps1" `
            -ControlUrl "http://127.0.0.1:18080" `
            -ApiKey "sk-ci-windows" `
            -BaseUrl $baseUrl `
            -InstallDir $installDir `
            -NoDaemon
    } catch {
        $checksumFailed = $true
    }
    if (-not $checksumFailed) {
        throw "Installer accepted a checksum mismatch."
    }
    if ((Get-FileHash -LiteralPath $installedAgent -Algorithm SHA256).Hash -ne $installedAgentHash -or
        (Get-FileHash -LiteralPath $installedHook -Algorithm SHA256).Hash -ne $installedHookHash) {
        throw "Checksum failure changed the installed Windows bundle."
    }
}

function Wait-TestArtifactServer() {
    for ($attempt = 0; $attempt -lt 20; $attempt++) {
        try {
            Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/SHA256SUMS" | Out-Null
            return
        } catch {
            Start-Sleep -Milliseconds 250
        }
    }
    throw "Local artifact server did not start."
}

function Wait-TestControlServer() {
    for ($attempt = 0; $attempt -lt 20; $attempt++) {
        try {
            Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:18080/healthz" | Out-Null
            return
        } catch {
            Start-Sleep -Milliseconds 250
        }
    }
    throw "Local control server did not start."
}

function Assert-HookLauncherInstalled() {
    if ($LASTEXITCODE -ne 0 -or
        -not (Test-Path -LiteralPath $installedAgent) -or
        -not (Test-Path -LiteralPath $installedHook)) {
        throw "Windows installer did not install the agent."
    }
    $hooksPath = Join-Path $testHome ".codex\hooks.json"
    $escapedInstalledHook = $installedHook.Replace('\', '\\')
    $hooksJson = Get-Content -LiteralPath $hooksPath -Raw
    if (-not $hooksJson.Contains($escapedInstalledHook)) {
        throw "Windows Codex hooks do not use the no-window launcher."
    }
    $hookOutput = ('{"hook_event_name":"SessionStart"}' | & $installedHook codex-hook | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $hookOutput -ne "{}") {
        throw "Windows hook launcher did not preserve the Codex hook protocol."
    }
}

function Assert-AgentUninstalled() {
    foreach ($binaryPath in @($installedAgent, $installedHook)) {
        if (Test-Path -LiteralPath $binaryPath) {
            throw "Windows uninstaller left an installed binary: $binaryPath"
        }
    }
    if ($null -ne (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue)) {
        throw "Windows uninstaller did not remove the scheduled task."
    }
    foreach ($settingsPath in @(
        (Join-Path $testHome ".codex\hooks.json"),
        (Join-Path $testHome ".codex\config.toml"),
        (Join-Path $testHome ".claude\settings.json")
    )) {
        if ((Test-Path -LiteralPath $settingsPath) -and
            (Select-String -LiteralPath $settingsPath -Pattern "gesta-agent|hooks\.state" -Quiet)) {
            throw "Windows uninstaller left Gesta hook configuration in $settingsPath."
        }
    }
}

function Assert-DeregisterRequest([string]$ExpectedDaemonID) {
    if (-not (Test-Path -LiteralPath $controlRequestLog)) {
        throw "Windows uninstaller did not deregister the Agent."
    }
    $request = Get-Content -LiteralPath $controlRequestLog -Raw | ConvertFrom-Json
    if ($request.path -ne "/api/v1/daemon" -or
        $request.daemon_id -ne $ExpectedDaemonID -or
        $request.authorization -ne "Bearer sk-ci-windows") {
        throw "Unexpected deregistration request."
    }
}

try {
    New-Item -ItemType Directory -Path $siteBin, $testHome, $installDir -Force | Out-Null
    Copy-Item -LiteralPath $BuiltAgent -Destination $siteAgent
    Copy-Item -LiteralPath $BuiltHook -Destination $siteHook
    $hash = (Get-FileHash -LiteralPath $siteAgent -Algorithm SHA256).Hash.ToLowerInvariant()
    $hookHash = (Get-FileHash -LiteralPath $siteHook -Algorithm SHA256).Hash.ToLowerInvariant()
    Set-TestChecksums -AgentHash $hash -HookHash $hookHash
    if ((Get-PESubsystem -Path $siteHook) -ne 2) {
        throw "Windows hook launcher is not built as a GUI-subsystem executable."
    }

    $server = Start-Process python -ArgumentList @(
        "-m", "http.server", "18765", "--bind", "127.0.0.1", "--directory", $siteRoot
    ) -PassThru -WindowStyle Hidden
    Wait-TestArtifactServer
    $controlServer = Start-Process python -ArgumentList @(
        "$PSScriptRoot/test-control-server.py", "--port", "18080", "--request-log", $controlRequestLog
    ) -PassThru -WindowStyle Hidden
    Wait-TestControlServer

    $env:USERPROFILE = $testHome
    & "$PSScriptRoot/install-agent.ps1" `
        -ControlUrl "http://127.0.0.1:18080" `
        -ApiKey "sk-ci-windows" `
        -BaseUrl $baseUrl `
        -InstallDir $installDir `
        -NoDaemon
    Assert-HookLauncherInstalled

    & $installedAgent status --require-running --wait 200ms 2>$null
    if ($LASTEXITCODE -eq 0) {
        throw "Runtime health check succeeded before the daemon started."
    }
    $daemon = Start-Process -FilePath $installedAgent -ArgumentList @("run", "--interval", "1m") -PassThru -WindowStyle Hidden
    & $installedAgent status --require-running --wait 10s
    if ($LASTEXITCODE -ne 0) {
        throw "Runtime health check did not observe the running daemon."
    }

    Stop-Process -Id $daemon.Id -Force
    $daemon.WaitForExit()
    $daemon = $null
    & "$PSScriptRoot/install-agent.ps1" `
        -ControlUrl "http://127.0.0.1:18080" `
        -ApiKey "sk-ci-windows" `
        -BaseUrl $baseUrl `
        -InstallDir $installDir
    if ($LASTEXITCODE -ne 0) {
        throw "Windows installer did not start the scheduled runtime."
    }
    $task = Get-ScheduledTask -TaskName $taskName
    $taskCommand = "$($task.Actions.Execute) $($task.Actions.Arguments)"
    if ($taskCommand -match "sk-ci-windows" -or $taskCommand -match "--apikey") {
        throw "Scheduled task action contains the API key."
    }

    Assert-ChecksumFailurePreservesInstall -AgentHash ("0" * 64) -HookHash $hookHash
    Assert-ChecksumFailurePreservesInstall -AgentHash $hash -HookHash ("0" * 64)
    Set-TestChecksums -AgentHash $hash -HookHash $hookHash

    $dataDir = Join-Path $testHome ".gesta"
    $expectedDaemonID = (Get-Content -LiteralPath (Join-Path $dataDir "state.json") -Raw | ConvertFrom-Json).daemon_id
    & "$PSScriptRoot/uninstall-agent.ps1" -Yes -DataDir $dataDir -InstallDir $installDir
    Assert-AgentUninstalled
    Assert-DeregisterRequest -ExpectedDaemonID $expectedDaemonID
} finally {
    if ($null -ne $daemon -and -not $daemon.HasExited) {
        Stop-Process -Id $daemon.Id -Force -ErrorAction SilentlyContinue
    }
    if ($null -ne $server -and -not $server.HasExited) {
        Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue
    }
    if ($null -ne $controlServer -and -not $controlServer.HasExited) {
        Stop-Process -Id $controlServer.Id -Force -ErrorAction SilentlyContinue
    }
    Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
}
