$ErrorActionPreference = 'Stop'

function Invoke-Docker {
    param([Parameter(Mandatory = $true)][string[]] $Arguments)

    $previousErrorActionPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $output = & docker @Arguments 2>&1
        $exitCode = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }

    if ($exitCode -ne 0) {
        throw "docker $($Arguments -join ' ') failed with exit code $exitCode`n$($output -join "`n")"
    }

    return $output
}

$image = if ($env:RUNNER_IMAGE) { $env:RUNNER_IMAGE } else { 'ajiasu-runner:test' }

$user = Invoke-Docker @('image', 'inspect', $image, '--format', '{{.Config.User}}')
if ($user -ne '65532:65532') { throw "expected user 65532:65532, got $user" }

$entrypoint = Invoke-Docker @('image', 'inspect', $image, '--format', '{{json .Config.Entrypoint}}')
if ($entrypoint -ne '["/usr/local/bin/runner-entrypoint.sh"]') { throw "unexpected entrypoint: $entrypoint" }

$command = Invoke-Docker @('image', 'inspect', $image, '--format', '{{json .Config.Cmd}}')
if ($command -ne '["connect"]') { throw "unexpected command: $command" }

$workdir = Invoke-Docker @('image', 'inspect', $image, '--format', '{{.Config.WorkingDir}}')
if ($workdir -ne '/var/lib/ajiasu') { throw "unexpected working directory: $workdir" }

$labelsJson = Invoke-Docker @('image', 'inspect', $image, '--format', '{{json .Config.Labels}}')
$labels = $labelsJson | ConvertFrom-Json
$version = $labels.'org.opencontainers.image.version'
if ($version -ne '4.2.3.0') { throw "unexpected AJiaSu version label: $version" }

$ajiasuMode = Invoke-Docker @('run', '--rm', '--entrypoint', '/bin/sh', $image, '-c', 'stat -c %u:%g-%a /usr/local/bin/ajiasu')
if ($ajiasuMode -ne '0:0-555') { throw "unexpected AJiaSu executable ownership/mode: $ajiasuMode (expected 0:0 555)" }

$entrypointMode = Invoke-Docker @('run', '--rm', '--entrypoint', '/bin/sh', $image, '-c', 'stat -c %u:%g-%a /usr/local/bin/runner-entrypoint.sh')
if ($entrypointMode -ne '0:0-555') { throw "unexpected runner entrypoint ownership/mode: $entrypointMode (expected 0:0 555)" }
