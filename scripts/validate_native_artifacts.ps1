# validate_native_artifacts.ps1 validates one prepared native LanceDB artifact and its manifest.
# validate_native_artifacts.ps1 用于校验一份已制备的原生 LanceDB 产物及其清单。
Param(
    [Parameter(Mandatory=$true)] [string]$ManifestPath,
    [string]$LibraryPath = '',
    [Parameter(Mandatory=$true)] [string]$Target,
    [string]$ExpectedSourceDigest = '',
    [string]$ExpectedCargoLockSha256 = '',
    [string]$ExpectedEngineVersion = '0.39.0'
)

$ErrorActionPreference = "Stop"

# NativeSupportArtifactPaths is the fixed support-file allowlist copied with one validated library.
# NativeSupportArtifactPaths 是与一份已校验动态库一同复制的固定支持文件白名单。
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

# Get-ExpectedLibraryName maps a Rust target family to the declared dynamic-library filename.
# Get-ExpectedLibraryName 将 Rust target 家族映射到契约声明的动态库文件名。
function Get-ExpectedLibraryName {
    param([Parameter(Mandatory=$true)] [string]$TargetName)
    switch ($TargetName) {
        "x86_64-pc-windows-msvc" { return "vmm_lancedb_native.dll" }
        "x86_64-unknown-linux-gnu" { return "libvmm_lancedb_native.so" }
        "aarch64-unknown-linux-gnu" { return "libvmm_lancedb_native.so" }
        "x86_64-apple-darwin" { return "libvmm_lancedb_native.dylib" }
        "aarch64-apple-darwin" { return "libvmm_lancedb_native.dylib" }
        default { throw "unsupported native artifact target: $TargetName" }
    }
}

# Assert-RegularSha256 verifies that a manifest digest is a complete lowercase-or-uppercase SHA-256 value.
# Assert-RegularSha256 校验清单摘要是否为完整的大小写不敏感 SHA-256 值。
function Assert-RegularSha256 {
    param([Parameter(Mandatory=$true)] [string]$Value, [Parameter(Mandatory=$true)] [string]$FieldName)
    if ($Value -notmatch '^[0-9a-fA-F]{64}$') { throw "manifest field '$FieldName' is not a SHA-256 digest" }
}

# Assert-RealFile verifies a file exists and is not a reparse point before hashing it.
# Assert-RealFile 校验文件存在且不是重解析点，然后才允许计算哈希。
function Assert-RealFile {
    param([Parameter(Mandatory=$true)] [string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "native artifact file is missing: $Path" }
    $Item = Get-Item -LiteralPath $Path -Force
    if ($Item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "native artifact path must not be a reparse point: $Path" }
}

# Resolve-ArtifactFile converts a manifest-relative support path into a regular file beside the manifest.
# Resolve-ArtifactFile 将清单中的相对支持路径解析为清单旁的普通文件。
function Resolve-ArtifactFile {
    param([Parameter(Mandatory=$true)] [string]$ManifestDirectory, [Parameter(Mandatory=$true)] [string]$RelativePath)
    if ($RelativePath -notmatch '^[A-Za-z0-9._/-]+$' -or $RelativePath.Contains("..") -or $RelativePath.Contains("\\") -or [IO.Path]::IsPathRooted($RelativePath)) { throw "native support artifact path is unsafe: $RelativePath" }
    $Candidate = Join-Path $ManifestDirectory ($RelativePath.Replace("/", [IO.Path]::DirectorySeparatorChar))
    $ExpectedRoot = [IO.Path]::GetFullPath($ManifestDirectory).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    if (-not [IO.Path]::GetFullPath($Candidate).StartsWith($ExpectedRoot, [StringComparison]::OrdinalIgnoreCase)) { throw "native support artifact escapes manifest directory: $RelativePath" }
    return $Candidate
}

# Validate-SupportArtifacts checks the fixed support list and each recorded content digest.
# Validate-SupportArtifacts 校验固定支持文件清单以及每个记录的内容摘要。
function Validate-SupportArtifacts {
    param([Parameter(Mandatory=$true)] [object]$Manifest, [Parameter(Mandatory=$true)] [string]$ManifestDirectory)
    if ($null -eq $Manifest.artifacts) { throw "native manifest must contain artifacts" }
    $Entries = @($Manifest.artifacts)
    if ($Entries.Count -ne $NativeSupportArtifactPaths.Count) { throw "native manifest artifacts count mismatch" }
    $Seen = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::Ordinal)
    foreach ($Entry in $Entries) {
        $PropertyNames = @($Entry.PSObject.Properties.Name)
        if ($PropertyNames.Count -ne 2 -or $PropertyNames -notcontains "path" -or $PropertyNames -notcontains "sha256") { throw "native manifest artifact entry must contain only path and sha256" }
        $Path = [string]$Entry.path
        $Digest = [string]$Entry.sha256
        if ($NativeSupportArtifactPaths -notcontains $Path) { throw "native manifest contains an unsupported artifact path: $Path" }
        if (-not $Seen.Add($Path)) { throw "native manifest contains a duplicate artifact path: $Path" }
        Assert-RegularSha256 -Value $Digest -FieldName "artifacts[$Path].sha256"
        $ArtifactPath = Resolve-ArtifactFile -ManifestDirectory $ManifestDirectory -RelativePath $Path
        Assert-RealFile -Path $ArtifactPath
        $Sha256 = [Security.Cryptography.SHA256]::Create()
        try {
            $ActualHash = ([BitConverter]::ToString($Sha256.ComputeHash([IO.File]::ReadAllBytes($ArtifactPath)))).Replace("-", "").ToLowerInvariant()
        }
        finally { $Sha256.Dispose() }
        if ($ActualHash -ne $Digest.ToLowerInvariant()) { throw "native support artifact hash mismatch: $Path" }
    }
    foreach ($ExpectedPath in $NativeSupportArtifactPaths) {
        if (-not $Seen.Contains($ExpectedPath)) { throw "native manifest is missing support artifact: $ExpectedPath" }
    }
}

# Validate-NativeArtifact checks schema, ABI, target, engine, filename, and content hashes as one closed contract.
# Validate-NativeArtifact 一次性校验 schema、ABI、target、engine、文件名和内容哈希，形成闭合契约。
function Validate-NativeArtifact {
    Assert-RealFile -Path $ManifestPath
    $ManifestItem = Get-Item -LiteralPath $ManifestPath -Force
    if ($ManifestItem.Name -ne "manifest.json") { throw "native manifest must be named manifest.json" }
    $Manifest = Get-Content -LiteralPath $ManifestPath -Raw | ConvertFrom-Json
    if ([int]$Manifest.schema_version -ne 1) { throw "unsupported native manifest schema_version" }
    if ([int]$Manifest.abi_version -ne 1) { throw "unsupported native manifest abi_version" }
    if ([string]$Manifest.engine_version -ne $ExpectedEngineVersion) { throw "native engine version mismatch: $($Manifest.engine_version)" }
    if ([string]$Manifest.target -ne $Target) { throw "native target mismatch: $($Manifest.target)" }
    Validate-SupportArtifacts -Manifest $Manifest -ManifestDirectory (Split-Path $ManifestPath -Parent)
    Assert-RegularSha256 -Value ([string]$Manifest.source_digest) -FieldName "source_digest"
    Assert-RegularSha256 -Value ([string]$Manifest.cargo_lock_sha256) -FieldName "cargo_lock_sha256"
    Assert-RegularSha256 -Value ([string]$Manifest.library_sha256) -FieldName "library_sha256"
    if (-not [string]::IsNullOrWhiteSpace($ExpectedSourceDigest) -and ([string]$Manifest.source_digest).ToLowerInvariant() -ne $ExpectedSourceDigest.ToLowerInvariant()) { throw "native source digest mismatch" }
    if (-not [string]::IsNullOrWhiteSpace($ExpectedCargoLockSha256) -and ([string]$Manifest.cargo_lock_sha256).ToLowerInvariant() -ne $ExpectedCargoLockSha256.ToLowerInvariant()) { throw "native Cargo.lock digest mismatch" }
    $LibraryName = [string]$Manifest.library_file
    if ([string]::IsNullOrWhiteSpace($LibraryName) -or $LibraryName.Contains("/") -or $LibraryName.Contains("\") -or [IO.Path]::IsPathRooted($LibraryName)) { throw "native library_file must be a single filename" }
    if ($LibraryName -ne (Get-ExpectedLibraryName -TargetName $Target)) { throw "native library_file does not match target ABI name" }
    if ([string]::IsNullOrWhiteSpace($LibraryPath)) { $LibraryPath = Join-Path (Split-Path $ManifestPath -Parent) $LibraryName }
    if ([IO.Path]::GetFullPath((Split-Path $LibraryPath -Parent)) -ne [IO.Path]::GetFullPath((Split-Path $ManifestPath -Parent))) { throw "native library and manifest must share one directory" }
    Assert-RealFile -Path $LibraryPath
    if ((Split-Path $LibraryPath -Leaf) -ne $LibraryName) { throw "native library path does not match manifest library_file" }
    $Sha256 = [Security.Cryptography.SHA256]::Create()
    try {
        $ActualHash = ([BitConverter]::ToString($Sha256.ComputeHash([IO.File]::ReadAllBytes($LibraryPath)))).Replace("-", "").ToLowerInvariant()
    }
    finally { $Sha256.Dispose() }
    if ($ActualHash -ne ([string]$Manifest.library_sha256).ToLowerInvariant()) { throw "native library hash mismatch: $LibraryPath" }
    Write-Output ("native artifact validated: target={0}; engine={1}; abi={2}; library={3}" -f $Target, $Manifest.engine_version, $Manifest.abi_version, $LibraryName)
}

Validate-NativeArtifact
