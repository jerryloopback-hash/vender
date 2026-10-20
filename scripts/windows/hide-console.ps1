#Requires -Version 5.1
# Internal worker. Only this process may detach/attach consoles, never setup's caller.
param(
    [Parameter(Mandatory=$true)][ValidateRange(1,2147483647)][int]$DaemonPid,
    [Parameter(Mandatory=$true)][ValidateRange(1,2147483647)][int]$CorePid,
    [Parameter(Mandatory=$true)][string]$ExpectedExe,
    [Parameter(Mandatory=$true)][string]$ExpectedUserSid,
    [Parameter(Mandatory=$true)][long]$ExpectedCreatedTicks,
    [Parameter(Mandatory=$true)][string]$ResultPath
)
$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot 'setup-lib.ps1')
try {
    $sid=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    if($sid -ne $ExpectedUserSid -or ![IO.Path]::IsPathRooted($ResultPath)){throw 'Worker identity or result path differs.'}
    $daemon=Get-CimInstance Win32_Process -Filter "ProcessId=$DaemonPid" -ErrorAction Stop
    $core=Get-CimInstance Win32_Process -Filter "ProcessId=$CorePid" -ErrorAction Stop
    if(!$daemon -or !$core -or $daemon.ExecutablePath -ne $ExpectedExe -or $daemon.CreationDate.ToUniversalTime().Ticks -ne $ExpectedCreatedTicks -or $daemon.CommandLine -notmatch '(?:^|\s)daemon(?:\s|$)'){throw 'Daemon checkpoint no longer matches.'}
    if((Invoke-CimMethod -InputObject $daemon -MethodName GetOwnerSid).Sid -ne $sid){throw 'Daemon owner differs.'}
    if($core.ParentProcessId -ne $DaemonPid -or $core.ExecutablePath -ne (Join-Path ([IO.Path]::GetDirectoryName($ExpectedExe)) 'sing-box.exe')){throw 'Engine checkpoint no longer matches.'}
    Initialize-VenderNative
    $handle=[VenderSetupNative]::Console([uint32]$DaemonPid,[uint32]$CorePid,[uint32]$PID,$true,0)
    [pscustomobject]@{Success=$true;Changed=($handle -ne 0);DaemonPid=$DaemonPid} | ConvertTo-Json | Set-Content -LiteralPath $ResultPath -Encoding UTF8
} catch {
    if([IO.Path]::IsPathRooted($ResultPath)){
        [pscustomobject]@{Success=$false;Error=$_.Exception.Message} | ConvertTo-Json | Set-Content -LiteralPath $ResultPath -Encoding UTF8
    }
    exit 1
}
