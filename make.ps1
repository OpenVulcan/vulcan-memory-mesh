# make.ps1
<#
.SYNOPSIS
    VMM Project Task Runner (Windows Native Replacement for Make)
.EXAMPLE
    .\make.ps1 build
    .\make.ps1 run
#>
Param(
    [Parameter(Position=0)]
    [ValidateSet("build", "run", "clean", "all", "tester")]
    $Target = "build",
    [Parameter(ValueFromRemainingArguments=$true)]
    [string[]]$ForwardArgs = @()
)

$ErrorActionPreference = "Stop"

# 1. 定位脚本目录
$PSScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Definition
$VmmScript = Join-Path (Join-Path $PSScriptDir "scripts") "vmm.ps1"

# 2. 检查核心脚本是否存在
if (!(Test-Path $VmmScript)) {
    Write-Host "❌ Error: Cannot find scripts/vmm.ps1" -ForegroundColor Red
    exit 1
}

# 3. 逻辑分发
Write-Host "--- VMM Task: $Target ---" -ForegroundColor DarkGray
switch ($Target) {
    "all"   { & $VmmScript build }
    "build" { & $VmmScript build }
    "run"   { & $VmmScript run @ForwardArgs }
    "clean" { & $VmmScript clean }
    "tester" { & $VmmScript tester }
    Default { & $VmmScript build }
}

if ($null -ne $LASTEXITCODE -and $LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}
