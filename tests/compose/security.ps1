$ErrorActionPreference='Stop'; Set-StrictMode -Version Latest
$root=Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$envFile=Join-Path $root 'deploy/compose/env/compose.env.example'
$modelText=& docker compose --env-file $envFile -f (Join-Path $root 'deploy/compose/compose.yaml') -f (Join-Path $root 'deploy/compose/compose.dependencies.yaml') -f (Join-Path $root 'deploy/compose/compose.production.yaml') config --format json
if($LASTEXITCODE -ne 0){throw 'Compose security model failed to render'}
$model=$modelText|ConvertFrom-Json
foreach($property in $model.services.PSObject.Properties){
    $name=$property.Name;$service=$property.Value
    $get={param($field) $candidate=$service.PSObject.Properties[$field];if($candidate){$candidate.Value}else{$null}}
    if((&$get 'privileged') -or (&$get 'network_mode') -eq 'host' -or (&$get 'pid') -eq 'host' -or (&$get 'ipc') -eq 'host'){throw "$name has host authority"}
    if(-not (&$get 'read_only')){throw "$name root filesystem is writable"}
    if(-not (@((&$get 'cap_drop')) -contains 'ALL')){throw "$name does not drop all capabilities"}
    $rendered=$service|ConvertTo-Json -Depth 12
    if($name -ne 'agent' -and $rendered.Contains('/var/run/docker.sock')){throw "$name receives the Docker socket"}
    if($rendered -match '(nse_|gwe_|postgresql://[^" ]+:[^" ]+@|BEGIN PRIVATE KEY)'){throw "$name rendered model exposed a secret"}
}
foreach($image in @('ajiasu-control-plane:phase7-task4','ajiasu-agent:phase7-task4','ajiasu-gateway:phase7-task4','ajiasu-fake-runner:phase7-task4')){
    & docker image inspect $image *> $null
    if($LASTEXITCODE -ne 0){continue}
    $inspection=(& docker image inspect $image|ConvertFrom-Json)[0]
    if($inspection.Config.User -ne '65532:65532'){throw "$image does not run as the release UID"}
    if(-not $inspection.Config.Healthcheck){throw "$image has no healthcheck"}
}
'compose Docker security inspection passed'
