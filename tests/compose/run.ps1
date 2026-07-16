[CmdletBinding()]
param([int]$Repeat=2,[switch]$Full,[string]$EnvFile,[string]$SmokeConfigurationFile)
$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot 'harness.ps1')
$root=Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$scope=$null
try {
    & docker version --format '{{.Server.Version}}' | Out-Null; if($LASTEXITCODE -ne 0){throw 'Docker unavailable'}
    $scope=New-ComposeTestScope
    for($attempt=1;$attempt -le $Repeat;$attempt++){
        & go test -count=1 ./tests/compose ./tests/compose/fakes/fake_target ./tests/e2e/fake-runner ./tests/integration ./tests/failure ./tests/security
        if($LASTEXITCODE -ne 0){throw "Compose Go gate failed on attempt $attempt"}
        & cargo test -p ajiasu-agent -p ajiasu-gateway --all-features
        if($LASTEXITCODE -ne 0){throw "Packaged proxy path gate failed on attempt $attempt"}
        & (Join-Path $root 'scripts/compose-model.test.ps1')
        & (Join-Path $root 'scripts/compose-lifecycle.test.ps1')
        & (Join-Path $root 'scripts/compose-recovery.test.ps1')
        & (Join-Path $PSScriptRoot 'security.ps1')
    }
    $unrelated='ajiasu-unrelated-'+[Guid]::NewGuid().ToString('N')
    & docker create --name $unrelated ajiasu-control-plane:phase7-task4 health live | Out-Null
    try {
        & (Join-Path $root 'scripts/compose-down.ps1') -EnvFile (Join-Path $root 'deploy/compose/env/compose.env.example') -Mode external -KeepDependencies 2>$null
        if(-not (& docker inspect $unrelated 2>$null)){throw 'Lifecycle gate removed an unrelated container'}
    } catch { if(-not (& docker inspect $unrelated 2>$null)){throw} } finally {& docker rm -f $unrelated 2>$null|Out-Null}
    if($Full){
        if(-not $EnvFile -or -not $SmokeConfigurationFile){throw '-Full requires fake-only EnvFile and SmokeConfigurationFile'}
        $environment=@{};foreach($line in Get-Content -LiteralPath $EnvFile){if($line -match '^([^#=]+)=(.*)$'){$environment[$matches[1]]=$matches[2]}}
        $smoke=[IO.File]::ReadAllText($SmokeConfigurationFile)|ConvertFrom-Json
        if(([string]$environment.AJIASU_ENVIRONMENT_ID) -notlike 'phase7-e2e-*' -or $smoke.fixture_only -ne $true){throw 'Full suite accepts only explicitly marked phase7-e2e fake fixtures'}
        & (Join-Path $root 'scripts/compose-up.ps1') -EnvFile $EnvFile -Mode single-host -SmokeConfigurationFile $SmokeConfigurationFile
        & (Join-Path $root 'scripts/compose-down.ps1') -EnvFile $EnvFile -Mode single-host
    }
} finally {
    if($scope){Remove-ComposeTestScope $scope; Assert-ComposeTestScopeClean $scope}
}
'phase7 compose end-to-end and security gates passed'
