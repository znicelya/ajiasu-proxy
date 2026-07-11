$ErrorActionPreference = 'Stop'

$image = if ($env:RUNNER_IMAGE) { $env:RUNNER_IMAGE } else { 'ajiasu-runner:test' }

$user = docker image inspect $image --format '{{.Config.User}}'
if ($user -ne '65532:65532') { throw "expected user 65532:65532, got $user" }

$entrypoint = docker image inspect $image --format '{{json .Config.Entrypoint}}'
if ($entrypoint -ne '["/usr/local/bin/runner-entrypoint.sh"]') { throw "unexpected entrypoint: $entrypoint" }

$labels = docker image inspect $image --format '{{index .Config.Labels \"org.opencontainers.image.version\"}}'
if ($labels -ne '4.2.3.0') { throw "unexpected AJiaSu version label: $labels" }
