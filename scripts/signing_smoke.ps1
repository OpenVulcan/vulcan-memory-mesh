# Test SimplySign on a disposable Windows runner, using only a harmless CI fixture.
# 在一次性 Windows Runner 上使用无副作用的 CI 测试程序验证 SimplySign。
param(
    [Parameter(Mandatory)][ValidateSet('Install', 'Sign')][string]$Phase,
    [Parameter(Mandatory)][string]$EvidenceDirectory
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# Invoke a native tool with bounded execution; captured output is never logged automatically.
# 限时运行原生工具，捕获的输出绝不自动写入日志。
function Invoke-BoundedTool {
    param([string]$Executable, [string[]]$Arguments, [int]$TimeoutSeconds = 120)
    $start = [System.Diagnostics.ProcessStartInfo]::new($Executable)
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.WindowStyle = [System.Diagnostics.ProcessWindowStyle]::Hidden
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($argument in $Arguments) { $start.ArgumentList.Add($argument) }
    $process = [System.Diagnostics.Process]::Start($start)
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()
    try {
        if (-not $process.WaitForExit($TimeoutSeconds * 1000)) {
            $process.Kill($true)
            $process.WaitForExit()
            throw 'native_tool_timeout'
        }
        return [pscustomobject]@{ ExitCode = $process.ExitCode; Stdout = $stdout.Result; Stderr = $stderr.Result }
    } finally {
        $process.Dispose()
    }
}

# Select only currently valid Certum code-signing certificates backed by an available key.
# 仅选取当前有效且已关联可用私钥的 Certum 代码签名证书。
function Get-CertumSigningCertificates {
    $now = Get-Date
    return @(Get-ChildItem Cert:\CurrentUser\My | Where-Object {
        $_.HasPrivateKey -and $_.NotBefore -le $now -and $_.NotAfter -gt $now -and
        $_.Issuer -match 'Certum' -and
        @($_.EnhancedKeyUsageList | Where-Object ObjectId -eq '1.3.6.1.5.5.7.3.3').Count -gt 0
    })
}

# These pinned values come from the official Certum download page and verified MSI metadata.
# 以下固定值来自 Certum 官方下载页面和已验证的 MSI 元数据。
$installerUrl = 'https://files.certum.eu/software/SimplySignDesktop/Windows/9.4.4.92/SimplySignDesktop-9.4.4.92-64-bit-en.msi'
$installerSha256 = '8ec420fc27798b86078b7bd02fe7152097e1b3005bab51820eaca8e57df84da3'
$installerSigner = 'AC9C643063CD501E851A7B6A9762E295FBABB012'
$desktop = Join-Path $env:ProgramFiles 'Certum\SimplySign Desktop\SimplySignDesktop.exe'
$evidence = [IO.Path]::GetFullPath($EvidenceDirectory)
New-Item -ItemType Directory -Path $evidence -Force | Out-Null

if ($Phase -eq 'Install') {
    # Install only the exact official binary whose hash and Authenticode signature were checked.
    # 仅安装哈希与 Authenticode 签名均通过校验的指定官方安装包。
    $installer = Join-Path $env:RUNNER_TEMP 'SimplySignDesktop-9.4.4.92.msi'
    Invoke-WebRequest -Uri $installerUrl -OutFile $installer
    if ((Get-FileHash -LiteralPath $installer -Algorithm SHA256).Hash.ToLowerInvariant() -ne $installerSha256) {
        throw 'simplysign_installer_hash_mismatch'
    }
    $signature = Get-AuthenticodeSignature -LiteralPath $installer
    if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Thumbprint -ne $installerSigner) {
        throw 'simplysign_installer_signature_invalid'
    }
    $installed = Invoke-BoundedTool -Executable 'msiexec.exe' -Arguments @('/i', $installer, '/qn', '/norestart') -TimeoutSeconds 240
    if ($installed.ExitCode -notin @(0, 3010)) { throw "simplysign_install_failed_$($installed.ExitCode)" }
    if (-not (Test-Path -LiteralPath $desktop -PathType Leaf) -or
        -not (Test-Path -LiteralPath "$env:WINDIR\System32\SimplySignPKCS.dll" -PathType Leaf)) {
        throw 'simplysign_installed_files_missing'
    }
    [pscustomobject]@{
        installerSha256 = $installerSha256
        installerSigner = $installerSigner
        version = (Get-Item -LiteralPath $desktop).VersionInfo.FileVersion
        installerExitCode = $installed.ExitCode
    } | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $evidence 'installer-result.json') -Encoding utf8
    Write-Output 'Verified official SimplySign Desktop installation completed'
    exit 0
}

# Keep a credential-free report even when login or the signing provider fails.
# 即使登录或签名提供程序失败，也保留不含凭据的测试报告。
$report = [ordered]@{
    kind = 'authenticode'
    status = 'failed'
    stage = 'credentials'
    sessionId = [System.Diagnostics.Process]::GetCurrentProcess().SessionId
    userInteractive = [Environment]::UserInteractive
}
$client = $null
$email = $env:CERTUM_TOTP_EMAIL
$otp = $null
try {
    if ([string]::IsNullOrWhiteSpace($email) -or [string]::IsNullOrWhiteSpace($env:CERTUM_TOTP_SECRET)) {
        throw 'CERTUM_credentials_unavailable_to_this_repository'
    }
    $probe = Join-Path $evidence 'probe.exe'
    if (-not (Test-Path -LiteralPath $probe -PathType Leaf)) { throw 'probe_missing' }
    $report.unsignedSha256 = (Get-FileHash -LiteralPath $probe -Algorithm SHA256).Hash
    $prior = @(Get-CertumSigningCertificates | ForEach-Object Thumbprint)
    if ($prior.Count -ne 0) { throw 'runner_is_not_a_clean_certum_environment' }

    # Use the observed /autologin account OTP interface, once, with a fresh TOTP window.
    # 使用已查证的 /autologin 账户 OTP 接口，在新的验证码时间窗内仅尝试一次登录。
    $report.stage = 'login'
    $otp = & python scripts/signing_smoke.py totp
    if ($LASTEXITCODE -ne 0 -or $otp -notmatch '^\d{6}$') { throw 'totp_generation_failed' }
    Write-Output "::add-mask::$otp"
    Remove-Item Env:CERTUM_TOTP_SECRET -ErrorAction SilentlyContinue
    Remove-Item Env:CERTUM_TOTP_EMAIL -ErrorAction SilentlyContinue
    $start = [System.Diagnostics.ProcessStartInfo]::new($desktop)
    $start.UseShellExecute = $false
    $start.WindowStyle = [System.Diagnostics.ProcessWindowStyle]::Hidden
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $start.ArgumentList.Add('/autologin')
    $start.ArgumentList.Add($email)
    $start.ArgumentList.Add($otp)
    $client = [System.Diagnostics.Process]::Start($start)
    $clientOutput = $client.StandardOutput.ReadToEndAsync()
    $clientError = $client.StandardError.ReadToEndAsync()
    $otp = $null
    $email = $null

    # Certificate readiness, not process startup, determines whether cloud login succeeded.
    # 通过证书就绪状态判断云登录成功，而不是仅检查进程是否启动。
    $deadline = [DateTime]::UtcNow.AddSeconds(90)
    do {
        $certificates = @(Get-CertumSigningCertificates)
        if ($certificates.Count -gt 0) { break }
        Start-Sleep -Seconds 2
    } while ([DateTime]::UtcNow -lt $deadline)
    $report.eligibleCertificates = $certificates.Count
    $report.clientExited = $client.HasExited
    if ($certificates.Count -ne 1) { throw 'exactly_one_ready_certum_certificate_required' }
    $certificate = $certificates[0]
    $report.certificateThumbprint = $certificate.Thumbprint
    $report.certificateExpiresUtc = $certificate.NotAfter.ToUniversalTime().ToString('o')

    # Resolve the SDK installed on this runner and pin signing to the discovered certificate.
    # 使用当前 Runner 安装的 SDK，并固定选用已就绪的那张证书。
    $sdkRoot = Join-Path ${env:ProgramFiles(x86)} 'Windows Kits\10\bin'
    $sdkVersions = @(Get-ChildItem -LiteralPath $sdkRoot -Directory |
        Where-Object Name -match '^\d+\.\d+\.\d+\.\d+$' |
        Sort-Object { [version]$_.Name } -Descending)
    $signTools = @($sdkVersions | ForEach-Object { Join-Path $_.FullName 'x64\signtool.exe' } |
        Where-Object { Test-Path -LiteralPath $_ -PathType Leaf })
    if ($signTools.Count -eq 0) { throw 'windows_sdk_signtool_missing' }
    $signTool = $signTools[0]
    $report.stage = 'sign'
    $signed = Invoke-BoundedTool -Executable $signTool -Arguments @(
        'sign', '/sha1', $certificate.Thumbprint, '/fd', 'SHA256',
        '/tr', 'http://time.certum.pl', '/td', 'SHA256', '/d', 'OpenVulcan CI signing probe', $probe
    ) -TimeoutSeconds 90
    $report.signExitCode = $signed.ExitCode
    if ($signed.ExitCode -ne 0) { throw 'signtool_sign_failed' }

    $report.stage = 'verify'
    $verified = Invoke-BoundedTool -Executable $signTool -Arguments @('verify', '/pa', '/all', $probe)
    if ($verified.ExitCode -ne 0) { throw 'signtool_verify_failed' }
    $signature = Get-AuthenticodeSignature -LiteralPath $probe
    if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Thumbprint -ne $certificate.Thumbprint -or
        $null -eq $signature.TimeStamperCertificate) { throw 'signed_identity_or_timestamp_invalid' }
    $report.signedSha256 = (Get-FileHash -LiteralPath $probe -Algorithm SHA256).Hash
    $report.timestampSignerThumbprint = $signature.TimeStamperCertificate.Thumbprint

    $report.stage = 'tamper_rejection'
    $tampered = Join-Path $env:RUNNER_TEMP 'tampered-signing-probe.exe'
    & python scripts/signing_smoke.py tamper-pe $probe $tampered
    if ($LASTEXITCODE -ne 0) { throw 'tamper_fixture_creation_failed' }
    $rejected = Get-AuthenticodeSignature -LiteralPath $tampered
    if ($rejected.Status -ne 'HashMismatch') { throw 'tampered_pe_not_rejected' }
    Remove-Item -LiteralPath $tampered -Force
    $report.tamperRejected = $true
    $report.status = 'passed'
    $report.stage = 'complete'
} catch {
    # Only stable codes generated here are persisted; upstream exception details stay private.
    # 仅保存本脚本生成的稳定错误码，外部异常细节不进入报告。
    $message = $_.Exception.Message
    $report.error = if ($message -match '^[A-Za-z0-9_]+$') { $message } else { 'unexpected_windows_signing_error' }
    throw $report.error
} finally {
    Remove-Item Env:CERTUM_TOTP_SECRET -ErrorAction SilentlyContinue
    Remove-Item Env:CERTUM_TOTP_EMAIL -ErrorAction SilentlyContinue
    $email = $null
    $otp = $null
    if ($null -ne $client) {
        try { $null = Invoke-BoundedTool -Executable $desktop -Arguments @('/close') -TimeoutSeconds 10 } catch { }
        if (-not $client.HasExited) { $client.Kill($true) }
        $client.Dispose()
    }
    $json = $report | ConvertTo-Json
    $json | Set-Content -LiteralPath (Join-Path $evidence 'authenticode-result.json') -Encoding utf8
    Write-Output $json
}
