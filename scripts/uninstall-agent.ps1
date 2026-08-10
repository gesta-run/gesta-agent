[CmdletBinding()]
param(
    [switch]$Yes,
    [switch]$KeepData,
    [string]$DataDir = "",
    [string]$InstallDir = "",
    [ValidateNotNullOrEmpty()]
    [string]$TaskName = "Gesta Agent"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($DataDir)) {
    $DataDir = Join-Path $env:USERPROFILE ".gesta"
}
if ([string]::IsNullOrWhiteSpace($InstallDir)) {
    $InstallDir = Join-Path $DataDir "bin"
}

$dataPath = [IO.Path]::GetFullPath($DataDir).TrimEnd([IO.Path]::DirectorySeparatorChar)
$homePath = [IO.Path]::GetFullPath($env:USERPROFILE).TrimEnd([IO.Path]::DirectorySeparatorChar)
$rootPath = [IO.Path]::GetPathRoot($dataPath).TrimEnd([IO.Path]::DirectorySeparatorChar)
if ([string]::IsNullOrWhiteSpace($dataPath) -or $dataPath -eq $homePath -or $dataPath -eq $rootPath) {
    throw "Refusing unsafe data directory: $dataPath"
}

$agentPath = Join-Path $InstallDir "gesta-agent.exe"
if (-not $Yes) {
    $prompt = if ($KeepData) {
        "Uninstall Gesta Agent and keep local data at $dataPath? [y/N]"
    } else {
        "Uninstall Gesta Agent and remove local data from $dataPath? [y/N]"
    }
    $answer = Read-Host $prompt
    if ($answer -notmatch '^(?i:y|yes)$') {
        Write-Host "Uninstall cancelled."
        exit 0
    }
}

if (-not (Test-Path -LiteralPath $agentPath -PathType Leaf)) {
    throw "Agent binary not found at $agentPath; reinstall the current agent, then rerun uninstall."
}

Write-Host "Removing Gesta hooks..." -ForegroundColor Cyan
& $agentPath uninstall-hooks
if ($LASTEXITCODE -ne 0) {
    throw "Hook cleanup failed; installation was left in place."
}

$task = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
if ($null -ne $task) {
    Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
}

$agentProcesses = @(
    Get-CimInstance Win32_Process -Filter "Name = 'gesta-agent.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.ExecutablePath -eq $agentPath }
)
foreach ($process in $agentProcesses) {
    Stop-Process -Id $process.ProcessId -Force -ErrorAction SilentlyContinue
}
foreach ($process in $agentProcesses) {
    Wait-Process -Id $process.ProcessId -Timeout 10 -ErrorAction SilentlyContinue
}

for ($attempt = 0; $attempt -lt 20 -and (Test-Path -LiteralPath $agentPath); $attempt++) {
    Remove-Item -LiteralPath $agentPath -Force -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $agentPath) {
        Start-Sleep -Milliseconds 250
    }
}
if (Test-Path -LiteralPath $agentPath) {
    throw "Could not remove agent binary after stopping its processes: $agentPath"
}
if ($KeepData) {
    Remove-Item -LiteralPath $InstallDir -Force -ErrorAction SilentlyContinue
    Write-Host "Gesta Agent removed; local data kept at $dataPath." -ForegroundColor Green
} else {
    Remove-Item -LiteralPath $dataPath -Recurse -Force -ErrorAction SilentlyContinue
    Write-Host "Gesta Agent and local data removed." -ForegroundColor Green
}
