$ErrorActionPreference='Stop'
$root=(Resolve-Path (Join-Path $PSScriptRoot '..')).Path

function Invoke-Checked([string]$Command,[string[]]$Arguments){
    Write-Host "[compose-ci] $Command $($Arguments -join ' ')"
    & $Command @Arguments
    if($LASTEXITCODE -ne 0){throw "$Command failed with exit code $LASTEXITCODE"}
}

Push-Location $root
try {
    Invoke-Checked docker @('compose','version')
    Invoke-Checked docker @('buildx','version')
    & ./scripts/lock-compose-images.test.ps1
    & ./scripts/compose-model.test.ps1
    & ./scripts/compose-init.test.ps1
    Invoke-Checked go @('tool','sqlc','vet')
    Invoke-Checked go @('tool','sqlc','diff')
    Invoke-Checked go @('test','-race','-p','1','./...')
    Invoke-Checked go @('vet','./...')
    Invoke-Checked go @('tool','staticcheck','./...')
    Invoke-Checked cargo @('fmt','--all','--check')
    Invoke-Checked cargo @('clippy','--workspace','--all-targets','--all-features','--','-D','warnings')
    Invoke-Checked cargo @('test','--workspace','--all-features')
    if(-not (Get-Command cargo-deny -ErrorAction SilentlyContinue)){throw 'cargo-deny is required for the Compose release gate'}
    Invoke-Checked cargo @('deny','check')
    & ./tests/compose/run.ps1 -Repeat 2
    & ./scripts/compose-image-ci.ps1
    if((git ls-files 'deploy/compose/generated/*' | Out-String).Trim()){throw 'Generated Compose state is tracked by Git'}
    Invoke-Checked git @('diff','--check')
    if((git status --porcelain | Out-String).Trim()){throw 'Compose release gate left a dirty worktree'}
} finally {Pop-Location}
'compose release gates passed'
