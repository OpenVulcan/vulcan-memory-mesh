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
    [ValidateSet("build", "run", "clean", "all", "tester", "deps")]
    $Task = "build",
    [Parameter(ValueFromRemainingArguments=$true)]
    [string[]]$ForwardArgs = @()
)

$ErrorActionPreference = "Stop"

# Re-enter through PowerShell 7 when available so helper scripts keep the same UTF-8 parsing behavior across launch shells.
if ($PSVersionTable.PSEdition -ne "Core") {
    $PwshCommand = Get-Command pwsh -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -ne $PwshCommand) {
        $ReentryArgs = New-Object System.Collections.Generic.List[string]
        $ReentryArgs.Add("-NoLogo")
        $ReentryArgs.Add("-NoProfile")
        $ReentryArgs.Add("-ExecutionPolicy")
        $ReentryArgs.Add("Bypass")
        $ReentryArgs.Add("-File")
        $ReentryArgs.Add($MyInvocation.MyCommand.Definition)
        $ReentryArgs.Add([string]$Task)
        foreach ($ForwardArg in $ForwardArgs) {
            $ReentryArgs.Add($ForwardArg)
        }
        & $PwshCommand.Source @($ReentryArgs.ToArray())
        exit $LASTEXITCODE
    }
}

# Normalize the historical -Target task syntax while leaving deps native -Target available for its Rust target.
# 兼容历史的 -Target 任务语法，同时保留 deps native -Target 传递给 Rust target。
if ($ForwardArgs.Count -ge 2 -and ([string]$ForwardArgs[0]).Trim().ToLowerInvariant() -eq "-target") {
    $Task = [string]$ForwardArgs[1]
    $ForwardArgs = @($ForwardArgs | Select-Object -Skip 2)
    if ($Task.Trim().ToLowerInvariant() -notin @("build", "run", "clean", "all", "tester", "deps")) { throw "unsupported make task: $Task" }
}

# 1. 定位脚本目录
$PSScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Definition
$VmmScript = Join-Path (Join-Path $PSScriptDir "scripts") "vmm.ps1"
$HostDepsScript = Join-Path (Join-Path $PSScriptDir "scripts") "install_host_deps.ps1"
$NativeDepsScript = Join-Path (Join-Path $PSScriptDir "scripts") "build_native_deps.ps1"

# 2. 检查核心脚本是否存在
if (!(Test-Path $VmmScript)) {
    Write-Host "❌ Error: Cannot find scripts/vmm.ps1" -ForegroundColor Red
    exit 1
}
if ($Task -eq "deps" -and !(Test-Path $HostDepsScript)) {
    Write-Host "❌ Error: Cannot find scripts/install_host_deps.ps1" -ForegroundColor Red
    exit 1
}
if ($Task -eq "deps" -and $ForwardArgs.Count -gt 0 -and $ForwardArgs[0].Trim().ToLowerInvariant() -eq "native" -and !(Test-Path $NativeDepsScript)) {
    Write-Host "Error: Cannot find scripts/build_native_deps.ps1" -ForegroundColor Red
    exit 1
}

# 3. 逻辑分发
Write-Host "--- VMM Task: $Task ---" -ForegroundColor DarkGray
switch ($Task) {
    "all"   { & $VmmScript build @ForwardArgs }
    "build" { & $VmmScript build @ForwardArgs }
    "run"   { & $VmmScript run @ForwardArgs }
    "clean" { & $VmmScript clean }
    "tester" { & $VmmScript tester }
    "deps" {
        if ($ForwardArgs.Count -eq 0 -or $ForwardArgs[0].Trim().ToLowerInvariant() -eq "host") {
            & $HostDepsScript
        }
        elseif ($ForwardArgs[0].Trim().ToLowerInvariant() -eq "native") {
            $NativeParams = @{}
            $NativeArgs = @($ForwardArgs | Select-Object -Skip 1)
            for ($Index = 0; $Index -lt $NativeArgs.Count; $Index++) {
                $NativeArg = ([string]$NativeArgs[$Index]).Trim()
                switch ($NativeArg.ToLowerInvariant()) {
                    "-target" { if ($Index + 1 -ge $NativeArgs.Count) { throw "deps native -Target requires a target triple" }; $NativeParams.Target = [string]$NativeArgs[++$Index] }
                    "--target" { if ($Index + 1 -ge $NativeArgs.Count) { throw "deps native --target requires a target triple" }; $NativeParams.Target = [string]$NativeArgs[++$Index] }
                    "-verifyonly" { $NativeParams.VerifyOnly = $true }
                    "--verify-only" { $NativeParams.VerifyOnly = $true }
                    "" { }
                    default { throw "unsupported deps native argument: $NativeArg" }
                }
            }
            & $NativeDepsScript @NativeParams
        }
        else {
            Write-Host "Error: use 'deps host' or 'deps native'" -ForegroundColor Red
            exit 1
        }
    }
    Default { & $VmmScript build }
}

if ($null -ne $LASTEXITCODE -and $LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}
