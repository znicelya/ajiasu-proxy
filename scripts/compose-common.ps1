Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$script:ComposeStableGeneratedFiles = @(
    'agent-cert.pem','agent-key.pem','agent-relay-cert.pem','agent-relay-key.pem','control-plane-cert.pem','control-plane-key.pem','control-plane-keyring',
    'database-migration-dsn','database-normal-dsn','database-normal-password','database-platform-dsn','database-platform-password','gateway-cert.pem','gateway-key.pem',
    'gateway-certificate-fingerprint','keycloak-bootstrap-password','oidc-client-secret','platform-ca.pem','postgres-password','redis-acl','redis-password','route-signing-key','route-verifying-key'
)

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

function Wait-ComposeServiceHealthy {
    param([Parameter(Mandatory = $true)][string[]]$DockerArguments, [Parameter(Mandatory = $true)][string]$Service, [TimeSpan]$Timeout = [TimeSpan]::FromMinutes(5))
    $deadline = [DateTime]::UtcNow.Add($Timeout)
    do {
        $containerId = (& docker @DockerArguments ps -q $Service | Out-String).Trim()
        if ($LASTEXITCODE -eq 0 -and $containerId) {
            $state = (& docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' $containerId 2>$null | Out-String).Trim()
            if ($state -eq 'healthy' -or $state -eq 'running') { return }
            if ($state -eq 'unhealthy' -or $state -eq 'exited' -or $state -eq 'dead') { throw "Service '$Service' entered terminal state '$state'" }
        }
        Start-Sleep -Seconds 2
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "Timed out waiting for service '$Service'; diagnostic containers were preserved"
}

function Get-ComposeRunnerClassification {
    param([Parameter(Mandatory = $true)][object[]]$Containers, [Parameter(Mandatory = $true)][Guid]$NodeId)
    $owned = New-Object Collections.Generic.List[string]
    $orphans = New-Object Collections.Generic.List[string]
    foreach ($container in $Containers) {
        $labels = $container.Config.Labels
        if (-not $labels) { continue }
        $value = { param($name) $property = $labels.PSObject.Properties[$name]; if ($property) { [string]$property.Value } else { '' } }
        if ((& $value 'ajiasu.owner') -ne 'control-plane') { continue }
        $containerNode = [Guid]::Empty
        $nodeValid = [Guid]::TryParse((& $value 'ajiasu.node_id'), [ref]$containerNode)
        if ($nodeValid -and $containerNode -ne $NodeId) { continue }
        $runner = [Guid]::Empty; $tenant = [Guid]::Empty; $endpoint = [Guid]::Empty; $operation = [Guid]::Empty
        $valid = $nodeValid -and $containerNode -eq $NodeId -and
            [Guid]::TryParse((& $value 'ajiasu.runner_id'), [ref]$runner) -and
            [Guid]::TryParse((& $value 'ajiasu.tenant_id'), [ref]$tenant) -and
            [Guid]::TryParse((& $value 'ajiasu.endpoint_id'), [ref]$endpoint) -and
            [Guid]::TryParse((& $value 'ajiasu.operation_id'), [ref]$operation) -and
            ((& $value 'ajiasu.generation') -match '^[1-9][0-9]*$')
        if ($valid) { $owned.Add([string]$container.Id) } else { $orphans.Add([string]$container.Id) }
    }
    return [pscustomobject]@{ Owned = @($owned); Orphans = @($orphans) }
}

function Invoke-ComposeSmokeProbes {
    param([Parameter(Mandatory = $true)][string]$ConfigurationFile)
    if (-not (Test-Path -LiteralPath $ConfigurationFile -PathType Leaf)) { throw 'Smoke configuration file is unavailable' }
    $configuration = [IO.File]::ReadAllText((Resolve-Path -LiteralPath $ConfigurationFile)) | ConvertFrom-Json
    foreach ($name in @('fixed', 'pool')) {
        $probe = $configuration.$name
        if (-not $probe -or -not $probe.proxy_uri -or -not $probe.target_uri -or -not $probe.username -or -not $probe.password) { throw "Smoke probe '$name' is incomplete" }
        Add-Type -AssemblyName System.Net.Http
        $handler = New-Object System.Net.Http.HttpClientHandler
        $handler.Proxy = New-Object System.Net.WebProxy([Uri]$probe.proxy_uri)
        $handler.Proxy.Credentials = New-Object System.Net.NetworkCredential([string]$probe.username, [string]$probe.password)
        $client = New-Object System.Net.Http.HttpClient -ArgumentList $handler
        $client.Timeout = [TimeSpan]::FromSeconds(15)
        try {
            $response = $client.GetAsync([Uri]$probe.target_uri).GetAwaiter().GetResult()
            $expected = if ($probe.expected_status) { [int]$probe.expected_status } else { 200 }
            if ([int]$response.StatusCode -ne $expected) { throw "Smoke probe '$name' returned an unexpected status" }
        } finally { $client.Dispose(); $handler.Dispose() }
    }
}

function Assert-ComposeGeneratedState {
    param([Parameter(Mandatory = $true)][string]$Directory, [Parameter(Mandatory = $true)][string]$EnvironmentId)
    $manifestPath = Join-Path $Directory 'generated-state.json'
    if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) { throw 'Generated-state manifest is unavailable' }
    $manifest = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
    if ($manifest.schema_version -ne 1 -or $manifest.environment_id -cne $EnvironmentId) { throw 'Generated state belongs to another environment or schema' }
    $allowed = @{'generated-state.json'=$true; 'agent-enrollment-token'=$true; 'gateway-enrollment-token'=$true}
    foreach ($name in $script:ComposeStableGeneratedFiles) {
        if (-not $manifest.files.PSObject.Properties[$name]) { throw "Generated-state manifest omits: $name" }
    }
    foreach ($property in $manifest.files.PSObject.Properties) {
        $allowed[$property.Name] = $true
        $path = Join-Path $Directory $property.Name
        $item = Get-Item -LiteralPath $path -Force
        if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Unsafe generated-state file: $($property.Name)" }
        $digest = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($digest -cne [string]$property.Value.sha256 -or $item.Length -ne [int64]$property.Value.size) { throw "Generated-state integrity failure: $($property.Name)" }
    }
    foreach ($item in Get-ChildItem -LiteralPath $Directory -Force) {
        if (-not $allowed.ContainsKey($item.Name)) { throw "Unexpected generated-state entry: $($item.Name)" }
    }
}

function Read-ComposeEnvironmentFile {
    param([Parameter(Mandatory = $true)][string]$Path)
    $values = @{}
    foreach ($line in Get-Content -LiteralPath $Path) { if ($line -match '^([^#=]+)=(.*)$') { $values[$matches[1]] = $matches[2] } }
    return $values
}

function Assert-ComposeBackup {
    param([Parameter(Mandatory = $true)][string]$Directory, [string]$EnvironmentId)
    $manifestPath = Join-Path $Directory 'backup-manifest.json'
    if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) { throw 'Backup manifest is unavailable' }
    $manifest = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
    if ($manifest.backup_version -ne 1 -or $manifest.schema_version -ne 11) { throw 'Backup schema is incompatible' }
    if ($EnvironmentId -and $manifest.environment_id -cne $EnvironmentId) { throw 'Backup belongs to another environment' }
    foreach ($artifact in @($manifest.database, $manifest.keyring, $manifest.configuration, $manifest.ca)) {
        $path = Join-Path $Directory ([string]$artifact.file)
        $item = Get-Item -LiteralPath $path -Force
        if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Backup artifact is unsafe' }
        if ($item.Length -ne [int64]$artifact.size -or (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() -cne [string]$artifact.sha256) { throw 'Backup artifact checksum mismatch' }
    }
    return $manifest
}
