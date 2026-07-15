$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
Push-Location -LiteralPath $repoRoot
try {
    cargo fmt --all --check
    if ($LASTEXITCODE -ne 0) { throw 'cargo fmt failed' }
    cargo clippy --workspace --all-targets --all-features -- -D warnings
    if ($LASTEXITCODE -ne 0) { throw 'cargo clippy failed' }
    cargo test --workspace --all-features
    if ($LASTEXITCODE -ne 0) { throw 'cargo test failed' }
    go tool sqlc vet
    if ($LASTEXITCODE -ne 0) { throw 'sqlc vet failed' }
    go tool sqlc diff
    if ($LASTEXITCODE -ne 0) { throw 'sqlc diff failed' }
    go test ./...
    if ($LASTEXITCODE -ne 0) { throw 'go test failed' }
    git diff --check
    if ($LASTEXITCODE -ne 0) { throw 'git diff check failed' }
}
finally { Pop-Location }
