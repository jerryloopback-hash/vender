#Requires -Version 5.1
<#
.SYNOPSIS
Adds current-user command entry points and configures an existing silent logon task.
.DESCRIPTION
Never starts/stops the daemon, installs private configurations, or changes network
settings. Use -DryRun first. Keep the reported protected backup directory.
#>
[CmdletBinding()]
param(
    [string]$BinDir=(Join-Path $env:LOCALAPPDATA 'vender\bin'),
    [switch]$AddPath,[switch]$RegisterAppPath,[switch]$SilentTask,[switch]$HideCurrentWindow,
    [string]$ExpectedUserSid,[string]$BackupDir,[string]$RestoreFrom,[switch]$DryRun
)
$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot 'setup-lib.ps1')
if($env:OS -ne 'Windows_NT'){throw 'Windows is required.'}
$identity=[Security.Principal.WindowsIdentity]::GetCurrent(); $sid=$identity.User.Value
if($ExpectedUserSid -and $ExpectedUserSid -ne $sid){throw 'Current account is not the expected daily user.'}
$elevated=(New-Object Security.Principal.WindowsPrincipal($identity)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
$envKey='Environment'; $appKey='Software\Microsoft\Windows\CurrentVersion\App Paths\vender.exe'
$mutex=New-Object Threading.Mutex($false,('Local\vender-windows-setup-'+$sid))
$locked=$false
try {
    try{$locked=$mutex.WaitOne(0)}catch [Threading.AbandonedMutexException] {$locked=$true}
    if(!$locked){throw 'Another setup for this user is running.'}
    $folder=$null
    if($RestoreFrom){
        if($AddPath -or $RegisterAppPath -or $SilentTask -or $HideCurrentWindow){throw 'RestoreFrom cannot be combined with setup operations.'}
        $file=Join-Path ([IO.Path]::GetFullPath($RestoreFrom)) 'state.json'
        $state=Get-Content -LiteralPath $file -Raw | ConvertFrom-Json
        if($state.Schema -ne 1 -or $state.SID -ne $sid){throw 'Backup schema or identity differs.'}
        if($state.TaskChanged){if(!$elevated){throw 'Task restoration requires the same daily user elevated.'};$service=New-Object -ComObject 'Schedule.Service';$service.Connect();$folder=$service.GetFolder('\vender')}
        if($DryRun){[pscustomobject]@{RestoreFrom=$RestoreFrom;SID=$sid;DryRun=$true};return}
        Restore-VenderSetupState $state $envKey $appKey $folder
        Initialize-VenderNative; [VenderSetupNative]::Refresh()
        Write-Output 'Restored only setup-owned PATH, App Paths and task changes. Daemon was not restarted; an already hidden current window stays hidden.'
        return
    }
    if(!$AddPath -and !$RegisterAppPath -and !$SilentTask -and !$HideCurrentWindow){throw 'Select AddPath, RegisterAppPath, SilentTask or HideCurrentWindow.'}
    if(($SilentTask -or $HideCurrentWindow) -and !$elevated){throw 'Use the original daily account elevated for task/window changes, not another administrator.'}
    if([Environment]::OSVersion.Version.Build -lt 22000){throw 'This setup supports Windows 11; do not assume older Windows compatibility.'}
    $arch=[Environment]::GetEnvironmentVariable('PROCESSOR_ARCHITEW6432');if(!$arch){$arch=$env:PROCESSOR_ARCHITECTURE}
    if($arch -ne 'AMD64'){throw 'Published/tested artifacts are native Windows amd64. ARM64 needs a separately verified build and engine.'}
    if(![IO.Path]::IsPathRooted($BinDir)){throw 'BinDir must be absolute.'}
    $BinDir=[IO.Path]::GetFullPath($BinDir).TrimEnd('\')
    $cli=Join-Path $BinDir 'vender.exe'; $helper=Join-Path $BinDir 'vender-daemon.exe'
    if(!(Test-Path -LiteralPath $cli -PathType Leaf)){throw 'Install the verified Windows vender.exe first.'}
    if((Get-VenderPE $cli).Machine -ne 0x8664 -or (Get-VenderPE $cli).Subsystem -ne 3){throw 'Expected amd64 console vender.exe, preserving the interactive TUI.'}
    if($SilentTask){
        $pe=Get-VenderPE $helper
        if($pe.Machine -ne 0x8664 -or $pe.Subsystem -ne 2){throw 'Expected matching amd64 GUI companion; build with -H=windowsgui.'}
        Test-VenderBinPermissions $BinDir $sid
    }
    $pathBefore=Get-VenderRegistryValue $envKey 'Path'
    $nextPath=Add-VenderPathEntry ([string]$pathBefore.Value) $BinDir
    $appBefore=Get-VenderRegistryValue $appKey ''
    $appObject=[Microsoft.Win32.Registry]::CurrentUser.OpenSubKey($appKey)
    try{$appExists=$null -ne $appObject}finally{if($appObject){$appObject.Dispose()}}
    if($RegisterAppPath -and $appExists -and (!$appBefore.Exists -or (ConvertTo-VenderPath $appBefore.Value) -ne (ConvertTo-VenderPath $cli))){throw 'Existing App Paths belongs to an unknown registration; refused.'}
    $taskBefore=''; $taskAfter=''; $security=''; $nextTask=''
    if($SilentTask){
        $service=New-Object -ComObject 'Schedule.Service';$service.Connect();$folder=$service.GetFolder('\vender')
        $task=$folder.GetTask('daemon');$taskBefore=$task.Xml;$security=$task.GetSecurityDescriptor(7)
        $nextTask=Get-VenderSilentTaskXml $taskBefore $BinDir $sid
    }
    $daemon=$null;$core=$null
    if($HideCurrentWindow){
        $daemons=@(Get-CimInstance Win32_Process | Where-Object {$_.ExecutablePath -eq $cli -and $_.CommandLine -match '(?:^|\s)daemon(?:\s|$)'})
        if($daemons.Count -ne 1){throw 'Expected one daemon from this exact installation.'}
        $daemon=$daemons[0]
        if((Invoke-CimMethod -InputObject $daemon -MethodName GetOwnerSid).Sid -ne $sid){throw 'Daemon belongs to another account.'}
        $cores=@(Get-CimInstance Win32_Process | Where-Object {$_.ParentProcessId -eq $daemon.ProcessId -and $_.ExecutablePath -eq (Join-Path $BinDir 'sing-box.exe')})
        if($cores.Count -ne 1){throw 'Expected one managed engine from this installation.'};$core=$cores[0]
    }
    $plan=[pscustomobject]@{SID=$sid;BinDir=$BinDir;AddPath=[bool]($AddPath -and $nextPath -ne $pathBefore.Value);CreateAppPaths=[bool]($RegisterAppPath -and !$appExists);UpdateExistingTask=[bool]($SilentTask -and $nextTask -ne $taskBefore);HideCurrentWindow=[bool]$HideCurrentWindow;DryRun=[bool]$DryRun;DaemonRestarted=$false}
    if($DryRun){$plan;return}
    if(!$BackupDir){$BackupDir=Join-Path $env:LOCALAPPDATA ('vender\setup-backups\'+(Get-Date -Format 'yyyyMMdd-HHmmss')+'-'+[guid]::NewGuid().ToString('N').Substring(0,8))}
    $BackupDir=[IO.Path]::GetFullPath($BackupDir);New-VenderPrivateDirectory $BackupDir $sid
    $state=[pscustomobject]@{Schema=1;SID=$sid;BinDir=$BinDir;PathBefore=$pathBefore;PathAdded=$false;AppChanged=$false;TaskChanged=$false;TaskBefore=$taskBefore;TaskAfter='';TaskSecurity=$security;WindowHidden=$false;Time=(Get-Date -Format o)}
    $statePath=Join-Path $BackupDir 'state.json';Save-VenderSetupState $state $statePath
    try {
        if($plan.AddPath){$state.PathAdded=$true;Save-VenderSetupState $state $statePath;Set-VenderRegistryValue $envKey 'Path' $nextPath $pathBefore.Kind}
        if($plan.CreateAppPaths){$state.AppChanged=$true;Save-VenderSetupState $state $statePath;Set-VenderRegistryValue $appKey '' $cli 'String';Set-VenderRegistryValue $appKey 'Path' $BinDir 'String'}
        if($plan.UpdateExistingTask){
            # Write the intended XML before registration, then capture Windows' canonical form.
            $state.TaskChanged=$true;$state.TaskAfter=$nextTask;Save-VenderSetupState $state $statePath
            $null=$folder.RegisterTask('daemon',$nextTask,4,$sid,$null,3,$security)
            $state.TaskAfter=$folder.GetTask('daemon').Xml;Save-VenderSetupState $state $statePath
        }
        Initialize-VenderNative;[VenderSetupNative]::Refresh()
        if($HideCurrentWindow){
            # Attaching to another console must never detach the user's shell.
            # Isolate FreeConsole/AttachConsole in a short-lived owned process.
            $resultPath=Join-Path $BackupDir 'hide-result.json'
            $worker=Join-Path $PSScriptRoot 'hide-console.ps1'
            $powershell=Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
            $arguments=@('-NoProfile','-NonInteractive','-File',('"'+$worker+'"'),'-DaemonPid',"$($daemon.ProcessId)",'-CorePid',"$($core.ProcessId)",'-ExpectedExe',('"'+$cli+'"'),'-ExpectedUserSid',$sid,'-ExpectedCreatedTicks',"$($daemon.CreationDate.ToUniversalTime().Ticks)",'-ResultPath',('"'+$resultPath+'"'))
            $child=Start-Process -FilePath $powershell -ArgumentList $arguments -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $BackupDir 'hide-worker.stdout.log') -RedirectStandardError (Join-Path $BackupDir 'hide-worker.stderr.log')
            if(!$child.WaitForExit(20000)){
                $owned=Get-CimInstance Win32_Process -Filter "ProcessId=$($child.Id)"
                if($owned.ExecutablePath -eq $powershell -and $owned.CommandLine.Contains($worker)){$child.Kill()}
                throw 'Console worker timed out; caller console was not detached.'
            }
            if($child.ExitCode -ne 0 -or !(Test-Path -LiteralPath $resultPath)){throw 'Console worker refused or failed; inspect the protected worker result.'}
            $result=Get-Content -LiteralPath $resultPath -Raw | ConvertFrom-Json
            if(!$result.Success){throw 'Console worker did not report success.'}
            $state.WindowHidden=[bool]$result.Changed;Save-VenderSetupState $state $statePath
        }
    } catch {
        $message=$_.Exception.Message
        try{Restore-VenderSetupState $state $envKey $appKey $folder}catch{throw "Setup failed: $message. Guarded restore also needs inspection: $($_.Exception.Message). Backup: $BackupDir"}
        throw "Setup failed; guarded changes restored: $message. Backup: $BackupDir"
    }
    [pscustomobject]@{BackupDir=$BackupDir;SID=$sid;BinDir=$BinDir;PATHChanged=$state.PathAdded;AppPathsChanged=$state.AppChanged;TaskChanged=$state.TaskChanged;WindowHidden=$state.WindowHidden;DaemonRestarted=$false;NewLogonNotVerified=$true;Note='Reopen old terminals or refresh their process PATH. App Paths alone is not a guarantee in old/elevated shells. Win+R full-path fallback remains available.'}
} finally {if($locked){$mutex.ReleaseMutex()};$mutex.Dispose()}
