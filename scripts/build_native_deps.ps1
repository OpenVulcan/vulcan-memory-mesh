# build_native_deps.ps1 is the explicit, locked Cargo preparation step for native LanceDB.
# build_native_deps.ps1 是原生 LanceDB 唯一显式执行锁定 Cargo 制备的步骤。
Param(
    [string]$Target = "",
    [switch]$VerifyOnly
)

$ErrorActionPreference = "Stop"
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Definition
$RootDir = Split-Path -Parent $ScriptDir
$NativeSourceDir = Join-Path $RootDir "native\lancedb"
$NativeDepsRoot = Join-Path (Join-Path (Join-Path $RootDir "third_party") "deps") "native_lancedb"
$ValidatorScript = Join-Path $ScriptDir "validate_native_artifacts.ps1"
$EngineVersion = "0.39.0"

# NativeSupportFiles maps checked-in support inputs to the fixed cache/output manifest paths.
# NativeSupportFiles 将已纳入版本管理的支持输入映射到固定缓存/输出清单路径。
$NativeSupportFiles = @(
    @{ SourcePath = "include\vmm_lancedb.h"; ArtifactPath = "native_lancedb-support/include/vmm_lancedb.h" },
    @{ SourcePath = "licenses\THIRD_PARTY_NOTICES.txt"; ArtifactPath = "native_lancedb-support/licenses/THIRD_PARTY_NOTICES.txt" },
    @{ SourcePath = "licenses\lancedb-0.39.0-license-metadata.txt"; ArtifactPath = "native_lancedb-support/licenses/lancedb-0.39.0-license-metadata.txt" },
    @{ SourcePath = "licenses\lancedb-0.39.0-LICENSE"; ArtifactPath = "native_lancedb-support/licenses/lancedb-0.39.0-LICENSE" },
    @{ SourcePath = "licenses\jieba-rs-0.10.4-dictionary-notice.txt"; ArtifactPath = "native_lancedb-support/licenses/jieba-rs-0.10.4-dictionary-notice.txt" },
    @{ SourcePath = "licenses\jieba-rs-0.10.4-LICENSE"; ArtifactPath = "native_lancedb-support/licenses/jieba-rs-0.10.4-LICENSE" },
    @{ SourcePath = "licenses\gse-1.0.2-LICENSE"; ArtifactPath = "native_lancedb-support/licenses/gse-1.0.2-LICENSE" },
    @{ SourcePath = "licenses\gse-1.0.2-embedded-dictionary-notice.txt"; ArtifactPath = "native_lancedb-support/licenses/gse-1.0.2-embedded-dictionary-notice.txt" },
    @{ SourcePath = "licenses\cedar-0.30.0-LICENSE"; ArtifactPath = "native_lancedb-support/licenses/cedar-0.30.0-LICENSE" },
    @{ SourcePath = "licenses\modernc-sqlite-1.59.0-LICENSE"; ArtifactPath = "native_lancedb-support/licenses/modernc-sqlite-1.59.0-LICENSE" },
    @{ SourcePath = "licenses\modernc-libc-1.75.7-LICENSE"; ArtifactPath = "native_lancedb-support/licenses/modernc-libc-1.75.7-LICENSE" },
    @{ SourcePath = "licenses\modernc-mathutil-1.7.1-LICENSE"; ArtifactPath = "native_lancedb-support/licenses/modernc-mathutil-1.7.1-LICENSE" },
    @{ SourcePath = "licenses\modernc-memory-1.12.1-LICENSE"; ArtifactPath = "native_lancedb-support/licenses/modernc-memory-1.12.1-LICENSE" },
    @{ SourcePath = "licenses\go-humanize-1.0.1-LICENSE"; ArtifactPath = "native_lancedb-support/licenses/go-humanize-1.0.1-LICENSE" },
    @{ SourcePath = "licenses\go-isatty-0.0.24-LICENSE"; ArtifactPath = "native_lancedb-support/licenses/go-isatty-0.0.24-LICENSE" },
    @{ SourcePath = "licenses\go-strftime-1.0.0-LICENSE"; ArtifactPath = "native_lancedb-support/licenses/go-strftime-1.0.0-LICENSE" },
    @{ SourcePath = "licenses\bigfft-20230129092748-LICENSE"; ArtifactPath = "native_lancedb-support/licenses/bigfft-20230129092748-LICENSE" },
    @{ SourcePath = "licenses\x-sys-0.47.0-LICENSE"; ArtifactPath = "native_lancedb-support/licenses/x-sys-0.47.0-LICENSE" }
)

# Resolve-NativeTarget chooses the host target unless the caller explicitly supplies a cross-compilation target.
# Resolve-NativeTarget 在调用方未指定交叉编译 target 时选择当前宿主 target。
function Resolve-NativeTarget {
    if (-not [string]::IsNullOrWhiteSpace($Target)) {
        $SupportedTargets = @(
            "x86_64-pc-windows-msvc",
            "x86_64-unknown-linux-gnu",
            "aarch64-unknown-linux-gnu",
            "x86_64-apple-darwin",
            "aarch64-apple-darwin"
        )
        if ($SupportedTargets -notcontains $Target) { throw "unsupported native target: $Target" }
        return $Target
    }
    $Arch = [Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture.ToString().ToLowerInvariant()
    $ArchPart = switch ($Arch) { "x64" { "x86_64" } "arm64" { "aarch64" } default { throw "unsupported architecture for native LanceDB preparation: $Arch" } }
    if ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::Windows)) { return "$ArchPart-pc-windows-msvc" }
    if ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::Linux)) { return "$ArchPart-unknown-linux-gnu" }
    if ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::OSX)) { return "$ArchPart-apple-darwin" }
    throw "unsupported platform for native LanceDB preparation"
}

# Get-NativeLibraryName resolves the ABI filename from the target family without probing alternative names.
# Get-NativeLibraryName 根据 target 家族解析 ABI 文件名，不轮询候选名称。
function Get-NativeLibraryName {
    param([Parameter(Mandatory=$true)] [string]$TargetName)
    switch ($TargetName) {
        "x86_64-pc-windows-msvc" { return "vmm_lancedb_native.dll" }
        "x86_64-unknown-linux-gnu" { return "libvmm_lancedb_native.so" }
        "aarch64-unknown-linux-gnu" { return "libvmm_lancedb_native.so" }
        "x86_64-apple-darwin" { return "libvmm_lancedb_native.dylib" }
        "aarch64-apple-darwin" { return "libvmm_lancedb_native.dylib" }
        default { throw "unsupported native target: $TargetName" }
    }
}

# Get-Sha256 returns the lowercase SHA-256 digest used by cache keys and manifests.
# Get-Sha256 返回用于缓存键和清单的统一小写 SHA-256 摘要。
function Get-Sha256 {
    param([Parameter(Mandatory=$true)] [string]$Path)
    $Sha256 = [Security.Cryptography.SHA256]::Create()
    try {
        return ([BitConverter]::ToString($Sha256.ComputeHash([IO.File]::ReadAllBytes($Path)))).Replace("-", "").ToLowerInvariant()
    }
    finally { $Sha256.Dispose() }
}

# Get-SourceDigest hashes every source file except generated Cargo target output in stable path order.
# Get-SourceDigest 按稳定路径顺序对除生成 target 目录外的全部源码文件计算摘要。
function Get-SourceDigest {
    $SourceRootFull = ([IO.Path]::GetFullPath($NativeSourceDir)).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    $Lines = New-Object System.Collections.Generic.List[string]
    foreach ($File in (Get-ChildItem -LiteralPath $NativeSourceDir -Recurse -File | Where-Object { $_.FullName -notmatch '[\\/]target[\\/]' -and $_.FullName -notmatch '[\\/]\.git[\\/]' })) {
        $Relative = $File.FullName.Substring($SourceRootFull.Length).Replace("\", "/")
        $Lines.Add($Relative + "`0" + (Get-Sha256 -Path $File.FullName))
    }
    $Lines.Sort()
    $Digest = [Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($Digest.ComputeHash([Text.Encoding]::UTF8.GetBytes(($Lines -join "`n"))))).Replace("-", "").ToLowerInvariant() }
    finally { $Digest.Dispose() }
}

# Assert-NativeSupportInputs verifies every checked-in header and notice before Cargo or cache mutation.
# Assert-NativeSupportInputs 在 Cargo 或缓存变更前校验所有纳入版本管理的头文件和声明文件。
function Assert-NativeSupportInputs {
    foreach ($SupportFile in $NativeSupportFiles) {
        $SourcePath = Join-Path $NativeSourceDir $SupportFile.SourcePath
        if (-not (Test-Path -LiteralPath $SourcePath -PathType Leaf)) { throw "missing native support input: $SourcePath" }
        $Item = Get-Item -LiteralPath $SourcePath -Force
        if ($Item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "native support input must not be a reparse point: $SourcePath" }
    }
}

# Copy-NativeSupportInputs copies the fixed support set into the cache beside the native library.
# Copy-NativeSupportInputs 将固定支持文件集复制到动态库旁的缓存目录。
function Copy-NativeSupportInputs {
    param([Parameter(Mandatory=$true)] [string]$CacheDir)
    $ManifestArtifacts = @()
    foreach ($SupportFile in $NativeSupportFiles) {
        $SourcePath = Join-Path $NativeSourceDir $SupportFile.SourcePath
        $Destination = Join-Path $CacheDir ($SupportFile.ArtifactPath.Replace("/", [IO.Path]::DirectorySeparatorChar))
        $DestinationParent = Split-Path $Destination -Parent
        Assert-CacheDirectory -Path $DestinationParent -Create
        if (Test-Path -LiteralPath $Destination) {
            $Existing = Get-Item -LiteralPath $Destination -Force
            if ($Existing.PSIsContainer -or ($Existing.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw "native support cache path must be a regular file: $Destination" }
        }
        Copy-Item -LiteralPath $SourcePath -Destination $Destination -Force
        $ManifestArtifacts += [pscustomobject]@{ path = $SupportFile.ArtifactPath; sha256 = Get-Sha256 -Path $Destination }
    }
    return @($ManifestArtifacts)
}

# Assert-CacheDirectory keeps native cache writes inside the repository and rejects reparse points at every existing level.
# Assert-CacheDirectory 将原生缓存写入限制在仓库内，并拒绝每一级已存在的重解析点。
function Assert-CacheDirectory {
    param([Parameter(Mandatory=$true)] [string]$Path, [switch]$Create)
    $RootFull = [IO.Path]::GetFullPath($RootDir).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    $PathFull = [IO.Path]::GetFullPath($Path)
    if ($PathFull -ne $RootFull -and -not $PathFull.StartsWith($RootFull + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) { throw "native cache path is outside repository: $Path" }
    $Relative = if ($PathFull -eq $RootFull) { @() } else { $PathFull.Substring($RootFull.Length + 1) -split '[\\/]' }
    $Current = $RootDir
    foreach ($Part in $Relative) {
        if ([string]::IsNullOrWhiteSpace($Part) -or $Part -eq "." -or $Part -eq "..") { throw "invalid native cache path: $Path" }
        $Current = Join-Path $Current $Part
        if (-not (Test-Path -LiteralPath $Current)) {
            if (-not $Create) { throw "missing native cache directory: $Current" }
            New-Item -ItemType Directory -Path $Current -Force | Out-Null
        }
        $Item = Get-Item -LiteralPath $Current -Force
        if (-not $Item.PSIsContainer -or ($Item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw "native cache path must be a real directory: $Current" }
    }
}

# Invoke-NativeValidator checks the prepared manifest and library before current.json is published.
# Invoke-NativeValidator 在发布 current.json 前校验已制备清单和动态库。
function Invoke-NativeValidator {
    param(
        [Parameter(Mandatory=$true)] [string]$ManifestPath,
        [Parameter(Mandatory=$true)] [string]$LibraryPath,
        [Parameter(Mandatory=$true)] [string]$TargetName,
        [Parameter(Mandatory=$true)] [string]$SourceDigest,
        [Parameter(Mandatory=$true)] [string]$CargoLockDigest
    )
    & $ValidatorScript -ManifestPath $ManifestPath -LibraryPath $LibraryPath -Target $TargetName -ExpectedSourceDigest $SourceDigest -ExpectedCargoLockSha256 $CargoLockDigest -ExpectedEngineVersion $EngineVersion
    if (-not $?) { throw "native artifact validation failed" }
}

# Assert-ProtocAvailable reports the protobuf compiler requirement before Cargo starts a long build.
# Assert-ProtocAvailable 在 Cargo 长时间构建前明确报告 protobuf 编译器依赖。
# Resolve-VendoredProtoc finds exactly one platform-specific protoc binary in Cargo's registry cache.
# Resolve-VendoredProtoc 在 Cargo registry 缓存中查找唯一的平台匹配 protoc 文件。
function Resolve-VendoredProtoc {
    $CargoHome = [string]$env:CARGO_HOME
    if ([string]::IsNullOrWhiteSpace($CargoHome)) { $CargoHome = Join-Path $HOME ".cargo" }
    $RegistrySource = Join-Path (Join-Path $CargoHome "registry") "src"
    if (-not (Test-Path -LiteralPath $RegistrySource -PathType Container)) { return "" }
    $BinaryName = if ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::Windows)) { "protoc.exe" } else { "protoc" }
    $PackagePattern = if ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::Windows)) { "protoc-bin-vendored-win32-*" } elseif ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::Linux)) { "protoc-bin-vendored-linux-*" } elseif ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::OSX)) { "protoc-bin-vendored-macos-*" } else { return "" }
    $Candidates = New-Object System.Collections.Generic.List[string]
    foreach ($RegistryRoot in (Get-ChildItem -LiteralPath $RegistrySource -Directory -ErrorAction SilentlyContinue)) {
        foreach ($PackageRoot in (Get-ChildItem -LiteralPath $RegistryRoot.FullName -Directory -Filter $PackagePattern -ErrorAction SilentlyContinue)) {
            $Candidate = Join-Path (Join-Path $PackageRoot.FullName "bin") $BinaryName
            if (Test-Path -LiteralPath $Candidate -PathType Leaf) {
                $Item = Get-Item -LiteralPath $Candidate -Force
                if (-not ($Item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { [void]$Candidates.Add($Item.FullName) }
            }
        }
    }
    $UniqueCandidates = @($Candidates | Sort-Object -Unique)
    if ($UniqueCandidates.Count -gt 1) { throw "multiple vendored protoc binaries found; set PROTOC explicitly: $($UniqueCandidates -join '; ')" }
    if ($UniqueCandidates.Count -eq 1) { return $UniqueCandidates[0] }
    return ""
}

function Assert-ProtocAvailable {
    $ConfiguredProtoc = [string]$env:PROTOC
    if (-not [string]::IsNullOrWhiteSpace($ConfiguredProtoc)) {
        if (-not (Test-Path -LiteralPath $ConfiguredProtoc -PathType Leaf)) { throw "PROTOC does not point to a file: $ConfiguredProtoc" }
        return
    }
    $VendoredProtoc = Resolve-VendoredProtoc
    if (-not [string]::IsNullOrWhiteSpace($VendoredProtoc)) {
        $env:PROTOC = $VendoredProtoc
        Write-Output "using vendored protoc: $VendoredProtoc"
        return
    }
    if ($null -eq (Get-Command protoc -CommandType Application -ErrorAction SilentlyContinue)) { throw "protoc is required by the locked native dependency graph; install protoc or set PROTOC" }
}

# Write-JsonAtomically writes only the owned selection or manifest file and never clears its containing cache directory.
# Write-JsonAtomically 只写入本步骤拥有的选择或清单文件，不清空所在缓存目录。
function Write-JsonAtomically {
    param([Parameter(Mandatory=$true)] [string]$Path, [Parameter(Mandatory=$true)] [object]$Value)
    Assert-CacheDirectory -Path (Split-Path $Path -Parent)
    if (Test-Path -LiteralPath $Path) {
        if ((Get-Item -LiteralPath $Path -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "native selection path must not be a reparse point: $Path" }
    }
    $TempPath = "$Path.tmp.$PID"
    if (Test-Path -LiteralPath $TempPath -PathType Leaf) {
        if ((Get-Item -LiteralPath $TempPath -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "native temporary path must not be a reparse point: $TempPath" }
    }
    $Value | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $TempPath -Encoding UTF8
    Move-Item -LiteralPath $TempPath -Destination $Path -Force
}

# Resolve-CurrentSelection reads one exact current.json entry for verify-only checks.
# Resolve-CurrentSelection 读取一份精确 current.json，供只校验模式使用。
function Resolve-CurrentSelection {
    param([Parameter(Mandatory=$true)] [string]$TargetName)
    $TargetRoot = Join-Path $NativeDepsRoot $TargetName
    Assert-CacheDirectory -Path $TargetRoot
    $CurrentPath = Join-Path $TargetRoot "current.json"
    if (-not (Test-Path -LiteralPath $CurrentPath -PathType Leaf)) { throw "missing native current.json: $CurrentPath" }
    if ((Get-Item -LiteralPath $CurrentPath -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "native current.json must not be a reparse point: $CurrentPath" }
    $Current = Get-Content -LiteralPath $CurrentPath -Raw | ConvertFrom-Json
    if ([int]$Current.schema_version -ne 1 -or [string]$Current.target -ne $TargetName) { throw "native current.json schema or target mismatch" }
    if ([string]$Current.source_digest -notmatch '^[0-9a-fA-F]{64}$' -or [string]$Current.manifest_path -ne "$($Current.source_digest)/manifest.json") { throw "native current.json selection is invalid" }
    $ManifestPath = Join-Path $TargetRoot ([string]$Current.manifest_path).Replace("/", [IO.Path]::DirectorySeparatorChar)
    Assert-CacheDirectory -Path (Split-Path $ManifestPath -Parent)
    $Manifest = Get-Content -LiteralPath $ManifestPath -Raw | ConvertFrom-Json
    $LibraryPath = Join-Path (Split-Path $ManifestPath -Parent) ([string]$Manifest.library_file)
    Invoke-NativeValidator -ManifestPath $ManifestPath -LibraryPath $LibraryPath -TargetName $TargetName -SourceDigest ([string]$Current.source_digest) -CargoLockDigest ([string]$Manifest.cargo_lock_sha256)
}

$ResolvedTarget = Resolve-NativeTarget
if (-not (Test-Path -LiteralPath $NativeSourceDir -PathType Container)) { throw "missing native LanceDB source directory: $NativeSourceDir" }
if (-not (Test-Path -LiteralPath $ValidatorScript -PathType Leaf)) { throw "missing native artifact validator: $ValidatorScript" }

if ($VerifyOnly) {
    Resolve-CurrentSelection -TargetName $ResolvedTarget
    Write-Output "native dependency verification succeeded: $ResolvedTarget"
    exit 0
}

$CargoTomlPath = Join-Path $NativeSourceDir "Cargo.toml"
$CargoLockPath = Join-Path $NativeSourceDir "Cargo.lock"
if (-not (Test-Path -LiteralPath $CargoTomlPath -PathType Leaf) -or -not (Test-Path -LiteralPath $CargoLockPath -PathType Leaf)) { throw "native source must contain Cargo.toml and Cargo.lock" }
Assert-NativeSupportInputs
Assert-ProtocAvailable
$SourceDigest = Get-SourceDigest
$CargoLockDigest = Get-Sha256 -Path $CargoLockPath
$LibraryName = Get-NativeLibraryName -TargetName $ResolvedTarget
$CargoTargetDir = Join-Path $NativeSourceDir "target"
$CargoArguments = @("build", "--manifest-path", $CargoTomlPath, "--locked", "--release", "--target", $ResolvedTarget, "--target-dir", $CargoTargetDir)
Write-Output "native dependency build: target=$ResolvedTarget; source_digest=$SourceDigest; cargo_lock_sha256=$CargoLockDigest"
& cargo @CargoArguments
if ($LASTEXITCODE -ne 0) { throw "cargo build failed for native LanceDB ($LASTEXITCODE)" }
$BuiltLibraryPath = Join-Path (Join-Path (Join-Path $CargoTargetDir $ResolvedTarget) "release") $LibraryName
if (-not (Test-Path -LiteralPath $BuiltLibraryPath -PathType Leaf)) { throw "Cargo did not produce expected native library: $BuiltLibraryPath" }
$RustcVersion = (& rustc --version 2>$null)
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($RustcVersion)) { throw "cannot resolve rustc version for native manifest" }
$CacheTargetDir = Join-Path $NativeDepsRoot $ResolvedTarget
$CacheDir = Join-Path $CacheTargetDir $SourceDigest
Assert-CacheDirectory -Path $CacheDir -Create
$ManifestArtifacts = @(Copy-NativeSupportInputs -CacheDir $CacheDir)
$CachedLibraryPath = Join-Path $CacheDir $LibraryName
$ManifestPath = Join-Path $CacheDir "manifest.json"
if (Test-Path -LiteralPath $CachedLibraryPath) {
    $CachedLibraryItem = Get-Item -LiteralPath $CachedLibraryPath -Force
    if ($CachedLibraryItem.PSIsContainer -or ($CachedLibraryItem.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw "native cached library path must be a regular file: $CachedLibraryPath" }
}
Copy-Item -LiteralPath $BuiltLibraryPath -Destination $CachedLibraryPath -Force
$Manifest = [ordered]@{
    schema_version = 1
    abi_version = 1
    engine_version = $EngineVersion
    target = $ResolvedTarget
    rustc_version = $RustcVersion.Trim()
    source_digest = $SourceDigest
    cargo_lock_sha256 = $CargoLockDigest
    library_file = $LibraryName
    library_sha256 = Get-Sha256 -Path $CachedLibraryPath
    artifacts = $ManifestArtifacts
}
Write-JsonAtomically -Path $ManifestPath -Value $Manifest
Invoke-NativeValidator -ManifestPath $ManifestPath -LibraryPath $CachedLibraryPath -TargetName $ResolvedTarget -SourceDigest $SourceDigest -CargoLockDigest $CargoLockDigest | Out-Null
$CurrentTargetDir = $CacheTargetDir
Assert-CacheDirectory -Path $CurrentTargetDir -Create
$Current = [ordered]@{
    schema_version = 1
    target = $ResolvedTarget
    source_digest = $SourceDigest
    manifest_path = "$SourceDigest/manifest.json"
    library_file = $LibraryName
}
Write-JsonAtomically -Path (Join-Path $CurrentTargetDir "current.json") -Value $Current
Write-Output "native dependency ready: $ManifestPath"
