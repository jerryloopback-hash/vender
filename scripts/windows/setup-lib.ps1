# Shared pure planning and guarded registry/task helpers. Windows PowerShell 5.1+.
Set-StrictMode -Version 2.0

function ConvertTo-VenderPath([string]$Value) {
    if ([string]::IsNullOrWhiteSpace($Value)) { return '' }
    $value = [Environment]::ExpandEnvironmentVariables($Value.Trim().Trim('"'))
    if ([IO.Path]::IsPathRooted($value)) { $value = [IO.Path]::GetFullPath($value) }
    return $value.TrimEnd('\').ToLowerInvariant()
}

function Add-VenderPathEntry([string]$Value, [string]$BinDir) {
    $target = ConvertTo-VenderPath $BinDir
    foreach ($part in ($Value -split ';')) { if ((ConvertTo-VenderPath $part) -eq $target) { return $Value } }
    if ([string]::IsNullOrEmpty($Value)) { return $BinDir }
    if ($Value.EndsWith(';')) { return $Value + $BinDir }
    return $Value + ';' + $BinDir
}

function Remove-VenderPathEntry([string]$Value, [string]$BinDir) {
    $parts = New-Object 'Collections.Generic.List[string]'
    $removed = $false
    foreach ($part in ($Value -split ';')) {
        if (!$removed -and (ConvertTo-VenderPath $part) -eq (ConvertTo-VenderPath $BinDir)) { $removed = $true; continue }
        $parts.Add($part)
    }
    return ($parts -join ';')
}

function Get-VenderRegistryValue([string]$KeyPath, [string]$Name) {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey($KeyPath)
    try {
        if ($null -eq $key -or $key.GetValueNames() -notcontains $Name) { return [pscustomobject]@{ Exists=$false; Value=$null; Kind='ExpandString' } }
        return [pscustomobject]@{ Exists=$true; Value=$key.GetValue($Name,$null,[Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames); Kind=$key.GetValueKind($Name).ToString() }
    } finally { if ($key) { $key.Dispose() } }
}

function Set-VenderRegistryValue([string]$KeyPath, [string]$Name, $Value, [string]$Kind) {
    $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey($KeyPath)
    try { $key.SetValue($Name,$Value,([Enum]::Parse([Microsoft.Win32.RegistryValueKind],$Kind))) }
    finally { $key.Dispose() }
}

function Remove-VenderRegistryValue([string]$KeyPath, [string]$Name) {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey($KeyPath,$true)
    try { if ($key) { $key.DeleteValue($Name,$false) } } finally { if ($key) { $key.Dispose() } }
}

function New-VenderPrivateDirectory([string]$Path, [string]$Sid) {
    if (Test-Path -LiteralPath $Path) { throw 'Backup directory must be new; existing files are never overwritten.' }
    $parent = [IO.DirectoryInfo]([IO.Path]::GetFullPath($Path))
    while ($parent) { if (Test-Path -LiteralPath (Join-Path $parent.FullName '.git')) { throw 'Backups must be outside Git repositories.' }; $parent=$parent.Parent }
    $null = New-Item -ItemType Directory -Path $Path -Force
    $acl = New-Object Security.AccessControl.DirectorySecurity
    $acl.SetAccessRuleProtection($true,$false)
    $acl.SetOwner((New-Object Security.Principal.SecurityIdentifier($Sid)))
    foreach ($s in @($Sid,'S-1-5-18','S-1-5-32-544') | Select-Object -Unique) {
        $rule = New-Object Security.AccessControl.FileSystemAccessRule((New-Object Security.Principal.SecurityIdentifier($s)),'FullControl','ContainerInherit,ObjectInherit','None','Allow')
        $null = $acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $Path -AclObject $acl
}

function Test-VenderBinPermissions([string]$BinDir, [string]$Sid) {
    # Modify is composite and includes read bits: using it as a bitmask would
    # incorrectly classify ordinary read-only ACEs as write access.
    $write = [Security.AccessControl.FileSystemRights]::Write -bor [Security.AccessControl.FileSystemRights]::ChangePermissions -bor [Security.AccessControl.FileSystemRights]::TakeOwnership -bor [Security.AccessControl.FileSystemRights]::Delete -bor [Security.AccessControl.FileSystemRights]::DeleteSubdirectoriesAndFiles
    $paths=@($BinDir,[IO.Path]::GetDirectoryName($BinDir)) + @(Get-ChildItem -LiteralPath $BinDir -File | Where-Object {$_.Extension -in @('.exe','.dll')} | ForEach-Object {$_.FullName})
    foreach ($path in $paths) {
        foreach ($rule in (Get-Acl -LiteralPath $path).GetAccessRules($true,$true,[Security.Principal.SecurityIdentifier])) {
            if ($rule.AccessControlType -eq 'Allow' -and ($rule.FileSystemRights -band $write) -ne 0 -and $rule.IdentityReference.Value -notin @($Sid,'S-1-5-18','S-1-5-32-544')) {
                throw 'The program directory or its parent grants write access to another identity. Protect the install root before privileged startup.'
            }
        }
    }
}

function Get-VenderPE([string]$Path) {
    $stream = [IO.File]::OpenRead($Path)
    $reader = New-Object IO.BinaryReader($stream)
    try {
        if ($stream.Length -lt 256 -or $reader.ReadUInt16() -ne 0x5a4d) { throw 'Not a Windows executable.' }
        $stream.Position=0x3c; $offset=$reader.ReadInt32()
        if ($offset -lt 64 -or $offset+94 -gt $stream.Length) { throw 'Invalid PE header offset.' }
        $stream.Position=$offset
        if ($reader.ReadUInt32() -ne 0x4550) { throw 'Invalid PE signature.' }
        $machine=$reader.ReadUInt16(); $stream.Position=$offset+24+68
        return [pscustomobject]@{Machine=$machine;Subsystem=$reader.ReadUInt16()}
    } finally { $reader.Dispose(); $stream.Dispose() }
}

function Resolve-VenderSid([string]$Value) {
    if ($Value -match '^S-1-') { return $Value }
    return (New-Object Security.Principal.NTAccount($Value)).Translate([Security.Principal.SecurityIdentifier]).Value
}

function Initialize-VenderNative {
    if ('VenderSetupNative' -as [type]) { return }
    Add-Type -TypeDefinition @'
using System; using System.Text; using System.Runtime.InteropServices;
public static class VenderSetupNative {
 [DllImport("shell32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern IntPtr CommandLineToArgvW(string s,out int n);
 [DllImport("kernel32.dll")] static extern IntPtr LocalFree(IntPtr p);
 public static string[] Split(string s) {int n;IntPtr p=CommandLineToArgvW("vender "+s,out n);if(p==IntPtr.Zero)throw new System.ComponentModel.Win32Exception();try{string[] a=new string[n-1];for(int i=1;i<n;i++)a[i-1]=Marshal.PtrToStringUni(Marshal.ReadIntPtr(p,i*IntPtr.Size));return a;}finally{LocalFree(p);}}
 [DllImport("kernel32.dll",SetLastError=true)] static extern bool AttachConsole(uint p);
 [DllImport("kernel32.dll")] static extern bool FreeConsole();
 [DllImport("kernel32.dll")] static extern uint GetConsoleProcessList([Out] uint[] p,uint n);
 [DllImport("kernel32.dll")] static extern IntPtr GetConsoleWindow();
 public static long CurrentConsole() {return GetConsoleWindow().ToInt64();}
 [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
 [DllImport("user32.dll")] static extern bool ShowWindowAsync(IntPtr h,int s);
 [DllImport("user32.dll",CharSet=CharSet.Unicode)] static extern int GetClassNameW(IntPtr h,StringBuilder s,int n);
 [DllImport("user32.dll",CharSet=CharSet.Unicode)] static extern IntPtr SendMessageTimeoutW(IntPtr h,uint m,IntPtr w,string s,uint f,uint ms,out IntPtr r);
 public static void Refresh() {IntPtr r;SendMessageTimeoutW(new IntPtr(0xffff),0x1a,IntPtr.Zero,"Environment",2,2000,out r);}
 public static long Console(uint daemon,uint core,uint self,bool hide,long expected) {
  FreeConsole();if(!AttachConsole(daemon))throw new System.ComponentModel.Win32Exception();
  try{uint[] ids=new uint[64];uint n=GetConsoleProcessList(ids,64);if(n==0||n>64)throw new Exception("Console membership unavailable");bool found=false;for(int i=0;i<n;i++){if(ids[i]==daemon)found=true;else if(ids[i]!=core&&ids[i]!=self)throw new Exception("Console shared with another process; refused");}if(!found)throw new Exception("Daemon not attached");IntPtr h=GetConsoleWindow();if(h==IntPtr.Zero)return 0;if(expected!=0&&h.ToInt64()!=expected)throw new Exception("Console changed; refused");StringBuilder c=new StringBuilder(128);GetClassNameW(h,c,128);if(!IsWindowVisible(h)&&hide)return 0;if(c.ToString()!="ConsoleWindowClass")throw new Exception("Unexpected console host; refused");ShowWindowAsync(h,hide?0:5);return h.ToInt64();}finally{FreeConsole();}
 }
}
'@
}

function Get-VenderSilentTaskXml([string]$Xml, [string]$BinDir, [string]$Sid) {
    Initialize-VenderNative
    [xml]$doc=$Xml
    $ns=New-Object Xml.XmlNamespaceManager($doc.NameTable); $ns.AddNamespace('t','http://schemas.microsoft.com/windows/2004/02/mit/task')
    $exec=@($doc.SelectNodes('/t:Task/t:Actions/t:Exec',$ns))
    if ($exec.Count -ne 1 -or $doc.SelectNodes('/t:Task/t:Actions/*',$ns).Count -ne 1) { throw 'Expected one known vender Exec action.' }
    $cli=Join-Path $BinDir 'vender.exe'; $helper=Join-Path $BinDir 'vender-daemon.exe'
    $command=ConvertTo-VenderPath $exec[0].Command
    if ($command -notin @((ConvertTo-VenderPath $cli),(ConvertTo-VenderPath $helper))) { throw 'Task belongs to another installation; refused.' }
    $principals=@($doc.SelectNodes('/t:Task/t:Principals/t:Principal',$ns)); $triggers=@($doc.SelectNodes('/t:Task/t:Triggers/t:LogonTrigger',$ns))
    if ($principals.Count -ne 1 -or $triggers.Count -ne 1 -or $doc.SelectNodes('/t:Task/t:Triggers/*',$ns).Count -ne 1) { throw 'Expected one per-user logon trigger and principal.' }
    if ((Resolve-VenderSid $principals[0].UserId) -ne $Sid -or (Resolve-VenderSid $triggers[0].UserId) -ne $Sid -or $principals[0].LogonType -ne 'InteractiveToken') { throw 'Task identity differs from the daily user; refused.' }
    if ($principals[0].RunLevel -notin @('HighestAvailable','LeastPrivilege')) { throw 'Unknown privilege definition.' }
    if ((ConvertTo-VenderPath $exec[0].WorkingDirectory) -ne (ConvertTo-VenderPath $BinDir)) { throw 'Task working directory differs from install directory.' }
    $args=@([VenderSetupNative]::Split([string]$exec[0].Arguments))
    if ($args.Count -lt 1 -or $args[0] -ne 'daemon' -or $args.Count % 2 -ne 1) { throw 'Not a known daemon invocation.' }
    $seen=@{}
    for($i=1;$i -lt $args.Count;$i+=2){if($args[$i] -notin @('--config-dir','--run-dir') -or $seen.ContainsKey($args[$i]) -or ![IO.Path]::IsPathRooted($args[$i+1])){throw 'Unknown or relative daemon arguments.'};$seen[$args[$i]]=$true}
    if($command -eq (ConvertTo-VenderPath $helper)){return $Xml}
    # Use the DOM property directly: Windows PowerShell 5.1's XML adapter can
    # reject a pipeline-wrapped string assigned through the child-name adapter.
    $exec[0].SelectSingleNode('t:Command',$ns).InnerText=[string]$helper
    return $doc.OuterXml
}

function Save-VenderSetupState($State,[string]$Path) {
    $temp=$Path+'.tmp'
    $State | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $temp -Encoding UTF8
    Move-Item -LiteralPath $temp -Destination $Path -Force
}

function ConvertTo-VenderTaskComparison([string]$Xml) {
    $doc=New-Object Xml.XmlDocument
    $doc.PreserveWhitespace=$false
    $doc.LoadXml($Xml)
    return $doc.OuterXml
}

function Restore-VenderSetupState($State,[string]$EnvKey,[string]$AppKey,$Folder=$null) {
    $failures=New-Object 'Collections.Generic.List[string]'
    # Independent guards: a missing registry value cannot prevent task recovery.
    if($State.TaskChanged){try{
        if(!$Folder){throw 'Task Scheduler connection required.'}
        $task=$Folder.GetTask('daemon')
        $current=ConvertTo-VenderTaskComparison $task.Xml
        if($current -eq (ConvertTo-VenderTaskComparison $State.TaskBefore)) { } else {
        if($current -ne (ConvertTo-VenderTaskComparison $State.TaskAfter)){throw 'Task edited externally; left unchanged.'}
        $null=Get-VenderSilentTaskXml $State.TaskBefore $State.BinDir $State.SID
        $null=$Folder.RegisterTask('daemon',$State.TaskBefore,4,$State.SID,$null,3,$State.TaskSecurity)
        }
    }catch{$failures.Add('Task: '+$_.Exception.Message)}}
    if($State.AppChanged){try{
        $default=Get-VenderRegistryValue $AppKey ''; $path=Get-VenderRegistryValue $AppKey 'Path'
        if($default.Exists -and (ConvertTo-VenderPath $default.Value) -ne (ConvertTo-VenderPath (Join-Path $State.BinDir 'vender.exe'))){throw 'App Paths target edited externally; left unchanged.'}
        if($path.Exists -and (ConvertTo-VenderPath $path.Value) -ne (ConvertTo-VenderPath $State.BinDir)){throw 'App Paths search path edited externally; left unchanged.'}
        Remove-VenderRegistryValue $AppKey ''; Remove-VenderRegistryValue $AppKey 'Path'
        $key=[Microsoft.Win32.Registry]::CurrentUser.OpenSubKey($AppKey)
        try{$empty=$key -and $key.ValueCount -eq 0 -and $key.SubKeyCount -eq 0}finally{if($key){$key.Dispose()}}
        if($empty){[Microsoft.Win32.Registry]::CurrentUser.DeleteSubKey($AppKey,$false)}
    }catch{$failures.Add('App Paths: '+$_.Exception.Message)}}
    if($State.PathAdded){try{
        $current=Get-VenderRegistryValue $EnvKey 'Path'
        if($current.Exists){
            $value=Remove-VenderPathEntry ([string]$current.Value) $State.BinDir
            if(!$State.PathBefore.Exists -and [string]::IsNullOrEmpty($value)){Remove-VenderRegistryValue $EnvKey 'Path'}else{
                $kind=$current.Kind
                if($State.PathBefore.Exists -and $value -eq $State.PathBefore.Value){$kind=$State.PathBefore.Kind}
                Set-VenderRegistryValue $EnvKey 'Path' $value $kind
            }
        }
    }catch{$failures.Add('PATH: '+$_.Exception.Message)}}
    if($failures.Count){throw ($failures -join ' | ')}
}
