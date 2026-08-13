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
$LibDir = Join-Path $OutputDir "libs"
$DatabaseDir = Join-Path $OutputDir "database"
$ExePath = Join-Path $BinDir "vmm-local.exe"
$MigrateExePath = Join-Path $BinDir "vmm-migrate.exe"
$TesterExePath = Join-Path $BinDir "vmm-pii-tester.exe"
$ThirdPartyDepsDir = Join-Path (Join-Path $RootDir "third_party") "deps"
$SourceRevision = "unknown"
$SourceStateDigest = "unknown"

# Resolve-SourceIdentity captures the exact source revision and source-state digest used for a local build.
# Resolve-SourceIdentity 记录本次本地构建使用的精确源码版本与源码状态摘要。
function Resolve-SourceIdentity {
    $revision = (& git -C $RootDir rev-parse HEAD 2>$null)
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($revision)) {
        throw "cannot resolve VMM source revision from git"
    }
    $script:SourceRevision = $revision.Trim()
    $relativeFiles = @(& git -C $RootDir ls-files -co --exclude-standard)
    if ($LASTEXITCODE -ne 0) {
        throw "cannot enumerate VMM source files from git"
    }
    $lines = New-Object System.Collections.Generic.List[string]
    foreach ($relativeFile in $relativeFiles) {
        if ([string]::IsNullOrWhiteSpace($relativeFile)) {
            continue
        }
        $absoluteFile = Join-Path $RootDir $relativeFile
        if (-not (Test-Path -LiteralPath $absoluteFile -PathType Leaf)) {
            continue
        }
        $fileDigest = (Get-FileHash -Algorithm SHA256 -LiteralPath $absoluteFile).Hash.ToLowerInvariant()
        $lines.Add(($relativeFile.Replace("\", "/") + "`0" + $fileDigest))
    }
    $lines.Sort()
    $digest = [Security.Cryptography.SHA256]::Create()
    try {
        $bytes = [Text.Encoding]::UTF8.GetBytes(($lines -join "`n"))
        $script:SourceStateDigest = [Convert]::ToHexString($digest.ComputeHash($bytes)).ToLowerInvariant()
    }
    finally {
        $digest.Dispose()
    }
}

# Resolve-GoExe locates go.exe lazily so run/clean actions can keep working on machines that only carry packaged binaries.
# Resolve-GoExe 用于按需解析 go.exe，确保只携带打包产物的机器仍然可以正常执行 run/clean 动作。
function Resolve-GoExe {
    if ($script:ResolvedGoExe) {
        return $script:ResolvedGoExe
    }
    $GoExe = (Get-Command go -CommandType Application | Select-Object -First 1 -ExpandProperty Source)
    if ([string]::IsNullOrWhiteSpace($GoExe)) {
        throw "cannot resolve go executable from PATH"
    }
    $script:ResolvedGoExe = $GoExe
    return $script:ResolvedGoExe
}

function Sync-Configs {
    # Keep the packaged configs tree in sync for both the standard build and the standalone tester build.
    # 同步打包后的 configs 目录，确保标准构建和独立测试器构建都使用当前规则与配置。
    Write-Host "=> 📂 Syncing configs (including prompts)..." -ForegroundColor Gray
    $TargetConfig = Join-Path $OutputDir "configs"
    if (Test-Path $TargetConfig) { Remove-Item -Path $TargetConfig -Recurse -Force }
    Copy-Item -Path "$RootDir\configs" -Destination $OutputDir -Recurse -Force
}

# Get-HostLibraryNames returns the dynamic-library filenames that must be packaged for the current host platform.
# Get-HostLibraryNames 用于返回当前宿主平台必须打包的动态库文件名。
function Get-HostLibraryNames {
    $HostIsWindows = [System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::Windows)
    $HostIsLinux = [System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::Linux)
    $HostIsMacOS = [System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::OSX)

    if ($HostIsWindows) {
        return @("vldb_sqlite.dll", "vldb_lancedb.dll")
    }
    if ($HostIsLinux) {
        return @("libvldb_sqlite.so", "libvldb_lancedb.so")
    }
    if ($HostIsMacOS) {
        return @("libvldb_sqlite.dylib", "libvldb_lancedb.dylib")
    }

    throw "unsupported platform for host library packaging"
}

# Sync-HostLibraries copies downloaded host libraries into output/libs so the packaged runtime can resolve FFI dependencies without extra setup.
# Sync-HostLibraries 用于把已下载的宿主动态库复制到 output/libs，确保打包后的运行时无需额外配置即可解析 FFI 依赖。
function Sync-HostLibraries {
    Write-Host "=> 📦 Syncing host libraries..." -ForegroundColor Gray
    if (!(Test-Path -LiteralPath $ThirdPartyDepsDir)) {
        throw "missing host dependency directory: $ThirdPartyDepsDir. Run '.\\make.ps1 deps host' first."
    }
    if (Test-Path -LiteralPath $LibDir) {
        Remove-Item -Path $LibDir -Recurse -Force
    }
    New-Item -ItemType Directory -Path $LibDir -Force | Out-Null

    foreach ($LibraryName in Get-HostLibraryNames) {
        $SourcePath = Join-Path $ThirdPartyDepsDir $LibraryName
        if (!(Test-Path -LiteralPath $SourcePath)) {
            throw "missing host dynamic library: $SourcePath. Run '.\\make.ps1 deps host' first."
        }
        Copy-Item -Path $SourcePath -Destination (Join-Path $LibDir $LibraryName) -Force
    }
}

# Get-ControllerBinaryName returns the vldb-controller executable filename for the current host platform.
# Get-ControllerBinaryName 用于返回当前宿主平台对应的 vldb-controller 可执行文件名。
function Get-ControllerBinaryName {
    $HostIsWindows = [System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::Windows)
    if ($HostIsWindows) {
        return "vldb-controller.exe"
    }
    return "vldb-controller"
}

# Sync-ControllerBinary copies the pinned controller executable into output/bin beside the VMM binaries.
# Sync-ControllerBinary 用于把固定版本 controller 可执行文件复制到 VMM 二进制旁的 output/bin。
function Sync-ControllerBinary {
    Write-Host "=> 📦 Syncing vldb-controller..." -ForegroundColor Gray
    $BinaryName = Get-ControllerBinaryName
    $SourcePath = Join-Path $ThirdPartyDepsDir $BinaryName
    if (!(Test-Path -LiteralPath $SourcePath)) {
        throw "missing controller executable: $SourcePath. Run '.\\make.ps1 deps host' first."
    }
    Copy-Item -Path $SourcePath -Destination (Join-Path $BinDir $BinaryName) -Force
}

# Ensure-DatabaseLayout creates the packaged database root next to the binary so local SQLite and LanceDB backends share one deterministic storage layout.
# Ensure-DatabaseLayout 用于在二进制旁边创建统一的 database 根目录，让本地 SQLite 与 LanceDB 后端共享稳定存储布局。
function Ensure-DatabaseLayout {
    Write-Host "=> 🗄️ Ensuring packaged database layout..." -ForegroundColor Gray
    New-Item -ItemType Directory -Path $DatabaseDir -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $DatabaseDir "lancedb") -Force | Out-Null
}

function Invoke-GoBuild {
    param(
        [Parameter(Mandatory=$true)]
        [string]$OutputPath,
        [Parameter(Mandatory=$true)]
        [string]$PackagePath,
        [string]$Ldflags = ""
    )

    # Build a single Go entrypoint through the resolved go.exe path so packaging never silently skips a binary because of shell alias or function shadowing.
    # 通过已解析的 go.exe 路径构建单个 Go 入口，避免因为 shell 别名或函数遮蔽导致打包静默跳过某个二进制。
    $GoExe = Resolve-GoExe
    if ([string]::IsNullOrWhiteSpace($Ldflags)) {
        & $GoExe build -o $OutputPath $PackagePath
    }
    else {
        & $GoExe build -ldflags $Ldflags -o $OutputPath $PackagePath
    }
    if ($null -ne $LASTEXITCODE -and $LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    if (!(Test-Path -LiteralPath $OutputPath)) {
        throw "expected build artifact was not produced: $OutputPath"
    }
}

function Do-Clean {
    if (Test-Path $OutputDir) {
        Write-Host "=> 🧹 Cleaning output..." -ForegroundColor Gray
        Remove-Item -Path $OutputDir -Recurse -Force
    }
}

function Do-Build {
    Resolve-SourceIdentity
    $BuildLdflags = "-X github.com/openvulcan/vmm/internal/buildinfo.SourceRevision=$SourceRevision -X github.com/openvulcan/vmm/internal/buildinfo.SourceStateDigest=$SourceStateDigest"
    Write-Host "=> 🚀 Building VMM Gateway..." -ForegroundColor Cyan
    if (!(Test-Path $BinDir)) { New-Item -ItemType Directory -Path $BinDir -Force | Out-Null }
    
    # Compile the full main package so debug helpers and future entrypoint files are linked into the final binary.
    # 编译完整的 main 包，确保调试辅助文件和后续入口文件都会被链接进最终二进制。
    Invoke-GoBuild -OutputPath $ExePath -PackagePath "$RootDir\cmd\vmm-local" -Ldflags $BuildLdflags

    # Build the standalone maintenance tool together with the main binary so packaged output keeps destructive cleanup, migration, and vector rebuild workflows outside the runtime process.
    # 同时编译独立维护工具，确保标准打包产物把清库、迁移和向量重建工作流放在运行时进程之外。
    Invoke-GoBuild -OutputPath $MigrateExePath -PackagePath "$RootDir\cmd\vmm-migrate" -Ldflags $BuildLdflags

    # Build the standalone PII tester together with the main binary so packaged output keeps the validator tooling available.
    # 同时编译独立 PII 测试器，确保标准构建产物里保留验证规则所需的工具链。
    Invoke-GoBuild -OutputPath $TesterExePath -PackagePath "$RootDir\cmd\vmm-pii-tester"
    
    Sync-Configs
    Sync-HostLibraries
    Sync-ControllerBinary
    Ensure-DatabaseLayout
    
    Write-Host "=> ✅ Build Success!" -ForegroundColor Green
}

function Do-BuildTester {
    Write-Host "=> 🧪 Building VMM PII Tester..." -ForegroundColor Cyan
    if (!(Test-Path $BinDir)) { New-Item -ItemType Directory -Path $BinDir -Force | Out-Null }

    # Build only the standalone tester when contributors want to iterate on rules without rebuilding the full runtime.
    # 仅编译独立测试器，方便贡献者在不重建完整运行时的情况下迭代 PII 规则。
    Invoke-GoBuild -OutputPath $TesterExePath -PackagePath "$RootDir\cmd\vmm-pii-tester"

    # Keep the tester output self-contained so file mode always reads the rules that were just built and copied.
    # 保持测试器产物自包含，确保文件模式始终读取刚刚构建并复制过去的规则与配置。
    Sync-Configs
    Sync-HostLibraries
    Ensure-DatabaseLayout

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
