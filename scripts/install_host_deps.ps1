# install_host_deps.ps1 installs host-side dynamic libraries into third_party/deps so the packaged runtime can later copy them into output/libs.
# install_host_deps.ps1 用于把宿主侧动态库安装到 third_party/deps，供后续打包流程复制到 output/libs。

$ErrorActionPreference = "Stop"
$ProjectDir = Split-Path $PSScriptRoot -Parent
Set-Location $ProjectDir

# Use RuntimeInformation for platform detection so Windows PowerShell and PowerShell 7 behave consistently.
# 使用 RuntimeInformation 做平台探测，确保 Windows PowerShell 与 PowerShell 7 行为一致。
$script:IsWindowsPlatform = [System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::Windows)
$script:IsMacOSPlatform = [System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::OSX)
$script:IsLinuxPlatform = [System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::Linux)

# ThirdPartyDir stores downloaded archives and extracted libraries under one ignored workspace root.
# ThirdPartyDir 用于在一个被忽略的工作区根目录下保存下载压缩包与解压后的动态库。
$ThirdPartyDir = Join-Path $ProjectDir "third_party"
$DepsDir = Join-Path $ThirdPartyDir "deps"
$LanceDBDir = Join-Path $ThirdPartyDir "vldb_lancedb"
$SQLiteDir = Join-Path $ThirdPartyDir "vldb_sqlite"
$LanceDBRepo = "OpenVulcan/vldb-lancedb"
$SQLiteRepo = "OpenVulcan/vldb-sqlite"

function Ensure-Dir {
    param([string]$Path)

    if (-not (Test-Path -LiteralPath $Path)) {
        New-Item -ItemType Directory -Path $Path -Force | Out-Null
    }
}

function Find-LocalArchive {
    param([string]$AssetName)

    $Candidates = @((Join-Path $ThirdPartyDir $AssetName))
    foreach ($Dir in (Get-ChildItem -Path $ThirdPartyDir -Directory -ErrorAction SilentlyContinue)) {
        $Candidates += Join-Path $Dir.FullName $AssetName
    }
    foreach ($Candidate in $Candidates) {
        if (Test-Path -LiteralPath $Candidate) {
            return $Candidate
        }
    }
    return $null
}

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

function Get-CurrentArchitectureKey {
    $Arch = [System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture.ToString().ToLowerInvariant()
    switch ($Arch) {
        "x64" { return "x86_64" }
        "arm64" { return "aarch64" }
        default { throw "unsupported architecture for host dependency bootstrap: $Arch" }
    }
}

function Get-LatestRepoTag {
    param(
        [string]$Repo,
        [string]$DisplayName
    )

    $ApiURL = "https://api.github.com/repos/$Repo/tags?per_page=1"
    Write-Host "==> Querying latest $DisplayName tag..."
    $Tags = Invoke-RestMethod -Uri $ApiURL -UseBasicParsing
    if (-not $Tags) {
        throw "latest $DisplayName tag lookup returned no results / 最新 $DisplayName tag 查询结果为空"
    }
    $FirstTag = @($Tags)[0]
    if (-not $FirstTag.name) {
        throw "latest $DisplayName tag is missing name / 最新 $DisplayName tag 缺少 name 字段"
    }
    return $FirstTag.name
}

function Get-ReleaseByTagOrNull {
    param(
        [string]$Repo,
        [string]$TagName
    )

    $ApiURL = "https://api.github.com/repos/$Repo/releases/tags/$TagName"
    try {
        return Invoke-RestMethod -Uri $ApiURL -UseBasicParsing
    } catch {
        $StatusCode = $null
        if ($_.Exception.Response -and $_.Exception.Response.StatusCode) {
            try {
                $StatusCode = [int]$_.Exception.Response.StatusCode
            } catch {
                $StatusCode = $null
            }
        }
        if ($StatusCode -eq 404) {
            return $null
        }
        throw
    }
}

function Get-VldbAssetInfo {
    param([ValidateSet("sqlite", "lancedb")] [string]$Kind)

    $ArchKey = Get-CurrentArchitectureKey
    if ($script:IsWindowsPlatform) {
        if ($ArchKey -ne "x86_64") {
            throw "$Kind prebuilt library currently supports Windows x86_64 only. Current arch: $ArchKey"
        }
        return @{
            target = "x86_64-pc-windows-msvc"
            archive_ext = ".zip"
            library_name = if ($Kind -eq "sqlite") { "vldb_sqlite.dll" } else { "vldb_lancedb.dll" }
        }
    }
    if ($script:IsLinuxPlatform) {
        return @{
            target = if ($ArchKey -eq "aarch64") { "aarch64-unknown-linux-gnu" } else { "x86_64-unknown-linux-gnu" }
            archive_ext = ".tar.gz"
            library_name = if ($Kind -eq "sqlite") { "libvldb_sqlite.so" } else { "libvldb_lancedb.so" }
        }
    }
    if ($script:IsMacOSPlatform) {
        return @{
            target = if ($ArchKey -eq "aarch64") { "aarch64-apple-darwin" } else { "x86_64-apple-darwin" }
            archive_ext = ".tar.gz"
            library_name = if ($Kind -eq "sqlite") { "libvldb_sqlite.dylib" } else { "libvldb_lancedb.dylib" }
        }
    }
    throw "unsupported platform for host dependency bootstrap"
}

function Install-VldbLibrary {
    param([ValidateSet("sqlite", "lancedb")] [string]$Kind)

    Ensure-Dir $ThirdPartyDir
    Ensure-Dir $DepsDir

    $Info = Get-VldbAssetInfo -Kind $Kind
    $TarPath = Get-AvailableTarPath
    $TargetDir = if ($Kind -eq "sqlite") { $SQLiteDir } else { $LanceDBDir }
    $Repo = if ($Kind -eq "sqlite") { $SQLiteRepo } else { $LanceDBRepo }
    $RepoPrefix = if ($Kind -eq "sqlite") { "vldb-sqlite" } else { "vldb-lancedb" }

    Ensure-Dir $TargetDir

    $LocalPattern = "$RepoPrefix-lib-v*-$($Info.target)$($Info.archive_ext)"
    $LocalArchive = Get-ChildItem -Path $ThirdPartyDir -File -Filter $LocalPattern -ErrorAction SilentlyContinue |
        Sort-Object Name -Descending |
        Select-Object -First 1
    if (-not $LocalArchive) {
        $LocalArchive = Get-ChildItem -Path $ThirdPartyDir -Directory -ErrorAction SilentlyContinue |
            ForEach-Object { Get-ChildItem -Path $_.FullName -File -Filter $LocalPattern -ErrorAction SilentlyContinue } |
            Sort-Object Name -Descending |
            Select-Object -First 1
    }

    $TagName = $null
    $AssetName = $null
    $LocalArchivePath = $null
    if ($LocalArchive) {
        $AssetName = $LocalArchive.Name
        if ($AssetName -match "^$RepoPrefix-lib-(v.+)-[^-]+(?:-[^-]+){2,3}(\\.zip|\\.tar\\.gz)$") {
            $TagName = $Matches[1]
        } else {
            throw "unable to parse $Kind tag from local archive name: $AssetName"
        }
        $LocalArchivePath = $LocalArchive.FullName
    } else {
        $TagName = Get-LatestRepoTag -Repo $Repo -DisplayName $RepoPrefix
        $AssetName = "$RepoPrefix-lib-$TagName-$($Info.target)$($Info.archive_ext)"
        $LocalArchivePath = Find-LocalArchive -AssetName $AssetName
    }

    $MarkerFile = Join-Path $TargetDir ".installed-$TagName-$($Info.target)"
    $LibraryDest = Join-Path $DepsDir $Info.library_name
    if ((Test-Path -LiteralPath $MarkerFile) -and (Test-Path -LiteralPath $LibraryDest)) {
        Write-Host "==> $RepoPrefix library already installed ($AssetName)."
        return
    }

    $Release = $null
    if (-not $LocalArchivePath) {
        $Release = Get-ReleaseByTagOrNull -Repo $Repo -TagName $TagName
        if (-not $Release) {
            throw "$RepoPrefix tag '$TagName' currently has no GitHub Release library asset. Please download '$AssetName' manually into third_party before rerunning / 当前 tag 未发布 GitHub Release 库资产，请先手动下载 '$AssetName' 到 third_party 后重试。"
        }
        $Asset = $Release.assets | Where-Object { $_.name -eq $AssetName } | Select-Object -First 1
        if (-not $Asset) {
            $Available = ($Release.assets | ForEach-Object { $_.name }) -join ", "
            throw "$RepoPrefix asset '$AssetName' not found in release '$TagName'. Available assets: $Available / 目标 tag 对应 Release 中未找到该库资产。"
        }
    }

    $TempDir = Join-Path $env:TEMP ("{0}_{1}" -f $RepoPrefix.Replace("-", "_"), $PID)
    if (Test-Path -LiteralPath $TempDir) {
        Remove-Item -Path $TempDir -Recurse -Force -ErrorAction SilentlyContinue
    }
    Ensure-Dir $TempDir

    try {
        $ArchivePath = Join-Path $TempDir $AssetName
        if ($LocalArchivePath) {
            Write-Host "==> Using local $RepoPrefix library package: $LocalArchivePath"
            Copy-Item -Path $LocalArchivePath -Destination $ArchivePath -Force
        } else {
            Write-Host "==> Downloading $RepoPrefix library package: $AssetName"
            Invoke-WebRequest -Uri $Asset.browser_download_url -OutFile $ArchivePath -UseBasicParsing
        }

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
        Copy-Item -Path $LibrarySource.FullName -Destination $LibraryDest -Force

        Get-ChildItem -Path $TargetDir -Filter ".installed-*" -File -ErrorAction SilentlyContinue |
            Remove-Item -Force -ErrorAction SilentlyContinue
        New-Item -ItemType File -Path $MarkerFile -Force | Out-Null
        Write-Host "==> $RepoPrefix library installed successfully."
    } finally {
        Remove-Item -Path $TempDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

Install-VldbLibrary -Kind "sqlite"
Install-VldbLibrary -Kind "lancedb"
