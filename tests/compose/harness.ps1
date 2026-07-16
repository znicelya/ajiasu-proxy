$ErrorActionPreference='Stop'

function New-ComposeTestScope {
    $id='phase7-'+[Guid]::NewGuid().ToString('N')
    & docker network create --label "ajiasu.test_run=$id" "$id-network" | Out-Null
    & docker volume create --label "ajiasu.test_run=$id" "$id-volume" | Out-Null
    return $id
}

function Remove-ComposeTestScope([string]$Id) {
    $containers=@(& docker ps -aq --filter "label=ajiasu.test_run=$Id")
    foreach($container in $containers){if($container){& docker rm -f $container | Out-Null}}
    $networks=@(& docker network ls -q --filter "label=ajiasu.test_run=$Id")
    foreach($network in $networks){if($network){& docker network rm $network | Out-Null}}
    $volumes=@(& docker volume ls -q --filter "label=ajiasu.test_run=$Id")
    foreach($volume in $volumes){if($volume){& docker volume rm $volume | Out-Null}}
}

function Assert-ComposeTestScopeClean([string]$Id) {
    foreach($query in @(
        @('ps','-aq','--filter',"label=ajiasu.test_run=$Id"),
        @('network','ls','-q','--filter',"label=ajiasu.test_run=$Id"),
        @('volume','ls','-q','--filter',"label=ajiasu.test_run=$Id")
    )) { if ((& docker @query | Out-String).Trim()) { throw "Leaked Docker resource for $Id" } }
}
