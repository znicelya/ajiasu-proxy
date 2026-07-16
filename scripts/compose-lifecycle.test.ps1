$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'compose-common.ps1')

$node = [Guid]::NewGuid(); $otherNode = [Guid]::NewGuid()
function New-FixtureContainer([string]$Id, [hashtable]$Labels) { [pscustomobject]@{ Id = $Id; Config = [pscustomobject]@{ Labels = [pscustomobject]$Labels } } }
$valid = New-FixtureContainer 'owned-runner' @{
    'ajiasu.owner'='control-plane'; 'ajiasu.node_id'=$node.ToString(); 'ajiasu.runner_id'=[Guid]::NewGuid().ToString();
    'ajiasu.tenant_id'=[Guid]::NewGuid().ToString(); 'ajiasu.endpoint_id'=[Guid]::NewGuid().ToString(); 'ajiasu.operation_id'=[Guid]::NewGuid().ToString(); 'ajiasu.generation'='3'
}
$unrelated = New-FixtureContainer 'unrelated-host-container' @{'application'='customer-database'}
$otherEnvironment = New-FixtureContainer 'other-ajiasu-runner' @{
    'ajiasu.owner'='control-plane'; 'ajiasu.node_id'=$otherNode.ToString(); 'ajiasu.runner_id'=[Guid]::NewGuid().ToString();
    'ajiasu.tenant_id'=[Guid]::NewGuid().ToString(); 'ajiasu.endpoint_id'=[Guid]::NewGuid().ToString(); 'ajiasu.operation_id'=[Guid]::NewGuid().ToString(); 'ajiasu.generation'='1'
}
$orphan = New-FixtureContainer 'malformed-runner' @{'ajiasu.owner'='control-plane'; 'ajiasu.node_id'=$node.ToString(); 'ajiasu.runner_id'='invalid'}
$result = Get-ComposeRunnerClassification -Containers @($valid, $unrelated, $otherEnvironment, $orphan) -NodeId $node
if ($result.Owned.Count -ne 1 -or $result.Owned[0] -ne 'owned-runner') { throw 'Exact owned Runner was not selected' }
if ($result.Orphans.Count -ne 1 -or $result.Orphans[0] -ne 'malformed-runner') { throw 'Malformed Runner was not preserved as an orphan diagnostic' }
if (($result.Owned + $result.Orphans) -contains 'unrelated-host-container' -or ($result.Owned + $result.Orphans) -contains 'other-ajiasu-runner') { throw 'Unrelated host containers entered the lifecycle removal set' }

foreach ($script in @('compose-up.ps1','compose-status.ps1','compose-down.ps1')) {
    $text = [IO.File]::ReadAllText((Join-Path $PSScriptRoot $script))
    if ($text -match 'docker\s+(rm|stop)\s+[^\r\n]*--filter\s+name=') { throw "$script uses unsafe name-based removal" }
}

$root = Join-Path ([IO.Path]::GetTempPath()) ('ajiasu-lifecycle-' + [Guid]::NewGuid().ToString('N'))
$oldPath = $env:PATH
try {
    New-Item -ItemType Directory -Path $root | Out-Null
    $fakeDocker = Join-Path $root 'docker.cmd'
    $log = Join-Path $root 'docker.log'
    $batch = @'
@echo off
echo %*>>"%DOCKER_FIXTURE_LOG%"
echo %*| findstr /c:"version --format" >nul && (echo 25.0.0& exit /b 0)
echo %*| findstr /c:"run --rm migration" >nul && if defined DOCKER_FAIL_MIGRATION exit /b 19
echo %*| findstr /c:"ps -q postgres" >nul && (echo id-postgres& exit /b 0)
echo %*| findstr /c:"ps -q redis" >nul && (echo id-redis& exit /b 0)
echo %*| findstr /c:"ps -q control-plane" >nul && (echo id-control-plane& exit /b 0)
echo %*| findstr /c:"ps -q agent" >nul && (echo id-agent& exit /b 0)
echo %*| findstr /c:"ps -q gateway" >nul && (echo id-gateway& exit /b 0)
echo %*| findstr /c:"ps -q migration" >nul && exit /b 0
if "%1"=="inspect" (if defined DOCKER_INSPECT_STARTING (echo starting) else (echo healthy))& exit /b 0
echo %*| findstr /c:"compose runtime-status" >nul && (echo {"nodes":{"total":1,"online":1,"draining":0,"sessions":1},"gateways":{"total":1,"online":1,"sessions":1},"assignments":{"fixed_assigned":1,"pool_assigned":1}}& exit /b 0)
echo %*| findstr /c:"ajiasu-agent status" >nul && (echo {"component":"agent","state":"enrolled","node_id":"11111111-1111-1111-1111-111111111111","protocol_revision":2}& exit /b 0)
echo %*| findstr /c:"stop -t" >nul && if defined DOCKER_FAIL_STOP exit /b 23
exit /b 0
'@
    [IO.File]::WriteAllText($fakeDocker, $batch, (New-Object Text.ASCIIEncoding))
    $env:PATH = $root + [IO.Path]::PathSeparator + $env:PATH; $env:DOCKER_FIXTURE_LOG = $log
    $generated = Join-Path $root 'generated'; New-Item -ItemType Directory -Path $generated | Out-Null
    $manifestFiles = [ordered]@{}
    foreach ($name in $script:ComposeStableGeneratedFiles) {
        $path = Join-Path $generated $name; [IO.File]::WriteAllText($path, ('fixture-' + $name))
        $item = Get-Item -LiteralPath $path
        $manifestFiles[$name] = [ordered]@{ sha256 = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant(); size = $item.Length; sensitive = $true }
    }
    $manifest = [ordered]@{ schema_version=1; environment_id='lifecycle-test'; mode='single-host'; created_at='2026-07-16T00:00:00Z'; gateway_certificate_fingerprint=('a' * 64); files=$manifestFiles; ephemeral_files=@('agent-enrollment-token','gateway-enrollment-token') }
    [IO.File]::WriteAllText((Join-Path $generated 'generated-state.json'), ($manifest | ConvertTo-Json -Depth 6))
    [IO.File]::WriteAllText((Join-Path $generated 'agent-enrollment-token'), 'fixture')
    [IO.File]::WriteAllText((Join-Path $generated 'gateway-enrollment-token'), 'fixture')
    $envFile = Join-Path $root 'compose.env'
    $image = 'example.invalid/fixture@sha256:' + ('1' * 64)
    [IO.File]::WriteAllLines($envFile, @(
        'AJIASU_ENVIRONMENT_ID=lifecycle-test', ('AJIASU_GENERATED_DIR=' + ($generated -replace '\\','/')),
        "AJIASU_CONTROL_PLANE_IMAGE=$image", "AJIASU_GATEWAY_IMAGE=$image", "AJIASU_AGENT_IMAGE=$image", "AJIASU_RUNNER_IMAGE=$image",
        "AJIASU_POSTGRES_IMAGE=$image", "AJIASU_REDIS_IMAGE=$image", "AJIASU_KEYCLOAK_IMAGE=$image"
    ))

    & (Join-Path $PSScriptRoot 'compose-up.ps1') -EnvFile $envFile -Mode single-host -SkipSmoke -Timeout ([TimeSpan]::FromSeconds(5)) | Out-Null
    & (Join-Path $PSScriptRoot 'compose-up.ps1') -EnvFile $envFile -Mode single-host -SkipSmoke -Timeout ([TimeSpan]::FromSeconds(5)) | Out-Null
    $calls = [IO.File]::ReadAllText($log)
    if (($calls -split 'run --rm migration').Count -lt 3) { throw 'Fresh and repeated start did not run the idempotent migration gate' }

    $env:DOCKER_FAIL_MIGRATION = '1'; $failed = $false
    try { & (Join-Path $PSScriptRoot 'compose-up.ps1') -EnvFile $envFile -Mode single-host -SkipSmoke -Timeout ([TimeSpan]::FromSeconds(5)) | Out-Null } catch { $failed = $true }
    Remove-Item Env:DOCKER_FAIL_MIGRATION
    if (-not $failed) { throw 'Migration failure did not stop startup' }

    $env:DOCKER_INSPECT_STARTING = '1'; $delayed = $false
    try { Wait-ComposeServiceHealthy -DockerArguments @('compose') -Service postgres -Timeout ([TimeSpan]::FromMilliseconds(100)) } catch { $delayed = $true }
    Remove-Item Env:DOCKER_INSPECT_STARTING
    if (-not $delayed) { throw 'Dependency delay did not produce a bounded timeout' }

    $env:DOCKER_FAIL_STOP = '1'; $timedOut = $false
    try { & (Join-Path $PSScriptRoot 'compose-down.ps1') -EnvFile $envFile -Mode single-host -TimeoutSeconds 1 | Out-Null } catch { $timedOut = $true }
    Remove-Item Env:DOCKER_FAIL_STOP
    if (-not $timedOut) { throw 'Forced shutdown timeout was not surfaced' }
} finally {
    if ($oldPath) { $env:PATH = $oldPath }
    Remove-Item Env:DOCKER_FIXTURE_LOG -ErrorAction SilentlyContinue
    Remove-Item Env:DOCKER_FAIL_MIGRATION -ErrorAction SilentlyContinue
    Remove-Item Env:DOCKER_INSPECT_STARTING -ErrorAction SilentlyContinue
    Remove-Item Env:DOCKER_FAIL_STOP -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}
'compose lifecycle ownership tests passed'
