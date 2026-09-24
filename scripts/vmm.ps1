# scripts/vmm.ps1 builds the standalone VMM package and owns only its declared output artifacts.
# scripts/vmm.ps1 用于构建独立 VMM 交付包，并且只管理明确声明的构建产物。
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
$ConfigDir = Join-Path $OutputDir "configs"
$DatabaseDir = Join-Path $OutputDir "database"
$ConfigOwnershipFile = Join-Path $OutputDir ".vmm-config-owned"
$ExePath = Join-Path $BinDir "vmm-local.exe"
$MigrateExePath = Join-Path $BinDir "vmm-migrate.exe"
$TesterExePath = Join-Path $BinDir "vmm-pii-tester.exe"
$ThirdPartyDepsDir = Join-Path (Join-Path $RootDir "third_party") "deps"
$NativeDepsRoot = Join-Path $ThirdPartyDepsDir "native_lancedb"
$NativeValidatorScript = Join-Path $ScriptDir "validate_native_artifacts.ps1"
$HostDependencyChecksumManifest = Join-Path $ScriptDir "host_deps_sha256.tsv"
$NativeEngineVersion = "0.39.0"
$SourceRevision = "unknown"
$SourceStateDigest = "unknown"

# NativeSupportArtifactPaths is the fixed support-file allowlist copied beside the native library.
# NativeSupportArtifactPaths 是复制到原生动态库旁的固定支持文件白名单。
$NativeSupportArtifactPaths = @(
    "native_lancedb-support/include/vmm_lancedb.h",
    "native_lancedb-support/licenses/THIRD_PARTY_NOTICES.txt",
    "native_lancedb-support/licenses/lancedb-0.39.0-license-metadata.txt",
    "native_lancedb-support/licenses/lancedb-0.39.0-LICENSE",
    "native_lancedb-support/licenses/jieba-rs-0.10.4-dictionary-notice.txt",
    "native_lancedb-support/licenses/jieba-rs-0.10.4-LICENSE",
    "native_lancedb-support/licenses/gse-1.0.2-LICENSE",
    "native_lancedb-support/licenses/gse-1.0.2-embedded-dictionary-notice.txt",
    "native_lancedb-support/licenses/cedar-0.30.0-LICENSE",
    "native_lancedb-support/licenses/modernc-sqlite-1.59.0-LICENSE",
    "native_lancedb-support/licenses/modernc-libc-1.75.7-LICENSE",
    "native_lancedb-support/licenses/modernc-mathutil-1.7.1-LICENSE",
    "native_lancedb-support/licenses/modernc-memory-1.12.1-LICENSE",
    "native_lancedb-support/licenses/go-humanize-1.0.1-LICENSE",
    "native_lancedb-support/licenses/go-isatty-0.0.24-LICENSE",
    "native_lancedb-support/licenses/go-strftime-1.0.0-LICENSE",
    "native_lancedb-support/licenses/bigfft-20230129092748-LICENSE",
    "native_lancedb-support/licenses/x-sys-0.47.0-LICENSE"
)

# Get-Sha256 returns a PowerShell 5.1-compatible lowercase SHA-256 digest for one regular file.
# Get-Sha256 为单个普通文件返回兼容 PowerShell 5.1 的小写 SHA-256 摘要。
function Get-Sha256 {
    param([Parameter(Mandatory=$true)] [string]$Path)
    $Sha256 = [Security.Cryptography.SHA256]::Create()
    try {
        return ([BitConverter]::ToString($Sha256.ComputeHash([IO.File]::ReadAllBytes($Path)))).Replace("-", "").ToLowerInvariant()
    }
    finally { $Sha256.Dispose() }
}

# Resolve-BuildProfile accepts only the documented standard and release forms so build arguments cannot be silently ignored.
# Resolve-BuildProfile 仅接受文档规定的标准与 release 形式，避免构建参数被静默忽略。
function Resolve-BuildProfile {
    param([string[]]$Arguments = @())
    if ($Arguments.Count -eq 0) { return "standard" }
    if ($Arguments.Count -eq 1 -and $Arguments[0].Trim().ToLowerInvariant() -eq "release") { return "release" }
    throw "unsupported build arguments: $($Arguments -join ' '). Use 'build' or 'build release'."
}

# Resolve-StorageProfile reads the build-only dependency profile and rejects unknown values before output changes.
# Resolve-StorageProfile 读取仅用于构建的依赖配置，并在修改输出前拒绝未知值。
function Resolve-StorageProfile {
    $RawProfile = [string]$env:VMM_BUILD_STORAGE_PROFILE
    if ([string]::IsNullOrWhiteSpace($RawProfile)) { return "legacy" }
    $Profile = $RawProfile.Trim().ToLowerInvariant()
    if ($Profile -notin @("legacy", "native", "all")) { throw "unsupported VMM_BUILD_STORAGE_PROFILE '$RawProfile'. Use legacy, native, or all." }
    return $Profile
}

# Assert-SafeOutputDirectory rejects reparse points so cleanup cannot escape the formal output tree.
# Assert-SafeOutputDirectory 拒绝重解析点，确保清理不会逃逸正式 output 目录。
function Assert-SafeOutputDirectory {
    if (Test-Path -LiteralPath $OutputDir) {
        $OutputItem = Get-Item -LiteralPath $OutputDir -Force
        if (-not $OutputItem.PSIsContainer -or ($OutputItem.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw "output path must be a real directory: $OutputDir" }
    }
    else { New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null }
}

# Assert-SafeOutputPath validates one output-relative path and every existing parent before access or deletion.
# Assert-SafeOutputPath 校验一个相对于 output 的路径及其所有已存在父级，然后才允许访问或删除。
function Assert-SafeOutputPath {
    param([Parameter(Mandatory=$true)] [string]$Path, [switch]$AllowMissing)
    Assert-SafeOutputDirectory
    $RootFull = [IO.Path]::GetFullPath($OutputDir).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    $PathFull = [IO.Path]::GetFullPath($Path)
    if ($PathFull -eq $RootFull -or -not $PathFull.StartsWith($RootFull + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) { throw "refusing to access a path outside output: $Path" }
    $Relative = $PathFull.Substring($RootFull.Length + 1)
    $Current = $OutputDir
    foreach ($Part in ($Relative -split '[\\/]')) {
        if ([string]::IsNullOrWhiteSpace($Part) -or $Part -eq "." -or $Part -eq "..") { throw "invalid output-relative path: $Path" }
        $Current = Join-Path $Current $Part
        if (Test-Path -LiteralPath $Current) {
            $Item = Get-Item -LiteralPath $Current -Force
            if ($Item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "refusing to follow a reparse point in output: $Current" }
        }
        elseif (-not $AllowMissing) { throw "expected output path does not exist: $Current" }
    }
}

# Resolve-SourceIdentity captures the exact source revision and source-state digest used for a local build.
# Resolve-SourceIdentity 记录本次本地构建使用的精确源码版本与源码状态摘要。
function Resolve-SourceIdentity {
    $Revision = (& git -C $RootDir rev-parse HEAD 2>$null)
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($Revision)) { throw "cannot resolve VMM source revision from git" }
    $script:SourceRevision = $Revision.Trim()
    $RelativeFiles = @(& git -C $RootDir ls-files -co --exclude-standard)
    if ($LASTEXITCODE -ne 0) { throw "cannot enumerate VMM source files from git" }
    $Lines = New-Object System.Collections.Generic.List[string]
    foreach ($RelativeFile in $RelativeFiles) {
        if ([string]::IsNullOrWhiteSpace($RelativeFile)) { continue }
        $AbsoluteFile = Join-Path $RootDir $RelativeFile
        if (-not (Test-Path -LiteralPath $AbsoluteFile -PathType Leaf)) { continue }
        $FileDigest = Get-Sha256 -Path $AbsoluteFile
        $Lines.Add(($RelativeFile.Replace("\", "/") + "`0" + $FileDigest))
    }
    $Lines.Sort()
    $Digest = [Security.Cryptography.SHA256]::Create()
    try {
        $Bytes = [Text.Encoding]::UTF8.GetBytes(($Lines -join "`n"))
        $script:SourceStateDigest = ([BitConverter]::ToString($Digest.ComputeHash($Bytes))).Replace("-", "").ToLowerInvariant()
    }
    finally { $Digest.Dispose() }
}

# Resolve-GoExe locates go.exe lazily so clean and run can work with packaged binaries only.
# Resolve-GoExe 按需定位 go.exe，使 clean 和 run 在仅携带打包二进制的环境中仍可工作。
function Resolve-GoExe {
    if ($script:ResolvedGoExe) { return $script:ResolvedGoExe }
    $GoCommand = Get-Command go -CommandType Application -ErrorAction Stop | Select-Object -First 1
    $script:ResolvedGoExe = $GoCommand.Source
    return $script:ResolvedGoExe
}

# Get-VldbLibraryNames returns the legacy dynamic-library names for the current host.
# Get-VldbLibraryNames 返回当前宿主对应的 legacy 动态库文件名。
function Get-VldbLibraryNames {
    if ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::Windows)) { return @("vldb_sqlite.dll", "vldb_lancedb.dll") }
    if ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::Linux)) { return @("libvldb_sqlite.so", "libvldb_lancedb.so") }
    if ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::OSX)) { return @("libvldb_sqlite.dylib", "libvldb_lancedb.dylib") }
    throw "unsupported platform for host library packaging"
}

# Get-NativeTarget maps the host to the exact Rust target used by native dependency preparation.
# Get-NativeTarget 将宿主映射到原生依赖制备使用的精确 Rust target。
function Get-NativeTarget {
    $Arch = [Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture.ToString().ToLowerInvariant()
    $ArchPart = switch ($Arch) { "x64" { "x86_64" } "arm64" { "aarch64" } default { throw "unsupported architecture for native LanceDB packaging: $Arch" } }
    if ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::Windows)) { return "$ArchPart-pc-windows-msvc" }
    if ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::Linux)) { return "$ArchPart-unknown-linux-gnu" }
    if ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::OSX)) { return "$ArchPart-apple-darwin" }
    throw "unsupported platform for native LanceDB packaging"
}

# Get-NativeLibraryName returns the one dynamic-library filename declared by the native ABI contract.
# Get-NativeLibraryName 返回原生 ABI 契约声明的动态库文件名。
function Get-NativeLibraryName {
    if ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::Windows)) { return "vmm_lancedb_native.dll" }
    if ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::Linux)) { return "libvmm_lancedb_native.so" }
    if ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::OSX)) { return "libvmm_lancedb_native.dylib" }
    throw "unsupported platform for native LanceDB packaging"
}

# Get-ControllerBinaryName returns the legacy controller artifact name for exact cleanup and packaging.
# Get-ControllerBinaryName 返回 legacy controller 产物名，用于精确清理和打包。
function Get-ControllerBinaryName {
    if ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::Windows)) { return "vldb-controller.exe" }
    return "vldb-controller"
}

# Remove-SafeTemporaryDirectory removes only a directory created by this process under the system temp root.
# Remove-SafeTemporaryDirectory 只删除本进程在系统临时根下创建的目录。
function Remove-SafeTemporaryDirectory {
    param([Parameter(Mandatory=$true)] [pscustomobject]$State)
    if ([string]::IsNullOrWhiteSpace([string]$State.Path) -or [string]::IsNullOrWhiteSpace([string]$State.Root) -or [string]::IsNullOrWhiteSpace([string]$State.Token) -or [string]::IsNullOrWhiteSpace([string]$State.OwnerPath)) {
        throw "temporary directory ownership state is incomplete / 临时目录所有权状态不完整。"
    }
    $RootFullPath = [IO.Path]::GetFullPath($State.Root).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    $DirectoryFullPath = [IO.Path]::GetFullPath($State.Path)
    if ($DirectoryFullPath -eq $RootFullPath -or -not $DirectoryFullPath.StartsWith($RootFullPath + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw "refusing to remove temporary path outside system temp root: $DirectoryFullPath"
    }
    $ExpectedOwnerPath = Join-Path $DirectoryFullPath (".vmm-temp-owner-{0}" -f $State.Token)
    if ([IO.Path]::GetFullPath([string]$State.OwnerPath) -ne [IO.Path]::GetFullPath($ExpectedOwnerPath)) {
        throw "temporary directory ownership marker path mismatch: $DirectoryFullPath"
    }
    if (-not (Test-Path -LiteralPath $DirectoryFullPath)) { return }
    $DirectoryItem = Get-Item -LiteralPath $DirectoryFullPath -Force
    if ($DirectoryItem.Attributes -band [IO.FileAttributes]::ReparsePoint) {
        throw "refusing to remove a reparse-point temporary directory: $DirectoryFullPath"
    }
    if (-not (Test-Path -LiteralPath $ExpectedOwnerPath -PathType Leaf)) {
        throw "temporary directory ownership marker is missing: $DirectoryFullPath"
    }
    $OwnerItem = Get-Item -LiteralPath $ExpectedOwnerPath -Force
    if ($OwnerItem.Attributes -band [IO.FileAttributes]::ReparsePoint) {
        throw "temporary directory ownership marker must not be a reparse point: $ExpectedOwnerPath"
    }
    if ([IO.File]::ReadAllText($ExpectedOwnerPath) -cne [string]$State.Token) {
        throw "temporary directory ownership marker mismatch: $DirectoryFullPath"
    }
    Remove-Item -LiteralPath $DirectoryFullPath -Recurse -Force -ErrorAction Stop
}

# New-SafeTemporaryDirectory creates a random exclusive workspace and records an in-memory ownership token.
# New-SafeTemporaryDirectory 创建随机独占工作区，并记录内存中的所有权令牌。
function New-SafeTemporaryDirectory {
    param([Parameter(Mandatory=$true)] [string]$Prefix)
    $TempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    if (-not (Test-Path -LiteralPath $TempRoot -PathType Container)) { throw "system temp root is unavailable: $TempRoot" }
    $TempRootItem = Get-Item -LiteralPath $TempRoot -Force
    if ($TempRootItem.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "system temp root must not be a reparse point: $TempRoot" }
    $SafePrefix = ($Prefix -replace '[^A-Za-z0-9._-]', '_')
    if ([string]::IsNullOrWhiteSpace($SafePrefix)) { $SafePrefix = "vmm" }
    for ($Attempt = 0; $Attempt -lt 20; $Attempt++) {
        $OwnerToken = [Guid]::NewGuid().ToString("N")
        $Candidate = Join-Path $TempRoot ("{0}_{1}_{2}" -f $SafePrefix, $OwnerToken, $PID)
        if (Test-Path -LiteralPath $Candidate) { continue }
        try {
            $Created = New-Item -ItemType Directory -Path $Candidate -ErrorAction Stop
        }
        catch {
            if (Test-Path -LiteralPath $Candidate) { continue }
            throw
        }
        $CreatedItem = Get-Item -LiteralPath $Created.FullName -Force
        if ($CreatedItem.Attributes -band [IO.FileAttributes]::ReparsePoint) {
            throw "new temporary directory unexpectedly became a reparse point: $($CreatedItem.FullName)"
        }
        $OwnerPath = Join-Path $CreatedItem.FullName (".vmm-temp-owner-{0}" -f $OwnerToken)
        try {
            [IO.File]::WriteAllText($OwnerPath, $OwnerToken, [Text.Encoding]::ASCII)
        }
        catch {
            throw
        }
        return [pscustomobject]@{ Path = $CreatedItem.FullName; Root = $TempRoot; Token = $OwnerToken; OwnerPath = $OwnerPath }
    }
    throw "unable to create an exclusive temporary directory under $TempRoot"
}

# Get-PinnedHostArchiveHash resolves one exact archive digest from the checked-in manifest.
# Get-PinnedHostArchiveHash 从仓库内固定清单解析唯一压缩包摘要。
function Get-PinnedHostArchiveHash {
    param(
        [Parameter(Mandatory=$true)] [string]$Repo,
        [Parameter(Mandatory=$true)] [string]$Tag,
        [Parameter(Mandatory=$true)] [string]$Asset
    )
    if (-not (Test-Path -LiteralPath $HostDependencyChecksumManifest -PathType Leaf)) {
        throw "missing host dependency checksum manifest: $HostDependencyChecksumManifest"
    }
    $ManifestItem = Get-Item -LiteralPath $HostDependencyChecksumManifest -Force
    if ($ManifestItem.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "host dependency checksum manifest must not be a reparse point: $HostDependencyChecksumManifest" }
    $Rows = New-Object System.Collections.Generic.List[object]
    $Seen = @{}
    $LineNumber = 0
    foreach ($Line in (Get-Content -LiteralPath $HostDependencyChecksumManifest -Encoding UTF8)) {
        $LineNumber++
        if ([string]::IsNullOrWhiteSpace($Line) -or $Line.TrimStart().StartsWith("#")) { continue }
        $Parts = $Line -split "`t"
        if ($Parts.Count -ne 4 -or ($Parts | Where-Object { [string]::IsNullOrWhiteSpace($_) }).Count -gt 0) {
            throw "invalid host dependency checksum manifest row $LineNumber"
        }
        $Hash = [string]$Parts[3]
        if ($Hash -notmatch '^[0-9a-fA-F]{64}$') { throw "invalid host dependency SHA256 at manifest row $LineNumber" }
        $Key = "$($Parts[0])`t$($Parts[1])`t$($Parts[2])"
        if ($Seen.ContainsKey($Key)) { throw "duplicate host dependency checksum manifest row $LineNumber" }
        $Seen[$Key] = $true
        [void]$Rows.Add([pscustomobject]@{ Repo = [string]$Parts[0]; Tag = [string]$Parts[1]; Asset = [string]$Parts[2]; Hash = $Hash.ToLowerInvariant() })
    }
    if ($Rows.Count -ne 15) { throw "host dependency checksum manifest must contain exactly 15 records" }
    $Matches = @($Rows | Where-Object { $_.Repo -ceq $Repo -and $_.Tag -ceq $Tag -and $_.Asset -ceq $Asset })
    if ($Matches.Count -ne 1) { throw "missing or duplicate pinned SHA256 for $Repo $Tag $Asset" }
    return ([string]$Matches[0].Hash).ToLowerInvariant()
}

# Get-HostDependencyReleaseInfo maps one installed artifact to its immutable official archive.
# Get-HostDependencyReleaseInfo 将一个已安装产物映射到不可变的官方压缩包。
function Get-HostDependencyReleaseInfo {
    param([Parameter(Mandatory=$true)] [ValidateSet("sqlite", "lancedb", "controller")] [string]$Kind)
    $Target = Get-NativeTarget
    $ArchiveExtension = if ([Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::Windows)) { ".zip" } else { ".tar.gz" }
    switch ($Kind) {
        "sqlite" { return [pscustomobject]@{ Repo = "OpenVulcan/vldb-sqlite"; Tag = "v0.1.7"; Asset = "vldb-sqlite-lib-v0.1.7-$Target$ArchiveExtension"; MarkerDirectoryName = "vldb_sqlite" } }
        "lancedb" { return [pscustomobject]@{ Repo = "OpenVulcan/vldb-lancedb"; Tag = "v0.1.5"; Asset = "vldb-lancedb-lib-v0.1.5-$Target$ArchiveExtension"; MarkerDirectoryName = "vldb_lancedb" } }
        "controller" { return [pscustomobject]@{ Repo = "OpenVulcan/vldb-controller"; Tag = "v0.2.4"; Asset = "vldb-controller-v0.2.4-$Target$ArchiveExtension"; MarkerDirectoryName = "vldb_controller" } }
    }
}

# Get-VerifiedHostDependencyPath revalidates the official archive and extracted artifact before packaging.
# Get-VerifiedHostDependencyPath 在打包前重新校验官方压缩包及其解压产物。
function Get-VerifiedHostDependencyPath {
    param([Parameter(Mandatory=$true)] [ValidateSet("sqlite", "lancedb", "controller")] [string]$Kind, [Parameter(Mandatory=$true)] [string]$FileName)
    $SourcePath = Join-Path $ThirdPartyDepsDir $FileName
    if (-not (Test-Path -LiteralPath $SourcePath -PathType Leaf)) { throw "missing host dependency: $SourcePath. Run '.\make.ps1 deps host' first." }
    $SourceItem = Get-Item -LiteralPath $SourcePath -Force
    if ($SourceItem.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "host dependency must not be a reparse point: $SourcePath" }
    $ReleaseInfo = Get-HostDependencyReleaseInfo -Kind $Kind
    $MarkerDirectory = Join-Path (Split-Path $ThirdPartyDepsDir -Parent) $ReleaseInfo.MarkerDirectoryName
    if (-not (Test-Path -LiteralPath $MarkerDirectory -PathType Container)) { throw "missing host dependency cache directory: $MarkerDirectory" }
    $MarkerDirectoryItem = Get-Item -LiteralPath $MarkerDirectory -Force
    if ($MarkerDirectoryItem.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "host dependency cache directory must not be a reparse point: $MarkerDirectory" }
    $ArchivePath = Join-Path $MarkerDirectory $ReleaseInfo.Asset
    if (-not (Test-Path -LiteralPath $ArchivePath -PathType Leaf)) { throw "missing trusted host dependency archive: $ArchivePath. Run '.\make.ps1 deps host' first." }
    $ArchiveItem = Get-Item -LiteralPath $ArchivePath -Force
    if ($ArchiveItem.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "host dependency archive must not be a reparse point: $ArchivePath" }
    $ExpectedArchiveHash = Get-PinnedHostArchiveHash -Repo $ReleaseInfo.Repo -Tag $ReleaseInfo.Tag -Asset $ReleaseInfo.Asset
    $ActualArchiveHash = Get-Sha256 -Path $ArchivePath
    if ($ActualArchiveHash -cne $ExpectedArchiveHash) { throw "host dependency archive checksum mismatch: $ArchivePath" }
    $Markers = @(Get-ChildItem -LiteralPath $MarkerDirectory -Filter ".installed-*" -File -ErrorAction SilentlyContinue)
    if ($Markers.Count -ne 1) { throw "expected exactly one installed marker for $Kind under $MarkerDirectory" }
    if ($Markers[0].Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "host dependency marker must not be a reparse point: $($Markers[0].FullName)" }
    $ExpectedMarkerName = ".installed-$($ReleaseInfo.Tag)-$((Get-NativeTarget))"
    if ($Markers[0].Name -cne $ExpectedMarkerName) { throw "host dependency marker identity mismatch: $($Markers[0].FullName)" }
    $MarkerLines = @(Get-Content -LiteralPath $Markers[0].FullName -Encoding ASCII)
    if ($MarkerLines.Count -ne 2) { throw "host dependency marker must contain exactly two lines: $($Markers[0].FullName)" }
    $MarkerArchiveHash = [string]$MarkerLines[0]
    $MarkerInstalledHash = [string]$MarkerLines[1]
    if ($MarkerArchiveHash -notmatch '^[0-9a-f]{64}$' -or $MarkerInstalledHash -notmatch '^[0-9a-f]{64}$') { throw "host dependency marker must contain two lowercase SHA256 values: $($Markers[0].FullName)" }
    if ($MarkerArchiveHash -cne $ExpectedArchiveHash) { throw "host dependency marker archive checksum mismatch: $($Markers[0].FullName)" }

    # Re-extract the fixed archive so a jointly forged marker and installed file cannot pass the package gate.
    # 重新解压固定压缩包，防止伪造 marker 与已安装文件共同绕过打包校验。
    $VerifyState = New-SafeTemporaryDirectory -Prefix ("vmm-host-verify_{0}" -f $Kind)
    $VerifyDir = $VerifyState.Path
    try {
        if ($ArchivePath.EndsWith(".zip", [StringComparison]::OrdinalIgnoreCase)) {
            Expand-Archive -LiteralPath $ArchivePath -DestinationPath $VerifyDir -Force
        }
        else {
            $TarCommand = Get-Command tar -CommandType Application -ErrorAction Stop | Select-Object -First 1
            & $TarCommand.Source -xzf $ArchivePath -C $VerifyDir
            if ($LASTEXITCODE -ne 0) { throw "failed to extract trusted host dependency archive: $ArchivePath" }
        }
        $TrustedCandidates = @(Get-ChildItem -LiteralPath $VerifyDir -Recurse -File -Filter $FileName -ErrorAction SilentlyContinue | Where-Object { ($_.Attributes -band [IO.FileAttributes]::ReparsePoint) -eq 0 })
        if ($TrustedCandidates.Count -ne 1) { throw "trusted host dependency artifact identity mismatch in ${ArchivePath}: $FileName" }
        $TrustedInstalledHash = Get-Sha256 -Path $TrustedCandidates[0].FullName
        $ActualInstalledHash = Get-Sha256 -Path $SourcePath
        if ($ActualInstalledHash -cne $TrustedInstalledHash) { throw "host dependency artifact differs from trusted archive: $SourcePath" }
        if ($MarkerInstalledHash -cne $ActualInstalledHash -or $MarkerInstalledHash -cne $TrustedInstalledHash) { throw "host dependency marker installed checksum mismatch: $($Markers[0].FullName)" }
        return $SourcePath
    }
    finally {
        Remove-SafeTemporaryDirectory -State $VerifyState
    }
}

# Assert-NativeArtifact calls the standalone validator so packaging never trusts a filename-only native library.
# Assert-NativeArtifact 调用独立校验器，确保打包不会只凭文件名信任原生库。
function Assert-NativeArtifact {
    param([Parameter(Mandatory=$true)] [hashtable]$Artifact)
    if (-not (Test-Path -LiteralPath $NativeValidatorScript -PathType Leaf)) { throw "missing native artifact validator: $NativeValidatorScript" }
    & $NativeValidatorScript -ManifestPath $Artifact.ManifestPath -LibraryPath $Artifact.LibraryPath -Target $Artifact.Target -ExpectedSourceDigest $Artifact.SourceDigest -ExpectedEngineVersion $NativeEngineVersion | Out-Null
    if (-not $?) { throw "native artifact validation failed: $($Artifact.ManifestPath)" }
}

# Resolve-NativeArtifact follows current.json exactly, then validates source digest, target, ABI, engine, and hash.
# Resolve-NativeArtifact 严格读取 current.json，再校验 source digest、target、ABI、engine 与文件哈希。
function Resolve-NativeArtifact {
    $Target = Get-NativeTarget
    $TargetRoot = Join-Path $NativeDepsRoot $Target
    if (-not (Test-Path -LiteralPath $TargetRoot -PathType Container)) { throw "missing native dependency target directory: $TargetRoot" }
    $TargetRootItem = Get-Item -LiteralPath $TargetRoot -Force
    if ($TargetRootItem.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "native dependency target directory must not be a reparse point: $TargetRoot" }
    $CurrentPath = Join-Path $TargetRoot "current.json"
    if (-not (Test-Path -LiteralPath $CurrentPath -PathType Leaf)) { throw "missing native dependency selection: $CurrentPath. Run '.\make.ps1 deps native' first." }
    if ((Get-Item -LiteralPath $CurrentPath -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "native current.json must not be a reparse point: $CurrentPath" }
    $Current = Get-Content -LiteralPath $CurrentPath -Raw | ConvertFrom-Json
    if ([int]$Current.schema_version -ne 1 -or [string]$Current.target -ne $Target) { throw "native current.json schema or target mismatch: $CurrentPath" }
    $SourceDigest = [string]$Current.source_digest
    if ($SourceDigest -notmatch '^[0-9a-fA-F]{64}$') { throw "native current.json source_digest is invalid: $CurrentPath" }
    $ManifestRelative = [string]$Current.manifest_path
    if ($ManifestRelative -ne "$SourceDigest/manifest.json" -or [IO.Path]::IsPathRooted($ManifestRelative) -or $ManifestRelative.Contains("..")) { throw "native current.json manifest_path is invalid: $CurrentPath" }
    $ManifestPath = Join-Path $TargetRoot ($ManifestRelative.Replace("/", [IO.Path]::DirectorySeparatorChar))
    $ExpectedManifestPath = Join-Path (Join-Path $TargetRoot $SourceDigest) "manifest.json"
    if ([IO.Path]::GetFullPath($ManifestPath) -ne [IO.Path]::GetFullPath($ExpectedManifestPath)) { throw "native current.json does not point to the source digest directory" }
    $ManifestDirectory = Split-Path $ManifestPath -Parent
    if (-not (Test-Path -LiteralPath $ManifestDirectory -PathType Container)) { throw "missing native dependency cache directory: $ManifestDirectory" }
    $ManifestDirectoryItem = Get-Item -LiteralPath $ManifestDirectory -Force
    if ($ManifestDirectoryItem.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "native dependency cache directory must not be a reparse point: $ManifestDirectory" }
    if (-not (Test-Path -LiteralPath $ManifestPath -PathType Leaf)) { throw "missing native dependency manifest: $ManifestPath" }
    if ((Get-Item -LiteralPath $ManifestPath -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "native manifest must not be a reparse point: $ManifestPath" }
    $Manifest = Get-Content -LiteralPath $ManifestPath -Raw | ConvertFrom-Json
    $LibraryName = [string]$Manifest.library_file
    if ([string]$Current.library_file -ne $LibraryName) { throw "native current.json library_file does not match manifest" }
    $LibraryPath = Join-Path (Split-Path $ManifestPath -Parent) $LibraryName
    $Artifact = @{ Target = $Target; SourceDigest = $SourceDigest.ToLowerInvariant(); ManifestPath = $ManifestPath; LibraryPath = $LibraryPath }
    Assert-NativeArtifact -Artifact $Artifact
    return $Artifact
}

# Get-ConfigOwnership reads only the build-owned output list and rejects unsafe relative entries.
# Get-ConfigOwnership 只读取构建拥有的 output 清单，并拒绝不安全的相对路径。
function Get-ConfigOwnership {
    if (-not (Test-Path -LiteralPath $ConfigOwnershipFile -PathType Leaf)) { return @() }
    Assert-SafeOutputPath -Path $ConfigOwnershipFile
    $Entries = @()
    foreach ($Line in (Get-Content -LiteralPath $ConfigOwnershipFile)) {
        $Entry = ([string]$Line).Trim()
        if ([string]::IsNullOrWhiteSpace($Entry)) { continue }
        if ($Entry -notmatch '^configs/[^/].*$' -or $Entry.Contains("..") -or [IO.Path]::IsPathRooted($Entry)) { throw "invalid config ownership entry: $Entry" }
        $Candidate = Join-Path $OutputDir ($Entry.Replace("/", [IO.Path]::DirectorySeparatorChar))
        Assert-SafeOutputPath -Path $Candidate -AllowMissing
        $Entries += $Entry
    }
    return @($Entries | Sort-Object -Unique)
}

# Save-ConfigOwnership atomically records the exact config files that this build may replace or clean.
# Save-ConfigOwnership 原子记录本次构建可以替换或清理的精确配置文件清单。
function Save-ConfigOwnership {
    param([string[]]$Entries)
    Assert-SafeOutputPath -Path $ConfigOwnershipFile -AllowMissing
    $TempPath = "$ConfigOwnershipFile.tmp.$PID"
    Assert-SafeOutputPath -Path $TempPath -AllowMissing
    Set-Content -LiteralPath $TempPath -Value (@($Entries | Sort-Object -Unique)) -Encoding UTF8
    Move-Item -LiteralPath $TempPath -Destination $ConfigOwnershipFile -Force
}

# Sync-Configs copies source files one by one and refuses to overwrite an unowned output file.
# Sync-Configs 逐个复制源文件，并拒绝覆盖未声明拥有的 output 文件。
function Sync-Configs {
    Write-Host "=> Syncing configs (owned files only)..." -ForegroundColor Gray
    Assert-SafeOutputDirectory
    $SourceConfig = Join-Path $RootDir "configs"
    if (-not (Test-Path -LiteralPath $SourceConfig -PathType Container)) { throw "missing config source directory: $SourceConfig" }
    if (Test-Path -LiteralPath $ConfigDir) { Assert-SafeOutputPath -Path $ConfigDir } else { New-Item -ItemType Directory -Path $ConfigDir -Force | Out-Null }
    $PreviousEntries = @(Get-ConfigOwnership)
    $PreviousSet = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)
    foreach ($Entry in $PreviousEntries) { [void]$PreviousSet.Add($Entry) }
    $NewEntries = New-Object System.Collections.Generic.List[string]
    $SourceRootFull = ([IO.Path]::GetFullPath($SourceConfig)).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    foreach ($SourceFile in (Get-ChildItem -LiteralPath $SourceConfig -Recurse -File)) {
        $Relative = $SourceFile.FullName.Substring($SourceRootFull.Length).Replace("\", "/")
        $Entry = "configs/$Relative"
        $Destination = Join-Path $ConfigDir ($Relative.Replace("/", [IO.Path]::DirectorySeparatorChar))
        $DestinationParent = Split-Path $Destination -Parent
        Assert-SafeOutputPath -Path $DestinationParent -AllowMissing
        if (-not (Test-Path -LiteralPath $DestinationParent)) { New-Item -ItemType Directory -Path $DestinationParent -Force | Out-Null }
        Assert-SafeOutputPath -Path $Destination -AllowMissing
        if (Test-Path -LiteralPath $Destination -PathType Container) { throw "refusing to overwrite a directory with a config file: $Destination" }
        if (Test-Path -LiteralPath $Destination -PathType Leaf) {
            if (-not $PreviousSet.Contains($Entry)) {
                $SourceHash = Get-Sha256 -Path $SourceFile.FullName
                $DestinationHash = Get-Sha256 -Path $Destination
                if ($SourceHash -ne $DestinationHash) { throw "refusing to overwrite unowned config: $Destination" }
            }
            Copy-Item -LiteralPath $SourceFile.FullName -Destination $Destination -Force
        }
        else { Copy-Item -LiteralPath $SourceFile.FullName -Destination $Destination -Force }
        [void]$NewEntries.Add($Entry)
    }
    $NewSet = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)
    foreach ($Entry in $NewEntries) { [void]$NewSet.Add($Entry) }
    foreach ($Entry in $PreviousEntries) {
        if ($NewSet.Contains($Entry)) { continue }
        $OldPath = Join-Path $OutputDir ($Entry.Replace("/", [IO.Path]::DirectorySeparatorChar))
        Assert-SafeOutputPath -Path $OldPath -AllowMissing
        if (Test-Path -LiteralPath $OldPath -PathType Leaf) { Remove-Item -LiteralPath $OldPath -Force }
    }
    Save-ConfigOwnership -Entries @($NewEntries)
}

# Remove-OwnedConfigs deletes only files listed by the build ownership marker and leaves user files and directories intact.
# Remove-OwnedConfigs 只删除构建拥有清单中的文件，保留用户文件和目录。
function Remove-OwnedConfigs {
    foreach ($Entry in @(Get-ConfigOwnership)) {
        $Path = Join-Path $OutputDir ($Entry.Replace("/", [IO.Path]::DirectorySeparatorChar))
        Assert-SafeOutputPath -Path $Path -AllowMissing
        if (Test-Path -LiteralPath $Path -PathType Leaf) { Remove-Item -LiteralPath $Path -Force }
    }
    if (Test-Path -LiteralPath $ConfigOwnershipFile -PathType Leaf) {
        Assert-SafeOutputPath -Path $ConfigOwnershipFile
        Remove-Item -LiteralPath $ConfigOwnershipFile -Force
    }
}

# Remove-OwnedArtifact removes one known build artifact after validating its exact output path.
# Remove-OwnedArtifact 在校验精确 output 路径后删除一个已知构建产物。
function Remove-OwnedArtifact {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) { return }
    Assert-SafeOutputPath -Path $Path
    $Item = Get-Item -LiteralPath $Path -Force
    if (-not $Item.PSIsContainer) { Remove-Item -LiteralPath $Path -Force }
}

# Sync-HostLibraries validates and copies exactly the selected dependency profile without recursive deletion.
# Sync-HostLibraries 校验并复制所选依赖配置，避免递归删除目录。
function Sync-HostLibraries {
    param([Parameter(Mandatory=$true)] [ValidateSet("legacy", "native", "all")] [string]$StorageProfile)
    Write-Host "=> Syncing libraries ($StorageProfile)..." -ForegroundColor Gray
    Assert-SafeOutputDirectory
    if (Test-Path -LiteralPath $LibDir) { Assert-SafeOutputPath -Path $LibDir } else { New-Item -ItemType Directory -Path $LibDir -Force | Out-Null }
    $LegacyNames = @(Get-VldbLibraryNames)
    $NativeName = Get-NativeLibraryName
    $KnownNames = @($LegacyNames + $NativeName + "manifest.json" + $NativeSupportArtifactPaths)
    $ExpectedNames = New-Object System.Collections.Generic.List[string]
    $NativeArtifact = $null
    if ($StorageProfile -in @("legacy", "all")) {
        [void]$ExpectedNames.Add($LegacyNames[0]); [void]$ExpectedNames.Add($LegacyNames[1])
        foreach ($Index in 0..($LegacyNames.Count - 1)) {
            $Kind = if ($Index -eq 0) { "sqlite" } else { "lancedb" }
            $Source = Get-VerifiedHostDependencyPath -Kind $Kind -FileName $LegacyNames[$Index]
            $Destination = Join-Path $LibDir $LegacyNames[$Index]
            Assert-SafeOutputPath -Path $Destination -AllowMissing
            if (Test-Path -LiteralPath $Destination -PathType Container) { throw "refusing to overwrite a library directory: $Destination" }
            Copy-Item -LiteralPath $Source -Destination $Destination -Force
        }
    }
    if ($StorageProfile -in @("native", "all")) {
        $NativeArtifact = Resolve-NativeArtifact
        [void]$ExpectedNames.Add($NativeName); [void]$ExpectedNames.Add("manifest.json")
        $NativeDestination = Join-Path $LibDir $NativeName
        $ManifestDestination = Join-Path $LibDir "manifest.json"
        Assert-SafeOutputPath -Path $NativeDestination -AllowMissing
        Assert-SafeOutputPath -Path $ManifestDestination -AllowMissing
        if (Test-Path -LiteralPath $NativeDestination -PathType Container) { throw "refusing to overwrite a native library directory: $NativeDestination" }
        if (Test-Path -LiteralPath $ManifestDestination -PathType Container) { throw "refusing to overwrite a native manifest directory: $ManifestDestination" }
        Copy-Item -LiteralPath $NativeArtifact.LibraryPath -Destination $NativeDestination -Force
        Copy-Item -LiteralPath $NativeArtifact.ManifestPath -Destination $ManifestDestination -Force
        foreach ($ArtifactPath in $NativeSupportArtifactPaths) {
            $SourceSupportPath = Join-Path (Split-Path $NativeArtifact.ManifestPath -Parent) ($ArtifactPath.Replace("/", [IO.Path]::DirectorySeparatorChar))
            if (-not (Test-Path -LiteralPath $SourceSupportPath -PathType Leaf)) { throw "native support artifact is missing: $SourceSupportPath" }
            $DestinationSupportPath = Join-Path $LibDir ($ArtifactPath.Replace("/", [IO.Path]::DirectorySeparatorChar))
            $DestinationSupportParent = Split-Path $DestinationSupportPath -Parent
            Assert-SafeOutputPath -Path $DestinationSupportParent -AllowMissing
            if (-not (Test-Path -LiteralPath $DestinationSupportParent)) { New-Item -ItemType Directory -Path $DestinationSupportParent -Force | Out-Null }
            Assert-SafeOutputPath -Path $DestinationSupportPath -AllowMissing
            if (Test-Path -LiteralPath $DestinationSupportPath -PathType Container) { throw "refusing to overwrite a native support directory: $DestinationSupportPath" }
            Copy-Item -LiteralPath $SourceSupportPath -Destination $DestinationSupportPath -Force
            [void]$ExpectedNames.Add($ArtifactPath)
        }
        Assert-NativeArtifact -Artifact @{ Target = $NativeArtifact.Target; SourceDigest = $NativeArtifact.SourceDigest; ManifestPath = $ManifestDestination; LibraryPath = $NativeDestination }
    }
    foreach ($KnownName in $KnownNames) {
        if ($ExpectedNames -notcontains $KnownName) {
            Remove-OwnedArtifact -Path (Join-Path $LibDir ($KnownName.Replace("/", [IO.Path]::DirectorySeparatorChar)))
        }
    }
    return $NativeArtifact
}

# Sync-ControllerBinary copies the verified legacy controller only for profiles that declare it.
# Sync-ControllerBinary 仅在声明 legacy 依赖的配置中复制已校验的 controller。
function Sync-ControllerBinary {
    $BinaryName = Get-ControllerBinaryName
    if (Test-Path -LiteralPath $BinDir) { Assert-SafeOutputPath -Path $BinDir } else { New-Item -ItemType Directory -Path $BinDir -Force | Out-Null }
    $Source = Get-VerifiedHostDependencyPath -Kind controller -FileName $BinaryName
    $Destination = Join-Path $BinDir $BinaryName
    Assert-SafeOutputPath -Path $Destination -AllowMissing
    if (Test-Path -LiteralPath $Destination -PathType Container) { throw "refusing to overwrite a controller directory: $Destination" }
    Copy-Item -LiteralPath $Source -Destination $Destination -Force
}

# Ensure-DatabaseLayout creates missing database directories but never removes or rewrites user data.
# Ensure-DatabaseLayout 只创建缺失的数据库目录，绝不删除或改写用户数据。
function Ensure-DatabaseLayout {
    Assert-SafeOutputDirectory
    if (Test-Path -LiteralPath $DatabaseDir) { Assert-SafeOutputPath -Path $DatabaseDir } else { New-Item -ItemType Directory -Path $DatabaseDir -Force | Out-Null }
    $LanceDBDir = Join-Path $DatabaseDir "lancedb"
    if (Test-Path -LiteralPath $LanceDBDir) { Assert-SafeOutputPath -Path $LanceDBDir } else { New-Item -ItemType Directory -Path $LanceDBDir -Force | Out-Null }
}

# Invoke-GoBuild builds one Go package with an explicit argument vector so release is observable in compiler flags.
# Invoke-GoBuild 使用显式参数向量构建一个 Go 包，确保 release 配置真正传递给编译器。
function Invoke-GoBuild {
    param(
        [Parameter(Mandatory=$true)] [string]$OutputPath,
        [Parameter(Mandatory=$true)] [string]$PackagePath,
        [string]$Ldflags = "",
        [switch]$TrimPath
    )
    # Validate the executable destination before the compiler can write through a redirected output path.
    # 在编译器写入之前校验可执行文件目标，避免通过重定向的 output 路径写到其他目录。
    Assert-SafeOutputPath -Path $OutputPath -AllowMissing
    if (Test-Path -LiteralPath $OutputPath -PathType Container) { throw "refusing to build an executable over a directory: $OutputPath" }
    $GoExe = Resolve-GoExe
    $BuildArguments = New-Object System.Collections.Generic.List[string]
    [void]$BuildArguments.Add("build")
    if ($TrimPath) { [void]$BuildArguments.Add("-trimpath") }
    if (-not [string]::IsNullOrWhiteSpace($Ldflags)) { [void]$BuildArguments.Add("-ldflags"); [void]$BuildArguments.Add($Ldflags) }
    [void]$BuildArguments.Add("-o"); [void]$BuildArguments.Add($OutputPath); [void]$BuildArguments.Add($PackagePath)
    & $GoExe @($BuildArguments.ToArray())
    if ($LASTEXITCODE -ne 0) { throw "go build failed for $PackagePath ($LASTEXITCODE)" }
    if (-not (Test-Path -LiteralPath $OutputPath -PathType Leaf)) { throw "expected build artifact was not produced: $OutputPath" }
}

# Assert-StorageDependencies performs a read-only preflight before Go compilation can leave a partial package.
# Assert-StorageDependencies 在 Go 编译前只读预检依赖，避免产生半成品交付包。
function Assert-StorageDependencies {
    param([Parameter(Mandatory=$true)] [ValidateSet("legacy", "native", "all")] [string]$StorageProfile)
    if ($StorageProfile -in @("legacy", "all")) {
        $LegacyNames = @(Get-VldbLibraryNames)
        [void](Get-VerifiedHostDependencyPath -Kind "sqlite" -FileName $LegacyNames[0])
        [void](Get-VerifiedHostDependencyPath -Kind "lancedb" -FileName $LegacyNames[1])
        [void](Get-VerifiedHostDependencyPath -Kind "controller" -FileName (Get-ControllerBinaryName))
    }
    if ($StorageProfile -in @("native", "all")) { [void](Resolve-NativeArtifact) }
}

# Do-Build creates the formal package while keeping storage selection separate from runtime configuration.
# Do-Build 构建正式交付目录，并保持存储选择与运行时配置彼此独立。
function Do-Build {
    param([Parameter(Mandatory=$true)] [ValidateSet("standard", "release")] [string]$BuildProfile)
    $StorageProfile = Resolve-StorageProfile
    Assert-StorageDependencies -StorageProfile $StorageProfile
    Resolve-SourceIdentity
    # Read the one release version authority before injecting executable metadata.
    # 在注入可执行文件元数据之前读取唯一发行版本来源。
    $ReleaseVersion = [IO.File]::ReadAllText((Join-Path $RootDir "VERSION")).Trim()
    if ($ReleaseVersion -notmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$') { throw "invalid VERSION: $ReleaseVersion" }
    $BuildLdflags = "-X github.com/openvulcan/vmm/internal/buildinfo.Version=$ReleaseVersion -X github.com/openvulcan/vmm/internal/buildinfo.SourceRevision=$SourceRevision -X github.com/openvulcan/vmm/internal/buildinfo.SourceStateDigest=$SourceStateDigest"
    $UseTrimPath = $false
    if ($BuildProfile -eq "release") { $BuildLdflags = "$BuildLdflags -s -w"; $UseTrimPath = $true }
    Write-Host "=> Building VMM Gateway ($BuildProfile, storage=$StorageProfile)..." -ForegroundColor Cyan
    Assert-SafeOutputPath -Path $BinDir -AllowMissing
    if (-not (Test-Path -LiteralPath $BinDir)) { New-Item -ItemType Directory -Path $BinDir -Force | Out-Null }
    Invoke-GoBuild -OutputPath $ExePath -PackagePath (Join-Path $RootDir "cmd\vmm-local") -Ldflags $BuildLdflags -TrimPath:$UseTrimPath
    Invoke-GoBuild -OutputPath $MigrateExePath -PackagePath (Join-Path $RootDir "cmd\vmm-migrate") -Ldflags $BuildLdflags -TrimPath:$UseTrimPath
    $TesterFlags = if ($BuildProfile -eq "release") { "-s -w" } else { "" }
    Invoke-GoBuild -OutputPath $TesterExePath -PackagePath (Join-Path $RootDir "cmd\vmm-pii-tester") -Ldflags $TesterFlags -TrimPath:$UseTrimPath
    Sync-Configs
    [void](Sync-HostLibraries -StorageProfile $StorageProfile)
    if ($StorageProfile -in @("legacy", "all")) { Sync-ControllerBinary } else { Remove-OwnedArtifact -Path (Join-Path $BinDir (Get-ControllerBinaryName)) }
    Ensure-DatabaseLayout
    Write-Host "=> Build Success!" -ForegroundColor Green
}

# Do-BuildTester builds only the PII tester but applies the same dependency and output ownership rules.
# Do-BuildTester 仅构建 PII 测试器，但复用相同的依赖和 output 所有权规则。
function Do-BuildTester {
    $StorageProfile = Resolve-StorageProfile
    Assert-StorageDependencies -StorageProfile $StorageProfile
    Write-Host "=> Building VMM PII Tester (storage=$StorageProfile)..." -ForegroundColor Cyan
    Assert-SafeOutputPath -Path $BinDir -AllowMissing
    if (-not (Test-Path -LiteralPath $BinDir)) { New-Item -ItemType Directory -Path $BinDir -Force | Out-Null }
    Invoke-GoBuild -OutputPath $TesterExePath -PackagePath (Join-Path $RootDir "cmd\vmm-pii-tester")
    Sync-Configs
    [void](Sync-HostLibraries -StorageProfile $StorageProfile)
    if ($StorageProfile -in @("legacy", "all")) { Sync-ControllerBinary } else { Remove-OwnedArtifact -Path (Join-Path $BinDir (Get-ControllerBinaryName)) }
    Ensure-DatabaseLayout
    Write-Host "=> Tester Build Success!" -ForegroundColor Green
}

# Do-Clean removes only known VMM build artifacts and preserves output/database plus unknown user files.
# Do-Clean 只删除已知 VMM 构建产物，保留 output/database 和未知用户文件。
function Do-Clean {
    if (-not (Test-Path -LiteralPath $OutputDir)) { return }
    Assert-SafeOutputDirectory
    Write-Host "=> Cleaning owned build artifacts..." -ForegroundColor Gray
    Remove-OwnedConfigs
    $KnownPaths = @(
        $ExePath,
        $MigrateExePath,
        $TesterExePath,
        (Join-Path $BinDir (Get-ControllerBinaryName)),
        (Join-Path $LibDir "vldb_sqlite.dll"),
        (Join-Path $LibDir "vldb_lancedb.dll"),
        (Join-Path $LibDir "libvldb_sqlite.so"),
        (Join-Path $LibDir "libvldb_lancedb.so"),
        (Join-Path $LibDir "libvldb_sqlite.dylib"),
        (Join-Path $LibDir "libvldb_lancedb.dylib"),
        (Join-Path $LibDir "vmm_lancedb_native.dll"),
        (Join-Path $LibDir "libvmm_lancedb_native.so"),
        (Join-Path $LibDir "libvmm_lancedb_native.dylib"),
        (Join-Path $LibDir "manifest.json")
    )
    foreach ($ArtifactPath in $NativeSupportArtifactPaths) {
        $KnownPaths += Join-Path $LibDir ($ArtifactPath.Replace("/", [IO.Path]::DirectorySeparatorChar))
    }
    foreach ($Path in $KnownPaths) { Remove-OwnedArtifact -Path $Path }
}

# Do-Run starts the packaged binary from output/bin so relative configuration and library lookup stay deterministic.
# Do-Run 从 output/bin 启动打包二进制，确保相对配置和动态库查找保持确定性。
function Do-Run {
    param([string[]]$ForwardArgs = @())
    if (-not (Test-Path -LiteralPath $ExePath -PathType Leaf)) { Do-Build -BuildProfile "standard" }
    Write-Host "=> Running VMM..." -ForegroundColor Magenta
    Push-Location $BinDir
    $ExitCode = 0
    try { & ".\vmm-local.exe" @ForwardArgs; $ExitCode = $LASTEXITCODE }
    finally { Pop-Location }
    if ($ExitCode -ne 0) { exit $ExitCode }
}

$BuildProfile = if ($Action -eq "build") { Resolve-BuildProfile -Arguments $ProgramArgs } else { "standard" }
switch ($Action) {
    "clean" { Do-Clean }
    "build" { Do-Build -BuildProfile $BuildProfile }
    "run" { Do-Run -ForwardArgs $ProgramArgs }
    "tester" { Do-BuildTester }
}
