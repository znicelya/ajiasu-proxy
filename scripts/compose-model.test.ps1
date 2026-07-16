$ErrorActionPreference = 'Stop'

$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$composeRoot = Join-Path $root 'deploy/compose'
$environment = Join-Path $composeRoot 'env/compose.env.example'
$base = Join-Path $composeRoot 'compose.yaml'

function Get-ComposeModel {
    param([string[]] $Overlays, [string[]] $Profiles = @())
    $arguments = @('compose', '--env-file', $environment, '-f', $base)
    foreach ($overlay in $Overlays) { $arguments += @('-f', (Join-Path $composeRoot $overlay)) }
    foreach ($profile in $Profiles) { $arguments += @('--profile', $profile) }
    $arguments += @('config', '--format', 'json')
    $output = @(& docker @arguments 2>&1)
    if ($LASTEXITCODE -ne 0) { throw "docker compose config failed: $($output -join "`n")" }
    return (($output -join "`n") | ConvertFrom-Json)
}

function Assert-SecureModel {
    param($Model, [string[]] $ExpectedServices)
    $actualServices = @($Model.services.PSObject.Properties.Name | Sort-Object)
    $expected = @($ExpectedServices | Sort-Object)
    if (($actualServices -join ',') -cne ($expected -join ',')) {
        throw "services=$($actualServices -join ','), expected=$($expected -join ',')"
    }
    foreach ($network in @('edge', 'control', 'dependencies')) {
        if ($Model.networks.PSObject.Properties.Name -notcontains $network) { throw "missing network $network" }
    }
    foreach ($entry in $Model.services.PSObject.Properties) {
        $name, $service = $entry.Name, $entry.Value
        if ([string]$service.image -notmatch '@sha256:[0-9a-f]{64}$') { throw "$name image is mutable" }
        if ($service.privileged -eq $true -or $service.network_mode -eq 'host' -or $service.pid -eq 'host' -or $service.ipc -eq 'host') {
            throw "$name has host or privileged authority"
        }
        if ($name -notin @('migration', 'postgres', 'redis', 'identity-provider')) {
            if ($service.read_only -ne $true) { throw "$name root filesystem is writable" }
            if (@($service.cap_drop) -notcontains 'ALL') { throw "$name does not drop all capabilities" }
            if (@($service.security_opt) -notcontains 'no-new-privileges:true') { throw "$name lacks no-new-privileges" }
            if ($null -eq $service.healthcheck) { throw "$name lacks a health check" }
        }
        foreach ($volume in @($service.volumes)) {
            if ($null -eq $volume) { continue }
            $source = [string]$volume.source
            $target = [string]$volume.target
            if ($source -match 'docker\.sock' -or $target -match 'docker\.sock') {
                if ($name -ne 'agent' -or $target -cne '/var/run/docker.sock') { throw "Docker socket leaked to $name" }
            }
        }
        foreach ($port in @($service.ports)) {
            if ($null -eq $port) { continue }
            if ($name -in @('postgres', 'redis')) { throw "$name publishes a dependency port" }
            if ($name -in @('control-plane', 'identity-provider') -and [string]$port.host_ip -notin @('127.0.0.1', '::1')) {
                throw "$name management port is not loopback-bound"
            }
        }
        foreach ($variable in @($service.environment.PSObject.Properties.Name)) {
            if ($variable -match '(PASSWORD|TOKEN|SECRET|KEYRING)$') { throw "$name exposes secret environment variable $variable" }
        }
    }
    if ($Model.services.PSObject.Properties.Name -contains 'runner' -or $Model.services.PSObject.Properties.Name -contains 'console') {
        throw 'standing Runner or Console service is forbidden'
    }
}

$external = Get-ComposeModel @('compose.production.yaml')
Assert-SecureModel $external @('migration', 'control-plane', 'agent', 'gateway')
$singleHost = Get-ComposeModel @('compose.dependencies.yaml', 'compose.production.yaml')
Assert-SecureModel $singleHost @('migration', 'control-plane', 'agent', 'gateway', 'postgres', 'redis')
$development = Get-ComposeModel @('compose.dependencies.yaml', 'compose.development.yaml') @('identity')
Assert-SecureModel $development @('migration', 'control-plane', 'agent', 'gateway', 'postgres', 'redis', 'identity-provider')

Write-Output 'compose rendered-model security tests passed'
