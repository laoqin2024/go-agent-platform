<#
.SYNOPSIS
  Batch deploy GoAgent on Windows via PowerShell Remoting + NSSM.

.DESCRIPTION
  - Copies local go-agent.exe to remote "C:\Program Files\GoAgent\go-agent.exe" using Copy-Item -ToSession
  - Ensures NSSM is present (download/copy from internal path) and registers/updates a Windows Service
  - Adds an outbound firewall allow rule for the agent executable
  - Idempotent: if service exists AND remote binary hash equals local hash, it skips reinstall and only ensures service is Running

.NOTES
  Common rollout pitfalls (handled best-effort by this script):
  - WinRM/PSRemoting must be enabled on targets (domain environment: recommend GPO to enable WinRM + firewall rules)
    Quick check: Test-WSMan <host>
  - ExecutionPolicy may block running scripts on the controller machine:
    Recommended launch: powershell.exe -ExecutionPolicy Bypass -File .\deploy_agent.ps1 ...
  - Unsigned go-agent.exe may be quarantined by Windows Defender:
    This script will try to add an exclusion for "C:\Program Files\GoAgent\" on the target via Add-MpPreference (admin required).

.PARAMETER ServerAddr
  Backend ingest address. Note: current agent flag is "-api-url". This script maps ServerAddr -> -api-url.

.PARAMETER ControlToken
  Control channel shared token, passed as "-control-token".

.PARAMETER TargetServers
  Array of target server names/IPs.

.PARAMETER TargetServersFile
  Optional file path containing one server per line.

.PARAMETER LocalExePath
  Local agent exe path. Default: ".\dist\agent-windows-amd64.exe"

.PARAMETER ServiceName
  Windows service name. Default: "go-agent"

.PARAMETER NssmSource
  Internal NSSM source. Can be a UNC path (\\share\nssm.exe) or an HTTP(S) URL.
  Default: "\\intranet\tools\nssm\nssm.exe" (change to your environment).

.PARAMETER Credential
  Optional credential for New-PSSession.

.EXAMPLE
  .\deploy_agent.ps1 -ServerAddr "http://10.0.0.2:8080/ingest" -ControlToken "xxx" -TargetServers @("win1","win2")

.EXAMPLE
  .\deploy_agent.ps1 -ServerAddr "http://10.0.0.2:8080/ingest" -ControlToken "xxx" -TargetServersFile ".\servers.txt"
#>

[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)]
  [string]$ServerAddr,

  [Parameter(Mandatory = $true)]
  [string]$ControlToken,

  [Parameter(Mandatory = $false)]
  [string[]]$TargetServers = @(),

  [Parameter(Mandatory = $false)]
  [string]$TargetServersFile = "",

  [Parameter(Mandatory = $false)]
  [string]$LocalExePath = ".\dist\agent-windows-amd64.exe",

  [Parameter(Mandatory = $false)]
  [string]$ServiceName = "go-agent",

  [Parameter(Mandatory = $false)]
  [string]$NssmSource = "\\intranet\tools\nssm\nssm.exe",

  [Parameter(Mandatory = $false)]
  [System.Management.Automation.PSCredential]$Credential
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Write-Info([string]$msg) { Write-Host "[INFO] $msg" -ForegroundColor Cyan }
function Write-Warn([string]$msg) { Write-Host "[WARN] $msg" -ForegroundColor Yellow }
function Write-Err([string]$msg)  { Write-Host "[ERR ] $msg" -ForegroundColor Red }

function Show-ExecutionPolicyWarning {
  try {
    $pol = Get-ExecutionPolicy -Scope Process
    if ($pol -eq "Undefined") {
      $pol = Get-ExecutionPolicy -Scope CurrentUser
    }
    if ($pol -eq "Undefined") {
      $pol = Get-ExecutionPolicy -Scope LocalMachine
    }
    if ($pol -in @("Restricted", "AllSigned")) {
      Write-Warn "ExecutionPolicy=$pol may block script execution. Recommended: powershell.exe -ExecutionPolicy Bypass -File .\\deploy_agent.ps1 ..."
    }
  } catch {
    # ignore
  }
}

function Get-Targets {
  $targets = @()
  if ($TargetServersFile -and (Test-Path -LiteralPath $TargetServersFile)) {
    $lines = Get-Content -LiteralPath $TargetServersFile -ErrorAction Stop
    foreach ($ln in $lines) {
      $s = ($ln -as [string]).Trim()
      if ($s -and -not $s.StartsWith("#")) { $targets += $s }
    }
  }
  if ($TargetServers -and @($TargetServers).Count -gt 0) {
    $targets += @($TargetServers)
  }
  $targets = @($targets | ForEach-Object { ($_ -as [string]).Trim() } | Where-Object { $_ } | Select-Object -Unique)
  return ,$targets
}

function Get-LocalHash([string]$path) {
  if (-not (Test-Path -LiteralPath $path)) {
    throw "Local exe not found: $path"
  }
  return (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Test-RemotingReady {
  param([string]$target)
  try {
    Test-WSMan -ComputerName $target -ErrorAction Stop | Out-Null
    return $true
  } catch {
    return $false
  }
}

function New-Session([string]$target) {
  # Keep options minimal for compatibility across different PowerShell versions/environments.
  $opt = New-PSSessionOption
  if ($PSBoundParameters.ContainsKey("Credential")) {
    return New-PSSession -ComputerName $target -Credential $Credential -SessionOption $opt
  }
  return New-PSSession -ComputerName $target -SessionOption $opt
}

function Initialize-RemoteDir {
  param([System.Management.Automation.Runspaces.PSSession]$Session, [string]$Dir)
  Invoke-Command -Session $Session -ScriptBlock {
    param($d)
    if (-not (Test-Path -LiteralPath $d)) {
      New-Item -ItemType Directory -Path $d -Force | Out-Null
    }
  } -ArgumentList $Dir
}

function Set-DefenderExclusion {
  param([System.Management.Automation.Runspaces.PSSession]$Session, [string]$Path)
  Invoke-Command -Session $Session -ScriptBlock {
    param($p)
    try {
      # Requires admin; may be blocked by enterprise policy. Best-effort only.
      Add-MpPreference -ExclusionPath $p -ErrorAction Stop | Out-Null
      return @{ ok = $true; msg = "added" }
    } catch {
      return @{ ok = $false; msg = $_.Exception.Message }
    }
  } -ArgumentList $Path
}

function Install-Nssm {
  param(
    [System.Management.Automation.Runspaces.PSSession]$Session,
    [string]$RemoteDir,
    [string]$NssmSourcePath
  )
  $remoteNssm = Join-Path $RemoteDir "nssm.exe"
  $hasNssm = Invoke-Command -Session $Session -ScriptBlock {
    param($p)
    if (Test-Path -LiteralPath $p) { return $true }
    $cmd = Get-Command "nssm.exe" -ErrorAction SilentlyContinue
    return [bool]$cmd
  } -ArgumentList $remoteNssm

  if ($hasNssm) {
    return $remoteNssm
  }

  Write-Info "NSSM not found on remote, installing from: $NssmSourcePath"
  Invoke-Command -Session $Session -ScriptBlock {
    param($dst, $src)
    if ($src -match '^https?://') {
      try {
        Invoke-WebRequest -Uri $src -OutFile $dst -UseBasicParsing -ErrorAction Stop
      } catch {
        throw "Failed to download nssm.exe from URL: $src. Error: $($_.Exception.Message)"
      }
    } else {
      if (-not (Test-Path -LiteralPath $src)) {
        throw "NssmSource not found (UNC/path): $src"
      }
      Copy-Item -LiteralPath $src -Destination $dst -Force
    }
    if (-not (Test-Path -LiteralPath $dst)) {
      throw "nssm.exe install failed, missing: $dst"
    }
  } -ArgumentList $remoteNssm, $NssmSourcePath

  return $remoteNssm
}

function Get-RemoteHash {
  param([System.Management.Automation.Runspaces.PSSession]$Session, [string]$RemoteExe)
  return Invoke-Command -Session $Session -ScriptBlock {
    param($p)
    if (-not (Test-Path -LiteralPath $p)) { return "" }
    return (Get-FileHash -LiteralPath $p -Algorithm SHA256).Hash.ToLowerInvariant()
  } -ArgumentList $RemoteExe
}

function Set-FirewallRule {
  param([System.Management.Automation.Runspaces.PSSession]$Session, [string]$RuleName, [string]$ProgramPath)
  Invoke-Command -Session $Session -ScriptBlock {
    param($name, $prog)
    $rule = Get-NetFirewallRule -DisplayName $name -ErrorAction SilentlyContinue
    if (-not $rule) {
      New-NetFirewallRule -DisplayName $name -Direction Outbound -Program $prog -Action Allow -Profile Any | Out-Null
    } else {
      # Best-effort: ensure it points to current program path
      $pf = Get-NetFirewallApplicationFilter -AssociatedNetFirewallRule $rule -ErrorAction SilentlyContinue
      if ($pf -and $pf.Program -ne $prog) {
        Set-NetFirewallRule -DisplayName $name -Program $prog | Out-Null
      }
    }
  } -ArgumentList $RuleName, $ProgramPath
}

function Set-ServiceNssm {
  param(
    [System.Management.Automation.Runspaces.PSSession]$Session,
    [string]$NssmPath,
    [string]$Service,
    [string]$ExePath,
    [string]$SvcArgs
  )

  Invoke-Command -Session $Session -ScriptBlock {
    param($nssm, $svc, $exe, $svcArgs)

    $exists = $false
    try {
      $null = Get-Service -Name $svc -ErrorAction Stop
      $exists = $true
    } catch {
      $exists = $false
    }

    if (-not $exists) {
      & $nssm install $svc $exe | Out-Null
    }

    # Always enforce current config (idempotent)
    & $nssm set $svc Application $exe | Out-Null
    & $nssm set $svc AppParameters $svcArgs | Out-Null
    & $nssm set $svc Start SERVICE_AUTO_START | Out-Null

    # Some sane defaults
    & $nssm set $svc AppStdout "C:\\Program Files\\GoAgent\\logs\\stdout.log" | Out-Null
    & $nssm set $svc AppStderr "C:\\Program Files\\GoAgent\\logs\\stderr.log" | Out-Null
    & $nssm set $svc AppRotateFiles 1 | Out-Null
    & $nssm set $svc AppRotateOnline 1 | Out-Null
    & $nssm set $svc AppRotateSeconds 86400 | Out-Null
    & $nssm set $svc AppRotateBytes 10485760 | Out-Null
  } -ArgumentList $NssmPath, $Service, $ExePath, $SvcArgs
}

function Start-ServiceRunning {
  param([System.Management.Automation.Runspaces.PSSession]$Session, [string]$Service)
  Invoke-Command -Session $Session -ScriptBlock {
    param($svc)
    Set-Service -Name $svc -StartupType Automatic -ErrorAction SilentlyContinue
    Start-Service -Name $svc -ErrorAction SilentlyContinue
    $s = Get-Service -Name $svc -ErrorAction Stop
    if ($s.Status -ne "Running") {
      throw "Service '$svc' is not running (status=$($s.Status))"
    }
  } -ArgumentList $Service
}

$targets = Get-Targets
if (-not $targets -or $targets.Count -eq 0) {
  throw "No targets specified. Use -TargetServers or -TargetServersFile."
}

Show-ExecutionPolicyWarning

$localHash = Get-LocalHash $LocalExePath
Write-Info "Local exe: $LocalExePath"
Write-Info "Local SHA256: $localHash"
Write-Info "Targets: $($targets -join ', ')"

foreach ($t in $targets) {
  Write-Info "---- Deploy to: $t ----"
  $session = $null
  try {
    if (-not (Test-RemotingReady -target $t)) {
      Write-Err "FAILED: $t => WinRM/WSMan not reachable. Ensure target has Enable-PSRemoting enabled (domain: recommend GPO) and firewall allows WinRM."
      continue
    }
    $session = New-Session $t

    $remoteBase = "C:\Program Files\GoAgent"
    $remoteExe = Join-Path $remoteBase "go-agent.exe"
    $remoteLogs = Join-Path $remoteBase "logs"

    Initialize-RemoteDir -Session $session -Dir $remoteBase
    Initialize-RemoteDir -Session $session -Dir $remoteLogs

    # Best-effort: avoid Defender quarantining unsigned binaries
    $mp = Set-DefenderExclusion -Session $session -Path $remoteBase
    if ($mp -and $mp.ok -ne $true) {
      Write-Warn "Defender exclusion not applied on ${t}: $($mp.msg)"
    }

    $remoteHash = Get-RemoteHash -Session $session -RemoteExe $remoteExe
    $svcExists = Invoke-Command -Session $session -ScriptBlock {
      param($svc)
      try { $null = Get-Service -Name $svc -ErrorAction Stop; return $true } catch { return $false }
    } -ArgumentList $ServiceName

    if ($svcExists -and $remoteHash -and ($remoteHash -eq $localHash)) {
      Write-Info "Service exists and binary hash matches; skip install. Ensuring Running..."
    } else {
      Write-Info "Installing/upgrading binary (remoteHash=$remoteHash)"
      Copy-Item -LiteralPath $LocalExePath -Destination $remoteExe -ToSession $session -Force
    }

    $nssmPath = Install-Nssm -Session $session -RemoteDir $remoteBase -NssmSourcePath $NssmSource

    # Map ServerAddr -> -api-url (agent uses -api-url as ingest endpoint)
    $deviceId = $t
    $svcArgs = "-api-url `"$ServerAddr`" -control-token `"$ControlToken`" -device-id `"$deviceId`" -data-dir `"$remoteBase\\data`""

    Initialize-RemoteDir -Session $session -Dir (Join-Path $remoteBase "data")

    Set-ServiceNssm -Session $session -NssmPath $nssmPath -Service $ServiceName -ExePath $remoteExe -SvcArgs $svcArgs
    Set-FirewallRule -Session $session -RuleName "GoAgent Outbound" -ProgramPath $remoteExe
    Start-ServiceRunning -Session $session -Service $ServiceName

    Write-Info "OK: $t"
  } catch {
    Write-Err "FAILED: $t => $($_.Exception.Message)"
  } finally {
    if ($session) {
      try { Remove-PSSession -Session $session -ErrorAction SilentlyContinue } catch {}
    }
  }
}

