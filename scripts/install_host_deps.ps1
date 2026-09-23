# install_host_deps.ps1 installs host-side dynamic libraries into third_party/deps so the packaged runtime can later copy them into output/libs.
# install_host_deps.ps1 用于把宿主侧动态库安装到 third_party/deps，供后续打包流程复制到 output/libs。

$ErrorActionPreference = "Stop"
$ProjectDir = Split-Path $PSScriptRoot -Parent
Set-Location $ProjectDir

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

# Use RuntimeInformation for platform detection so Windows PowerShell and PowerShell 7 behave consistently.
# 使用 RuntimeInformation 做平台探测，确保 Windows PowerShell 与 PowerShell 7 行为一致。
$script:IsWindowsPlatform = [System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::Windows)
$script:IsMacOSPlatform = [System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::OSX)
$script:IsLinuxPlatform = [System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::Linux)

# ThirdPartyDir stores downloaded archives and extracted libraries under one ignored workspace root.
# ThirdPartyDir 用于在一个被忽略的工作区根目录下保存下载压缩包与解压后的动态库。
$ThirdPartyDir = Join-Path $ProjectDir "third_party"
$DepsDir = Join-Path $ThirdPartyDir "deps"
$ChecksumManifestPath = Join-Path $PSScriptRoot "host_deps_sha256.tsv"
$LanceDBDir = Join-Path $ThirdPartyDir "vldb_lancedb"
$SQLiteDir = Join-Path $ThirdPartyDir "vldb_sqlite"
$ControllerDir = Join-Path $ThirdPartyDir "vldb_controller"
$LanceDBRepo = "OpenVulcan/vldb-lancedb"
$SQLiteRepo = "OpenVulcan/vldb-sqlite"
$ControllerRepo = "OpenVulcan/vldb-controller"
# Pinned dependency tags keep direct split mode aligned with the versions embedded by vldb-controller.
# 固定依赖标签用于确保直接 split 模式与 vldb-controller 内嵌的版本保持一致。
$LanceDBTag = "v0.1.5"
$SQLiteTag = "v0.1.6"
$ControllerTag = "v0.2.3"

# Read-ChecksumManifest loads and validates every checked-in archive digest before download or cache reuse.
# Read-ChecksumManifest 在下载或使用缓存前加载并校验仓库内全部压缩包摘要。
function Read-ChecksumManifest {
    param([Parameter(Mandatory=$true)] [string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "missing host dependency checksum manifest: $Path / 缺少宿主依赖 SHA256 固定清单。"
    }
    $ManifestItem = Get-Item -LiteralPath $Path -Force
    if ($ManifestItem.Attributes -band [IO.FileAttributes]::ReparsePoint) {
        throw "host dependency checksum manifest must not be a reparse point: $Path / 宿主依赖 SHA256 清单不能是重解析点。"
    }

    $Rows = @()
    $LineNumber = 0
    foreach ($Line in (Get-Content -LiteralPath $Path -Encoding UTF8)) {
        $LineNumber++
        if ([string]::IsNullOrWhiteSpace($Line) -or $Line.TrimStart().StartsWith("#")) {
            continue
        }
        $Parts = $Line -split "`t"
        if ($Parts.Count -ne 4 -or ($Parts | Where-Object { [string]::IsNullOrWhiteSpace($_) }).Count -gt 0) {
            throw "invalid host dependency checksum manifest row $LineNumber / 宿主依赖 SHA256 清单行格式错误。"
        }
        $Hash = $Parts[3].Trim().ToLowerInvariant()
        if ($Hash -notmatch '^[0-9a-f]{64}$') {
            throw "invalid host dependency SHA256 at manifest row $LineNumber / 宿主依赖 SHA256 格式错误。"
        }
        $Rows += [pscustomobject]@{
            Repo = $Parts[0]
            Tag = $Parts[1]
            Asset = $Parts[2]
            Hash = $Hash
        }
    }
    if ($Rows.Count -ne 15) {
        throw "host dependency checksum manifest must contain exactly 15 records / 宿主依赖 SHA256 清单必须包含 15 条记录。"
    }
    $Seen = @{}
    foreach ($Row in $Rows) {
        $Key = "$($Row.Repo)`t$($Row.Tag)`t$($Row.Asset)"
        if ($Seen.ContainsKey($Key)) {
            throw "host dependency checksum manifest contains duplicate records / 宿主依赖 SHA256 清单包含重复记录。"
        }
        $Seen[$Key] = $true
    }
    return $Rows
}

# Get-PinnedArchiveHash resolves one exact repository, tag, and asset tuple from the checked-in digest manifest.
# Get-PinnedArchiveHash 从仓库内摘要清单解析唯一的仓库、标签和资产组合摘要。
function Get-PinnedArchiveHash {
    param(
        [Parameter(Mandatory=$true)] [string]$Repo,
        [Parameter(Mandatory=$true)] [string]$Tag,
        [Parameter(Mandatory=$true)] [string]$Asset
    )
    $Matches = @($script:PinnedHostDependencyChecksums | Where-Object { $_.Repo -ceq $Repo -and $_.Tag -ceq $Tag -and $_.Asset -ceq $Asset })
    if ($Matches.Count -ne 1) {
        throw "missing or duplicate pinned SHA256 for $Repo $Tag $Asset / 缺少或重复的固定 SHA256。"
    }
    return $Matches[0].Hash
}

$script:PinnedHostDependencyChecksums = Read-ChecksumManifest -Path $ChecksumManifestPath

# Ensure-Dir creates one dependency workspace directory when it does not already exist.
# Ensure-Dir 在依赖工作区目录尚不存在时创建该目录。
function Ensure-Dir {
    param([string]$Path)

    if (-not (Test-Path -LiteralPath $Path)) {
        New-Item -ItemType Directory -Path $Path -Force | Out-Null
    }
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

# Find-LocalArchive resolves only the exact pinned release archive from known dependency cache directories.
# Find-LocalArchive 仅从已知依赖缓存目录中解析精确匹配的固定版本 release 压缩包。
function Find-LocalArchive {
    param([string]$AssetName)

    $Candidates = @((Join-Path $ThirdPartyDir $AssetName))
    foreach ($Dir in (Get-ChildItem -Path $ThirdPartyDir -Directory -ErrorAction SilentlyContinue)) {
        $Candidates += Join-Path $Dir.FullName $AssetName
    }
    foreach ($Candidate in $Candidates) {
        $Item = Get-Item -LiteralPath $Candidate -ErrorAction SilentlyContinue
        if ($Item -and -not $Item.PSIsContainer -and (($Item.Attributes -band [IO.FileAttributes]::ReparsePoint) -eq 0)) {
            return $Item.FullName
        }
    }
    return $null
}

# Get-AvailableTarPath locates the host tar executable used to unpack release assets.
# Get-AvailableTarPath 用于定位解压 release 资产所需的宿主 tar 可执行文件。
function Get-AvailableTarPath {
    $SystemTar = "$env:SystemRoot\System32\tar.exe"
    if (Test-Path -LiteralPath $SystemTar) {
        return $SystemTar
    }
    $TarCommand = Get-Command "tar.exe" -ErrorAction SilentlyContinue
    if ($TarCommand) {
        return $TarCommand.Source
    }
    throw "tar.exe is required to extract host dependency archives / 解压宿主依赖需要 tar.exe"
}

# Get-CurrentArchitectureKey maps the current process architecture to one supported release architecture key.
# Get-CurrentArchitectureKey 把当前进程架构映射为受支持的 release 架构标识。
function Get-CurrentArchitectureKey {
    $Arch = [System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture.ToString().ToLowerInvariant()
    switch ($Arch) {
        "x64" { return "x86_64" }
        "arm64" { return "aarch64" }
        default { throw "unsupported architecture for host dependency bootstrap: $Arch" }
    }
}

# Get-VldbAssetInfo returns the exact target triple, archive format, and installed filename for one pinned component.
# Get-VldbAssetInfo 返回某个固定组件对应的精确 target triple、压缩格式与安装文件名。
function Get-VldbAssetInfo {
    param([ValidateSet("sqlite", "lancedb", "controller")] [string]$Kind)

    $ArchKey = Get-CurrentArchitectureKey
    if ($script:IsWindowsPlatform) {
        if ($ArchKey -ne "x86_64") {
            throw "$Kind prebuilt library currently supports Windows x86_64 only. Current arch: $ArchKey"
        }
        return @{
            target = "x86_64-pc-windows-msvc"
            archive_ext = ".zip"
            library_name = if ($Kind -eq "sqlite") { "vldb_sqlite.dll" } elseif ($Kind -eq "lancedb") { "vldb_lancedb.dll" } else { "vldb-controller.exe" }
        }
    }
    if ($script:IsLinuxPlatform) {
        return @{
            target = if ($ArchKey -eq "aarch64") { "aarch64-unknown-linux-gnu" } else { "x86_64-unknown-linux-gnu" }
            archive_ext = ".tar.gz"
            library_name = if ($Kind -eq "sqlite") { "libvldb_sqlite.so" } elseif ($Kind -eq "lancedb") { "libvldb_lancedb.so" } else { "vldb-controller" }
        }
    }
    if ($script:IsMacOSPlatform) {
        return @{
            target = if ($ArchKey -eq "aarch64") { "aarch64-apple-darwin" } else { "x86_64-apple-darwin" }
            archive_ext = ".tar.gz"
            library_name = if ($Kind -eq "sqlite") { "libvldb_sqlite.dylib" } elseif ($Kind -eq "lancedb") { "libvldb_lancedb.dylib" } else { "vldb-controller" }
        }
    }
    throw "unsupported platform for host dependency bootstrap"
}

# Install-VldbLibrary downloads, verifies, extracts, and records one pinned database library or controller binary.
# Install-VldbLibrary 下载、校验、解压并记录一个固定版本数据库动态库或 controller 二进制。
function Install-VldbLibrary {
    param([ValidateSet("sqlite", "lancedb", "controller")] [string]$Kind)

    Ensure-Dir $ThirdPartyDir
    Ensure-Dir $DepsDir

    $Info = Get-VldbAssetInfo -Kind $Kind
    $TarPath = Get-AvailableTarPath
    switch ($Kind) {
        "sqlite" {
            $TargetDir = $SQLiteDir
            $Repo = $SQLiteRepo
            $RepoPrefix = "vldb-sqlite"
            $TagName = $SQLiteTag
        }
        "lancedb" {
            $TargetDir = $LanceDBDir
            $Repo = $LanceDBRepo
            $RepoPrefix = "vldb-lancedb"
            $TagName = $LanceDBTag
        }
        "controller" {
            $TargetDir = $ControllerDir
            $Repo = $ControllerRepo
            $RepoPrefix = "vldb-controller"
            $TagName = $ControllerTag
        }
    }

    Ensure-Dir $TargetDir

    # Resolve only the pinned asset so an unrelated newer local archive cannot silently change runtime behavior.
    # 只解析固定版本资产，避免无关的较新本地压缩包静默改变运行时行为。
    $AssetName = if ($Kind -eq "controller") {
        "$RepoPrefix-$TagName-$($Info.target)$($Info.archive_ext)"
    } else {
        "$RepoPrefix-lib-$TagName-$($Info.target)$($Info.archive_ext)"
    }
    $ExpectedArchiveHash = Get-PinnedArchiveHash -Repo $Repo -Tag $TagName -Asset $AssetName
    $LocalArchivePath = Find-LocalArchive -AssetName $AssetName

    $MarkerFile = Join-Path $TargetDir ".installed-$TagName-$($Info.target)"
    $LibraryDest = Join-Path $DepsDir $Info.library_name

    $TempState = New-SafeTemporaryDirectory -Prefix $RepoPrefix
    $TempDir = $TempState.Path

    try {
        $ArchivePath = Join-Path $TempDir $AssetName
        if ($LocalArchivePath) {
            Write-Host "==> Using local $RepoPrefix library package: $LocalArchivePath"
            Copy-Item -Path $LocalArchivePath -Destination $ArchivePath -Force
        } else {
            Write-Host "==> Downloading $RepoPrefix library package: $AssetName"
            $DownloadURL = "https://github.com/$Repo/releases/download/$TagName/$AssetName"
            Invoke-WebRequest -Uri $DownloadURL -OutFile $ArchivePath -UseBasicParsing
        }

        # Verify the release archive before extraction so corrupted or substituted assets never reach the packaged dependency directory.
        # 在解压前校验 release 压缩包，确保损坏或被替换的资产不会进入打包依赖目录。
        $ActualArchiveHash = Get-Sha256 -Path $ArchivePath
        if (-not $ExpectedArchiveHash -or $ExpectedArchiveHash -ne $ActualArchiveHash) {
            throw "$RepoPrefix archive checksum mismatch for $AssetName / 依赖压缩包 SHA256 校验失败。"
        }

        # Retain the verified archive in the dependency cache so later runs can revalidate trusted bytes without relying on a marker.
        # 将已验证压缩包保留在依赖缓存中，使后续运行可以重新校验可信字节，而不依赖 marker。
        $ArchiveCachePath = Join-Path $TargetDir $AssetName
        $ArchiveCacheTemp = "$ArchiveCachePath.tmp.$PID"
        Copy-Item -Path $ArchivePath -Destination $ArchiveCacheTemp -Force
        Move-Item -Path $ArchiveCacheTemp -Destination $ArchiveCachePath -Force

        if ($Info.archive_ext -eq ".zip") {
            Expand-Archive -Path $ArchivePath -DestinationPath $TempDir -Force
        } else {
            & $TarPath -xzf $ArchivePath -C $TempDir
        }

        $LibrarySource = Get-ChildItem -Path $TempDir -Recurse -File -Filter $Info.library_name -ErrorAction SilentlyContinue |
            Select-Object -First 1
        if (-not $LibrarySource) {
            throw "dynamic library '$($Info.library_name)' not found after extracting $AssetName"
        }

        # Compare an existing installed file with the freshly extracted trusted artifact before allowing cache reuse.
        # 在允许复用缓存前，把已安装文件与刚从可信压缩包提取的文件进行比对。
        $TrustedInstalledHash = Get-Sha256 -Path $LibrarySource.FullName
        if (Test-Path -LiteralPath $LibraryDest -PathType Leaf) {
            $ActualInstalledHash = Get-Sha256 -Path $LibraryDest
            if ($ActualInstalledHash -eq $TrustedInstalledHash) {
                Get-ChildItem -Path $TargetDir -Filter ".installed-*" -File -ErrorAction SilentlyContinue |
                    Remove-Item -Force -ErrorAction SilentlyContinue
                Set-Content -LiteralPath $MarkerFile -Value @($ExpectedArchiveHash, $TrustedInstalledHash) -Encoding ascii
                Write-Host "==> $RepoPrefix library already installed and verified ($AssetName)."
                return
            }
        }

        Copy-Item -Path $LibrarySource.FullName -Destination $LibraryDest -Force

        Get-ChildItem -Path $TargetDir -Filter ".installed-*" -File -ErrorAction SilentlyContinue |
            Remove-Item -Force -ErrorAction SilentlyContinue
        Set-Content -LiteralPath $MarkerFile -Value @($ExpectedArchiveHash, $TrustedInstalledHash) -Encoding ascii
        Write-Host "==> $RepoPrefix library installed successfully."
    } finally {
        Remove-SafeTemporaryDirectory -State $TempState
    }
}

Install-VldbLibrary -Kind "sqlite"
Install-VldbLibrary -Kind "lancedb"
Install-VldbLibrary -Kind "controller"
