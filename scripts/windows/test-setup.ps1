#Requires -Version 5.1
# Isolated tests: no production PATH, App Paths, task or network modifications.
param([string]$TestBinDir)
$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot 'setup-lib.ps1')
$checks=0
function Assert-Vender($Condition,[string]$Message){if(!$Condition){throw $Message};$script:checks++}
function Assert-VenderThrows([scriptblock]$Action,[string]$Message){$threw=$false;try{& $Action}catch{$threw=$true};Assert-Vender $threw $Message}
$sid=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$bin='C:\vender-test\bin'
Assert-Vender ((Add-VenderPathEntry 'C:\one;C:\two' $bin) -eq 'C:\one;C:\two;C:\vender-test\bin') 'PATH append'
Assert-Vender ((Add-VenderPathEntry 'C:\one;C:\VENDER-test\bin\;' $bin) -eq 'C:\one;C:\VENDER-test\bin\;') 'PATH idempotence/case/trailing separator'
Assert-Vender ((Remove-VenderPathEntry 'C:\one;C:\vender-test\bin;C:\later' $bin) -eq 'C:\one;C:\later') 'Preserve later PATH edits'
$originalEnv=$env:VENDER_TEST_PATH_ROOT
try{$env:VENDER_TEST_PATH_ROOT='C:\vender-test';Assert-Vender ((Add-VenderPathEntry '%VENDER_TEST_PATH_ROOT%\bin' $bin) -eq '%VENDER_TEST_PATH_ROOT%\bin') 'Expandable PATH match'}finally{$env:VENDER_TEST_PATH_ROOT=$originalEnv}
$root='Software\vender\SetupTests\'+[guid]::NewGuid().ToString('N')
$envKey=$root+'\Environment';$appKey=$root+'\App Paths\vender.exe'
try {
    Assert-Vender (!(Get-VenderRegistryValue $envKey 'Path').Exists) 'Missing key is safe'
    Set-VenderRegistryValue $envKey 'Other' 'preserve' 'String'
    Assert-Vender (!(Get-VenderRegistryValue $envKey 'Path').Exists) 'Missing value is safe'
    Remove-VenderRegistryValue $envKey 'Path'
    Set-VenderRegistryValue $envKey 'Path' '%TEMP%;C:\one' 'ExpandString'
    $before=Get-VenderRegistryValue $envKey 'Path'
    Assert-Vender ($before.Kind -eq 'ExpandString' -and $before.Value -eq '%TEMP%;C:\one') 'Keep unexpanded value and kind'
    Set-VenderRegistryValue $envKey 'Path' ((Add-VenderPathEntry $before.Value $bin)+';C:\later') 'ExpandString'
    Set-VenderRegistryValue $appKey '' (Join-Path $bin 'vender.exe') 'String'
    Set-VenderRegistryValue $appKey 'Path' $bin 'String'
    $state=[pscustomobject]@{TaskChanged=$false;AppChanged=$true;PathAdded=$true;BinDir=$bin;SID=$sid;PathBefore=$before}
    Restore-VenderSetupState $state $envKey $appKey
    Assert-Vender ((Get-VenderRegistryValue $envKey 'Path').Value -eq '%TEMP%;C:\one;C:\later') 'Restore retains unrelated PATH additions'
    Assert-Vender ((Get-VenderRegistryValue $envKey 'Other').Value -eq 'preserve') 'Unrelated registry value retained'
    Assert-Vender (!(Get-VenderRegistryValue $appKey '').Exists) 'Owned App Paths removed'
    Remove-VenderRegistryValue $envKey 'Path'
    Restore-VenderSetupState $state $envKey $appKey
    Assert-Vender (!(Get-VenderRegistryValue $envKey 'Path').Exists) 'Repeat restore with missing values is safe'
    Set-VenderRegistryValue $envKey 'Path' (Add-VenderPathEntry 'C:\one' $bin) 'String'
    Set-VenderRegistryValue $appKey '' 'C:\another\app.exe' 'String'
    Assert-VenderThrows {Restore-VenderSetupState $state $envKey $appKey} 'Refuse foreign App Paths'
    Assert-Vender ((Get-VenderRegistryValue $envKey 'Path').Value -eq 'C:\one') 'App Paths conflict does not prevent independent PATH restoration'
    Assert-Vender ((Get-VenderRegistryValue $appKey '').Value -eq 'C:\another\app.exe') 'Foreign App Paths untouched'
} finally {[Microsoft.Win32.Registry]::CurrentUser.DeleteSubKeyTree($root,$false)}
$xml=@"
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task"><RegistrationInfo><URI>\vender\daemon</URI></RegistrationInfo><Triggers><LogonTrigger><UserId>$sid</UserId></LogonTrigger></Triggers><Principals><Principal><UserId>$sid</UserId><LogonType>InteractiveToken</LogonType><RunLevel>HighestAvailable</RunLevel></Principal></Principals><Settings><Hidden>true</Hidden></Settings><Actions><Exec><Command>$bin\vender.exe</Command><Arguments>daemon --config-dir "C:\config path" --run-dir "C:\run path"</Arguments><WorkingDirectory>$bin\</WorkingDirectory></Exec></Actions></Task>
"@
$changed=Get-VenderSilentTaskXml $xml $bin $sid
[xml]$doc=$changed
Assert-Vender ($doc.Task.Actions.Exec.Command -eq "$bin\vender-daemon.exe") 'GUI companion selected'
Assert-Vender ($doc.Task.Principals.Principal.RunLevel -eq 'HighestAvailable') 'Privilege preserved'
Assert-Vender ($doc.Task.Actions.Exec.Arguments -eq 'daemon --config-dir "C:\config path" --run-dir "C:\run path"') 'Quoted arguments preserved'
Assert-Vender ((Get-VenderSilentTaskXml $changed $bin $sid) -eq $changed) 'Task conversion idempotent'
Assert-VenderThrows {Get-VenderSilentTaskXml ($xml.Replace("$bin\vender.exe",'C:\foreign\vender.exe')) $bin $sid} 'Reject foreign task target'
Assert-VenderThrows {Get-VenderSilentTaskXml ($xml.Replace('daemon --config-dir','tui --config-dir')) $bin $sid} 'Reject non-daemon action'
Assert-VenderThrows {Get-VenderSilentTaskXml ($xml.Replace('--run-dir "C:\run path"','--run-dir relative')) $bin $sid} 'Reject relative directories'
Assert-VenderThrows {Get-VenderSilentTaskXml $xml $bin 'S-1-5-21-0'} 'Reject another daily identity'
$unicodeBin='C:\'+[char]0x7528+[char]0x6237+' folder\bin'
$unicodeXml=$xml.Replace($bin,$unicodeBin)
[xml]$unicodeTask=Get-VenderSilentTaskXml $unicodeXml $unicodeBin $sid
Assert-Vender ($unicodeTask.Task.Actions.Exec.Command -eq ($unicodeBin+'\vender-daemon.exe')) 'Unicode path survives XML conversion'
$parsed=@([VenderSetupNative]::Split('daemon --config-dir "C:\space path\\"'))
Assert-Vender ($parsed.Count -eq 3 -and $parsed[2] -eq 'C:\space path\') 'Windows trailing-backslash argument parsing'
$fake=[pscustomobject]@{Xml=$changed;Writes=0}
$fake | Add-Member -MemberType ScriptMethod -Name GetTask -Value {param($name);return [pscustomobject]@{Xml=$this.Xml}}
$fake | Add-Member -MemberType ScriptMethod -Name RegisterTask -Value {param($name,$xml,$flags,$user,$password,$logon,$security);$this.Writes++;$this.Xml=$xml;return $null}
$state=[pscustomobject]@{TaskChanged=$true;AppChanged=$true;PathAdded=$true;BinDir=$bin;SID=$sid;TaskBefore=$xml;TaskAfter=$changed;TaskSecurity='';PathBefore=[pscustomobject]@{Exists=$false;Value=$null;Kind='String'}}
Restore-VenderSetupState $state ($root+'\missing-env') ($root+'\missing-app') $fake
Assert-Vender ($fake.Writes -eq 1) 'Missing registry values do not prevent task restoration'
Restore-VenderSetupState $state ($root+'\missing-env') ($root+'\missing-app') $fake
Assert-Vender ($fake.Writes -eq 1) 'Repeated task restoration is idempotent'
foreach($file in @('setup.ps1','setup-lib.ps1','hide-console.ps1')){$tokens=$null;$errors=$null;$null=[Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot $file),[ref]$tokens,[ref]$errors);Assert-Vender ($errors.Count -eq 0) "Parse $file"}
$entry=Get-Content (Join-Path $PSScriptRoot 'setup.ps1') -Raw
Assert-Vender ($entry -notmatch '\[VenderSetupNative\]::Console\(') 'Entry point never detaches caller console'
Assert-Vender ($entry -match 'hide-console.ps1' -and $entry -match 'Start-Process') 'Hide operation isolated in an owned worker'
$private=Join-Path ([IO.Path]::GetTempPath()) ('vender-setup-test-'+[guid]::NewGuid().ToString('N'))
try {
    New-VenderPrivateDirectory $private $sid
    Assert-Vender ((Get-Acl -LiteralPath $private).AreAccessRulesProtected) 'Backup ACL inheritance disabled'
    foreach($ace in (Get-Acl -LiteralPath $private).GetAccessRules($true,$true,[Security.Principal.SecurityIdentifier])){Assert-Vender ($ace.IdentityReference.Value -in @($sid,'S-1-5-18','S-1-5-32-544')) 'Backup excludes other identities'}
    Save-VenderSetupState ([pscustomobject]@{Schema=1;SID=$sid}) (Join-Path $private 'state.json')
    Assert-Vender ((Get-Content (Join-Path $private 'state.json') -Raw | ConvertFrom-Json).SID -eq $sid) 'Protected backup persisted'
    $consoleBefore=[VenderSetupNative]::CurrentConsole()
    $workerResult=Join-Path $private 'worker-result.json'
    $worker=Join-Path $PSScriptRoot 'hide-console.ps1'
    $power=Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
    $workerArgs=@('-NoProfile','-NonInteractive','-File',('"'+$worker+'"'),'-DaemonPid','2147483647','-CorePid','2147483646','-ExpectedExe',('"'+$bin+'\vender.exe"'),'-ExpectedUserSid',$sid,'-ExpectedCreatedTicks','0','-ResultPath',('"'+$workerResult+'"'))
    $child=Start-Process -FilePath $power -ArgumentList $workerArgs -WindowStyle Hidden -PassThru
    Assert-Vender ($child.WaitForExit(20000) -and $child.ExitCode -ne 0) 'Worker rejects nonexistent daemon'
    Assert-Vender (!(Get-Content $workerResult -Raw | ConvertFrom-Json).Success) 'Worker reports refusal in protected result'
    Assert-Vender ([VenderSetupNative]::CurrentConsole() -eq $consoleBefore) 'Worker does not detach caller console'
    $privateBin=Join-Path $private 'bin'
    $null=New-Item -ItemType Directory -Path $privateBin
    $acl=Get-Acl -LiteralPath $privateBin
    $reader=New-Object Security.Principal.SecurityIdentifier('S-1-5-32-545')
    $null=$acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($reader,'ReadAndExecute','ContainerInherit,ObjectInherit','None','Allow')))
    Set-Acl -LiteralPath $privateBin -AclObject $acl
    Test-VenderBinPermissions $privateBin $sid
    Assert-Vender $true 'Read-only identities are not incorrectly classified as writers'
    $null=$acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($reader,'Write','ContainerInherit,ObjectInherit','None','Allow')))
    Set-Acl -LiteralPath $privateBin -AclObject $acl
    Assert-VenderThrows {Test-VenderBinPermissions $privateBin $sid} 'Reject ordinary-user write access'
} finally {
    if(Test-Path (Join-Path $private 'state.json')){Remove-Item -LiteralPath (Join-Path $private 'state.json')}
    if(Test-Path (Join-Path $private 'worker-result.json')){Remove-Item -LiteralPath (Join-Path $private 'worker-result.json')}
    if(Test-Path (Join-Path $private 'bin')){[IO.Directory]::Delete((Join-Path $private 'bin'))}
    if(Test-Path -LiteralPath $private){[IO.Directory]::Delete($private)}
}
if($TestBinDir){
    $realPath=Get-VenderRegistryValue 'Environment' 'Path'
    $realApp=Get-VenderRegistryValue 'Software\Microsoft\Windows\CurrentVersion\App Paths\vender.exe' ''
    $null=& (Join-Path $PSScriptRoot 'setup.ps1') -BinDir $TestBinDir -AddPath -DryRun
    if($realApp.Exists -and (ConvertTo-VenderPath $realApp.Value) -ne (ConvertTo-VenderPath (Join-Path $TestBinDir 'vender.exe'))){
        Assert-VenderThrows {& (Join-Path $PSScriptRoot 'setup.ps1') -BinDir $TestBinDir -RegisterAppPath -DryRun} 'DryRun rejects conflict with a different production registration'
    }else{$null=& (Join-Path $PSScriptRoot 'setup.ps1') -BinDir $TestBinDir -RegisterAppPath -DryRun}
    Assert-Vender ((Get-VenderRegistryValue 'Environment' 'Path').Value -eq $realPath.Value) 'DryRun leaves production PATH unchanged'
    Assert-Vender ((Get-VenderRegistryValue 'Software\Microsoft\Windows\CurrentVersion\App Paths\vender.exe' '').Value -eq $realApp.Value) 'DryRun leaves production App Paths unchanged'
}
[pscustomobject]@{Checks=$checks;Passed=$true;ProductionSettingsChanged=$false}
