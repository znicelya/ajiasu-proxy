$ErrorActionPreference = 'Stop'

if ([string]::IsNullOrWhiteSpace($env:AJIASU_USERNAME)) {
    [Console]::Error.WriteLine('AJIASU_USERNAME is required')
    exit 64
}

if ([string]::IsNullOrWhiteSpace($env:AJIASU_PASSWORD)) {
    [Console]::Error.WriteLine('AJIASU_PASSWORD is required')
    exit 64
}

if ($env:AJIASU_USERNAME -match '[\r\n]' -or $env:AJIASU_PASSWORD -match '[\r\n]') {
    [Console]::Error.WriteLine('AJIASU credentials must not contain line breaks')
    exit 64
}

$config = @(
    "user $($env:AJIASU_USERNAME)"
    "pass $($env:AJIASU_PASSWORD)"
    'cache_dir /var/lib/ajiasu'
) -join "`n"
$config += "`n"

$containerScript = @'
set -eu
umask 077
cat > /run/ajiasu/ajiasu.conf
mode=$(stat -c '%a' /run/ajiasu/ajiasu.conf)
if [ "$mode" != 600 ]; then
    echo "ajiasu config permissions must be 0600, got $mode" >&2
    exit 77
fi
export AJIASU_CONFIG=/run/ajiasu/ajiasu.conf
/usr/local/bin/ajiasu login
/usr/local/bin/ajiasu list
'@

$dockerArgs = @(
    'run'
    '--rm'
    '-i'
    '--cap-drop'
    'ALL'
    '--read-only'
    '--tmpfs'
    '/run/ajiasu:uid=65532,gid=65532,mode=0700,noexec,nosuid,size=1m'
    '--tmpfs'
    '/var/lib/ajiasu:uid=65532,gid=65532,mode=0700,noexec,nosuid,size=16m'
    '--entrypoint'
    '/bin/sh'
    'ajiasu-runner:test'
    '-c'
    $containerScript
)

$config | & docker @dockerArgs
$dockerExit = $LASTEXITCODE
if ($dockerExit -ne 0) {
    [Console]::Error.WriteLine("AJiaSu contract container failed with exit code $dockerExit")
    exit $dockerExit
}
