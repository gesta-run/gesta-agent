[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateNotNullOrEmpty()]
    [string]$ControlUrl,

    [Parameter(Mandatory = $true)]
    [ValidateNotNullOrEmpty()]
    [string]$ApiKey,

    [ValidateNotNullOrEmpty()]
    [string]$BaseUrl = "https://artifacts.gesta.run/gesta/agent/rc",

    [string]$InstallDir = "",

    [switch]$NoDaemon
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

function Write-Step([string]$Message) {
    Write-Host "-> $Message" -ForegroundColor Cyan
}

function Write-Success([string]$Message) {
    Write-Host "OK $Message" -ForegroundColor Green
}

function Set-PrivateDirectoryAcl([string]$Path) {
    $identity = [System.Security.Principal.WindowsIdentity]::GetCurrent()
    $acl = Get-Acl -LiteralPath $Path
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($rule in @($acl.Access)) {
        [void]$acl.RemoveAccessRuleAll($rule)
    }
    $rights = [System.Security.AccessControl.FileSystemRights]::FullControl
    $inheritance = [System.Security.AccessControl.InheritanceFlags]::ContainerInherit -bor
        [System.Security.AccessControl.InheritanceFlags]::ObjectInherit
    $propagation = [System.Security.AccessControl.PropagationFlags]::None
    $type = [System.Security.AccessControl.AccessControlType]::Allow
    $rule = [System.Security.AccessControl.FileSystemAccessRule]::new(
        $identity.User,
        $rights,
        $inheritance,
        $propagation,
        $type
    )
    [void]$acl.AddAccessRule($rule)
    Set-Acl -LiteralPath $Path -AclObject $acl
}

function Stop-InstalledAgent([string]$AgentPath, [string]$TaskName) {
    $task = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    if ($null -ne $task) {
        Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    }
    $hookPath = Join-Path (Split-Path -Parent $AgentPath) "gesta-agent-hook-launcher.exe"
    foreach ($processName in @("gesta-agent.exe", "gesta-agent-hook-launcher.exe")) {
        Get-CimInstance Win32_Process -Filter "Name = '$processName'" -ErrorAction SilentlyContinue |
            Where-Object { $_.ExecutablePath -in @($AgentPath, $hookPath) } |
            ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
    }
}

function Get-VerifiedArtifact(
    [string]$Base,
    [string]$AssetPath,
    [string]$ChecksumText,
    [string]$ChecksumsUrl,
    [string]$Destination
) {
    $checksumPattern = "(?m)^([0-9a-fA-F]{64})\s+\*?" + [regex]::Escape($AssetPath) + "\s*$"
    $checksumMatch = [regex]::Match($ChecksumText, $checksumPattern)
    if (-not $checksumMatch.Success) {
        throw "Checksum entry for $AssetPath is missing from $ChecksumsUrl."
    }
    Invoke-WebRequest -UseBasicParsing -Uri "$Base/$AssetPath" -OutFile $Destination
    $expectedHash = $checksumMatch.Groups[1].Value.ToUpperInvariant()
    $actualHash = (Get-FileHash -LiteralPath $Destination -Algorithm SHA256).Hash
    if ($actualHash -ne $expectedHash) {
        throw "Checksum mismatch for $AssetPath."
    }
}

$version = [Environment]::OSVersion.Version
if ($version.Major -lt 10 -or $version.Build -lt 19045) {
    throw "Gesta Agent RC requires Windows 10 22H2 or newer."
}

$architecture = if ($env:PROCESSOR_ARCHITEW6432) {
    $env:PROCESSOR_ARCHITEW6432
} else {
    $env:PROCESSOR_ARCHITECTURE
}
if ($architecture -ne "AMD64") {
    throw "Gesta Agent RC supports windows/amd64 only; detected $architecture."
}

$parsedControlUrl = $null
if (-not [Uri]::TryCreate($ControlUrl, [UriKind]::Absolute, [ref]$parsedControlUrl) -or
    $parsedControlUrl.Scheme -notin @("http", "https")) {
    throw "ControlUrl must be an absolute HTTP or HTTPS URL."
}

$dataDir = Join-Path $env:USERPROFILE ".gesta"
if ([string]::IsNullOrWhiteSpace($InstallDir)) {
    $InstallDir = Join-Path $dataDir "bin"
}
$agentPath = Join-Path $InstallDir "gesta-agent.exe"
$hookPath = Join-Path $InstallDir "gesta-agent-hook-launcher.exe"
$taskName = "Gesta Agent"
$assetName = "gesta-agent-windows-amd64.exe"
$hookAssetName = "gesta-agent-hook-launcher-windows-amd64.exe"
$assetPath = "bin/$assetName"
$hookAssetPath = "bin/$hookAssetName"
$base = $BaseUrl.TrimEnd("/")
$checksumsUrl = "$base/SHA256SUMS"
$tempBinary = Join-Path ([IO.Path]::GetTempPath()) "gesta-agent-$PID.exe"
$tempHookBinary = Join-Path ([IO.Path]::GetTempPath()) "gesta-agent-hook-launcher-$PID.exe"
$tempChecksums = Join-Path ([IO.Path]::GetTempPath()) "gesta-agent-checksums-$PID.txt"

try {
    New-Item -ItemType Directory -Path $dataDir -Force | Out-Null
    Set-PrivateDirectoryAcl -Path $dataDir
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null

    Write-Step "Downloading Windows agent bundle"
    Invoke-WebRequest -UseBasicParsing -Uri $checksumsUrl -OutFile $tempChecksums
    $checksumText = Get-Content -LiteralPath $tempChecksums -Raw
    Get-VerifiedArtifact -Base $base -AssetPath $assetPath -ChecksumText $checksumText `
        -ChecksumsUrl $checksumsUrl -Destination $tempBinary
    Get-VerifiedArtifact -Base $base -AssetPath $hookAssetPath -ChecksumText $checksumText `
        -ChecksumsUrl $checksumsUrl -Destination $tempHookBinary
    Write-Success "Checksum verified"

    Stop-InstalledAgent -AgentPath $agentPath -TaskName $taskName
    Copy-Item -LiteralPath $tempBinary -Destination $agentPath -Force
    Copy-Item -LiteralPath $tempHookBinary -Destination $hookPath -Force

    Write-Step "Configuring Gesta hooks and state"
    & $agentPath install --control-url $ControlUrl --apikey $ApiKey --agent-bin $hookPath --data-dir $dataDir
    if ($LASTEXITCODE -ne 0) {
        throw "gesta-agent install failed with exit code $LASTEXITCODE."
    }

    if ($NoDaemon) {
        Disable-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue | Out-Null
        Write-Success "Background task disabled"
    } else {
        Write-Step "Registering current-user scheduled task"
        $currentUser = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
        $escapedControlUrl = $ControlUrl.Replace('"', '\"')
        $escapedDataDir = $dataDir.Replace('"', '\"')
        $actionArguments = "run --control-url `"$escapedControlUrl`" --data-dir `"$escapedDataDir`" --interval 1m --usage-window 10m"
        $action = New-ScheduledTaskAction -Execute $agentPath -Argument $actionArguments -WorkingDirectory $dataDir
        $trigger = New-ScheduledTaskTrigger -AtLogOn -User $currentUser
        $principal = New-ScheduledTaskPrincipal -UserId $currentUser -LogonType Interactive -RunLevel Limited
        $settings = New-ScheduledTaskSettingsSet `
            -RestartCount 999 `
            -RestartInterval (New-TimeSpan -Minutes 1) `
            -ExecutionTimeLimit ([TimeSpan]::Zero) `
            -MultipleInstances IgnoreNew `
            -AllowStartIfOnBatteries `
            -DontStopIfGoingOnBatteries
        Register-ScheduledTask `
            -TaskName $taskName `
            -Action $action `
            -Trigger $trigger `
            -Principal $principal `
            -Settings $settings `
            -Description "Runs the per-user Gesta Agent." `
            -Force | Out-Null
        Start-ScheduledTask -TaskName $taskName
        & $agentPath status --data-dir $dataDir --require-running --wait 15s
        if ($LASTEXITCODE -ne 0) {
            throw "The Gesta Agent scheduled task started but the runtime did not become healthy."
        }
        Write-Success "Scheduled task healthy: $taskName"
    }

    Write-Success "Gesta Agent installed"
    & $agentPath version
    if ($NoDaemon) {
        & $agentPath status --data-dir $dataDir
    }
    Write-Host "Status command: & '$agentPath' status --data-dir '$dataDir'"
} finally {
    if (Test-Path -LiteralPath $tempBinary) {
        Remove-Item -LiteralPath $tempBinary -Force -ErrorAction SilentlyContinue
    }
    if (Test-Path -LiteralPath $tempHookBinary) {
        Remove-Item -LiteralPath $tempHookBinary -Force -ErrorAction SilentlyContinue
    }
    if (Test-Path -LiteralPath $tempChecksums) {
        Remove-Item -LiteralPath $tempChecksums -Force -ErrorAction SilentlyContinue
    }
}
