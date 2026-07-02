$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
$env:ELECTRON_RUN_AS_NODE = $null
Set-Location $Root
npm.cmd run dev