# scripts/vmm.ps1
Param(
    [Parameter(Position=0)]
    [ValidateSet("build", "run", "clean")]
    $Action = "build",
    [Parameter(ValueFromRemainingArguments=$true)]
    [string[]]$ProgramArgs = @()
)

$ErrorActionPreference = "Stop"
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Definition
$RootDir = Split-Path -Parent $ScriptDir
$OutputDir = Join-Path $RootDir "output"
$BinDir = Join-Path $OutputDir "bin"
$ExePath = Join-Path $BinDir "vmm-local.exe"

function Do-Clean {
    if (Test-Path $OutputDir) {
        Write-Host "=> 🧹 Cleaning output..." -ForegroundColor Gray
        Remove-Item -Path $OutputDir -Recurse -Force
    }
}

function Do-Build {
    Write-Host "=> 🚀 Building VMM Gateway..." -ForegroundColor Cyan
    if (!(Test-Path $BinDir)) { New-Item -ItemType Directory -Path $BinDir -Force | Out-Null }
    
    # Compile the full main package so debug helpers and future entrypoint files are linked into the final binary.
    # 编译完整的 main 包，确保调试辅助文件和后续入口文件都会被链接进最终二进制。
    go build -o $ExePath "$RootDir\cmd\vmm-local"
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    
    # 仅仅同步整个 configs 目录 (包含内部的 prompts)
    Write-Host "=> 📂 Syncing configs (including prompts)..." -ForegroundColor Gray
    $TargetConfig = Join-Path $OutputDir "configs"
    if (Test-Path $TargetConfig) { Remove-Item -Path $TargetConfig -Recurse -Force }
    Copy-Item -Path "$RootDir\configs" -Destination $OutputDir -Recurse -Force
    
    Write-Host "=> ✅ Build Success!" -ForegroundColor Green
}

function Do-Run {
    param(
        [string[]]$ForwardArgs = @()
    )

    if (!(Test-Path $ExePath)) { Do-Build }
    
    Write-Host "=> 🏃 Running VMM..." -ForegroundColor Magenta
    
    # 1. 记录当前目录并压入栈中
    Push-Location $BinDir
    $ExitCode = 0
    
    try {
        # 2. 执行程序
        & ".\vmm-local.exe" @ForwardArgs
        $ExitCode = $LASTEXITCODE
    }
    finally {
        # 3. 无论程序是正常结束还是报错，都强制弹回原始目录
        Pop-Location
        Write-Host "=> 🔙 Back to original directory." -ForegroundColor Gray
    }

    if ($ExitCode -ne 0) { exit $ExitCode }
}

switch ($Action) {
    "clean" { Do-Clean }
    "build" { Do-Build }
    "run"   { Do-Run -ForwardArgs $ProgramArgs }
}
