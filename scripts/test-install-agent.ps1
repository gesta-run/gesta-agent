[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$BuiltAgent
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
$siteAgent = Join-Path $siteBin $assetName
$installedAgent = Join-Path $installDir "gesta-agent.exe"
$baseUrl = "http://127.0.0.1:18765"
$server = $null
$daemon = $null
$taskName = "Gesta Agent"

try {
    New-Item -ItemType Directory -Path $siteBin, $testHome, $installDir -Force | Out-Null
    Copy-Item -LiteralPath $BuiltAgent -Destination $siteAgent
    $hash = (Get-FileHash -LiteralPath $siteAgent -Algorithm SHA256).Hash.ToLowerInvariant()
    Set-Content -LiteralPath (Join-Path $siteRoot "SHA256SUMS") -Encoding Ascii -Value "$hash  bin/$assetName"

    $server = Start-Process python -ArgumentList @(
        "-m", "http.server", "18765", "--bind", "127.0.0.1", "--directory", $siteRoot
    ) -PassThru -WindowStyle Hidden
    $ready = $false
    for ($attempt = 0; $attempt -lt 20; $attempt++) {
        try {
            Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/SHA256SUMS" | Out-Null
            $ready = $true
            break
        } catch {
            Start-Sleep -Milliseconds 250
        }
    }
    if (-not $ready) {
        throw "Local artifact server did not start."
    }

    $env:USERPROFILE = $testHome
    & "$PSScriptRoot/install-agent.ps1" `
        -ControlUrl "http://127.0.0.1:18080" `
        -ApiKey "sk-ci-windows" `
        -BaseUrl $baseUrl `
        -InstallDir $installDir `
        -NoDaemon
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $installedAgent)) {
        throw "Windows installer did not install the agent."
    }

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

    $installedHash = (Get-FileHash -LiteralPath $installedAgent -Algorithm SHA256).Hash
    Set-Content -LiteralPath (Join-Path $siteRoot "SHA256SUMS") -Encoding Ascii -Value "$("0" * 64)  bin/$assetName"
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
    if ((Get-FileHash -LiteralPath $installedAgent -Algorithm SHA256).Hash -ne $installedHash) {
        throw "Checksum failure changed the installed agent."
    }
} finally {
    if ($null -ne $daemon -and -not $daemon.HasExited) {
        Stop-Process -Id $daemon.Id -Force -ErrorAction SilentlyContinue
    }
    if ($null -ne $server -and -not $server.HasExited) {
        Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue
    }
    Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
}
