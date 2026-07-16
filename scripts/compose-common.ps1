Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Assert-ComposeImmutableImage {
    param([Parameter(Mandatory = $true)][string]$Name, [Parameter(Mandatory = $true)][string]$Value)
    if ($Value -notmatch '@sha256:[0-9a-f]{64}$') {
        throw "$Name must use an immutable sha256 digest"
    }
}

function Set-ComposePrivatePath {
    param([Parameter(Mandatory = $true)][string]$Path)
    $item = Get-Item -LiteralPath $Path -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Unsafe link or reparse point: $Path"
    }
    if ($env:OS -eq 'Windows_NT') {
        $acl = New-Object System.Security.AccessControl.DirectorySecurity
        if (-not $item.PSIsContainer) {
            $acl = New-Object System.Security.AccessControl.FileSecurity
        }
        $acl.SetAccessRuleProtection($true, $false)
        $identity = [Security.Principal.WindowsIdentity]::GetCurrent().User
        $rights = if ($item.PSIsContainer) { [Security.AccessControl.FileSystemRights]::FullControl } else { [Security.AccessControl.FileSystemRights]::Read, [Security.AccessControl.FileSystemRights]::Write, [Security.AccessControl.FileSystemRights]::Delete }
        $inheritance = if ($item.PSIsContainer) { [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit' } else { [Security.AccessControl.InheritanceFlags]::None }
        $rule = New-Object Security.AccessControl.FileSystemAccessRule($identity, $rights, $inheritance, [Security.AccessControl.PropagationFlags]::None, [Security.AccessControl.AccessControlType]::Allow)
        $acl.AddAccessRule($rule)
        Set-Acl -LiteralPath $Path -AclObject $acl
    }
}

function Write-ComposePrivateFile {
    param([Parameter(Mandatory = $true)][string]$Path, [Parameter(Mandatory = $true)][string]$Value)
    if (Test-Path -LiteralPath $Path) { throw "Refusing to overwrite private file: $Path" }
    $directory = Split-Path -Parent $Path
    if (-not (Test-Path -LiteralPath $directory)) { New-Item -ItemType Directory -Path $directory | Out-Null }
    $temporary = Join-Path $directory ('.partial-' + [Guid]::NewGuid().ToString('N'))
    try {
        [IO.File]::WriteAllText($temporary, $Value + "`n", (New-Object Text.UTF8Encoding($false)))
        Set-ComposePrivatePath -Path $temporary
        [IO.File]::Move($temporary, $Path)
        Set-ComposePrivatePath -Path $Path
    } finally {
        if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force }
    }
}

function Get-ComposeFiles {
    param([Parameter(Mandatory = $true)][string]$ComposeDirectory, [Parameter(Mandatory = $true)][ValidateSet('development', 'single-host', 'external')][string]$Mode)
    $files = @((Join-Path $ComposeDirectory 'compose.yaml'))
    if ($Mode -eq 'single-host') {
        $files += Join-Path $ComposeDirectory 'compose.dependencies.yaml'
        $files += Join-Path $ComposeDirectory 'compose.production.yaml'
    } elseif ($Mode -eq 'development') {
        $files += Join-Path $ComposeDirectory 'compose.dependencies.yaml'
        $files += Join-Path $ComposeDirectory 'compose.development.yaml'
    } else {
        $files += Join-Path $ComposeDirectory 'compose.production.yaml'
    }
    return $files
}

function Get-ComposeDockerArguments {
    param([Parameter(Mandatory = $true)][string]$EnvFile, [Parameter(Mandatory = $true)][string[]]$Files)
    $arguments = @('compose', '--env-file', $EnvFile)
    foreach ($file in $Files) { $arguments += @('-f', $file) }
    return $arguments
}

function Invoke-ComposeControlPlaneCLI {
    param(
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [string]$ControlPlaneExecutable,
        [string]$ControlPlaneImage,
        [hashtable]$Environment = @{}
    )
    $saved = @{}
    try {
        foreach ($entry in $Environment.GetEnumerator()) {
            $saved[$entry.Key] = [Environment]::GetEnvironmentVariable($entry.Key)
            [Environment]::SetEnvironmentVariable($entry.Key, [string]$entry.Value)
        }
        if ($ControlPlaneExecutable) {
            & $ControlPlaneExecutable @Arguments
        } else {
            Assert-ComposeImmutableImage -Name 'AJIASU_CONTROL_PLANE_IMAGE' -Value $ControlPlaneImage
            $dockerArgs = @('run', '--rm')
            foreach ($entry in $Environment.GetEnumerator()) { $dockerArgs += @('-e', $entry.Key, '-v', ((Resolve-Path -LiteralPath $entry.Value).Path + ':' + $entry.Value + ':ro')) }
            $dockerArgs += @($ControlPlaneImage)
            $dockerArgs += $Arguments
            & docker @dockerArgs
        }
        if ($LASTEXITCODE -ne 0) { throw "Control Plane compose command failed with exit code $LASTEXITCODE" }
    } finally {
        foreach ($entry in $saved.GetEnumerator()) { [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value) }
    }
}
