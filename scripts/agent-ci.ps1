$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
Push-Location $root
try {
    cargo fmt --all --check
    cargo clippy --workspace --all-targets --all-features -- -D warnings
    cargo test --workspace --all-features
    if (Get-Command cargo-deny -ErrorAction SilentlyContinue) { cargo deny check }
    if (Get-Command cargo-audit -ErrorAction SilentlyContinue) { cargo audit }
}
finally { Pop-Location }
