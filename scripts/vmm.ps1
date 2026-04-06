# scripts/vmm.ps1
Param(
    [Parameter(Position=0)]
    [ValidateSet("build", "run", "clean", "tester")]
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
$TesterExePath = Join-Path $BinDir "vmm-pii-tester.exe"

function Sync-Configs {
    # Keep the packaged configs tree in sync for both the standard build and the standalone tester build.
    # 同步打包后的 configs 目录，确保标准构建和独立测试器构建都使用当前规则与配置。
    Write-Host "=> 📂 Syncing configs (including prompts)..." -ForegroundColor Gray
    $TargetConfig = Join-Path $OutputDir "configs"
    if (Test-Path $TargetConfig) { Remove-Item -Path $TargetConfig -Recurse -Force }
    Copy-Item -Path "$RootDir\configs" -Destination $OutputDir -Recurse -Force
}

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
    if ($null -ne $LASTEXITCODE -and $LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

    # Build the standalone PII tester together with the main binary so packaged output keeps the validator tooling available.
    # 同时编译独立 PII 测试器，确保标准构建产物里保留验证规则所需的工具链。
    go build -o $TesterExePath "$RootDir\cmd\vmm-pii-tester"
    if ($null -ne $LASTEXITCODE -and $LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    
    Sync-Configs
    
    Write-Host "=> ✅ Build Success!" -ForegroundColor Green
}

function Do-BuildTester {
    Write-Host "=> 🧪 Building VMM PII Tester..." -ForegroundColor Cyan
    if (!(Test-Path $BinDir)) { New-Item -ItemType Directory -Path $BinDir -Force | Out-Null }

    # Build only the standalone tester when contributors want to iterate on rules without rebuilding the full runtime.
    # 仅编译独立测试器，方便贡献者在不重建完整运行时的情况下迭代 PII 规则。
    go build -o $TesterExePath "$RootDir\cmd\vmm-pii-tester"
    if ($null -ne $LASTEXITCODE -and $LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

    # Keep the tester output self-contained so file mode always reads the rules that were just built and copied.
    # 保持测试器产物自包含，确保文件模式始终读取刚刚构建并复制过去的规则与配置。
    Sync-Configs

    Write-Host "=> ✅ Tester Build Success!" -ForegroundColor Green
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
    "tester" { Do-BuildTester }
}
