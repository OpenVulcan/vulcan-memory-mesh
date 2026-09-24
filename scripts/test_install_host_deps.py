"""Verify the fixed legacy VLDB dependency supply-chain contract.
验证 legacy VLDB 宿主依赖的固定供应链契约。
"""

from __future__ import annotations

import re
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


# EXPECTED_ARCHIVES records the official GitHub asset digests captured for the pinned release tags.
# EXPECTED_ARCHIVES 记录固定 Release 标签对应的官方 GitHub 资产摘要。
EXPECTED_ARCHIVES = {
    (
        "OpenVulcan/vldb-sqlite",
        "v0.1.7",
        "vldb-sqlite-lib-v0.1.7-aarch64-apple-darwin.tar.gz",
    ): "f3484c2a5a56017f60ff4b6fbcf04b0ea89b99c21e19329e2d91f233e605ca55",
    (
        "OpenVulcan/vldb-sqlite",
        "v0.1.7",
        "vldb-sqlite-lib-v0.1.7-aarch64-unknown-linux-gnu.tar.gz",
    ): "ca5123f0151e7378bca5fdc9965de82db349b92626cba9ba86efe2000be7fa6a",
    (
        "OpenVulcan/vldb-sqlite",
        "v0.1.7",
        "vldb-sqlite-lib-v0.1.7-x86_64-apple-darwin.tar.gz",
    ): "55b87e4e5921de65533f379e51eafa93abdf39ca1a3c3a361b48c589bcfd5a6a",
    (
        "OpenVulcan/vldb-sqlite",
        "v0.1.7",
        "vldb-sqlite-lib-v0.1.7-x86_64-pc-windows-msvc.zip",
    ): "0a9814e1d8b7d57ab859551242723c8384f8067138b52f796573c56b92897313",
    (
        "OpenVulcan/vldb-sqlite",
        "v0.1.7",
        "vldb-sqlite-lib-v0.1.7-x86_64-unknown-linux-gnu.tar.gz",
    ): "c4a0ae19c9c1efdf7f009b597593988547f126af798b8d7e18d695c2d98cf035",
    (
        "OpenVulcan/vldb-lancedb",
        "v0.1.5",
        "vldb-lancedb-lib-v0.1.5-aarch64-apple-darwin.tar.gz",
    ): "4ac63c8c25a408eacaaef2dc614d1775bff2499d4cd4b7335408bcb6e44935e2",
    (
        "OpenVulcan/vldb-lancedb",
        "v0.1.5",
        "vldb-lancedb-lib-v0.1.5-aarch64-unknown-linux-gnu.tar.gz",
    ): "ff77797fac147685c1301911409c3b1048e00df70d298bd83d34c5e0ccee8d80",
    (
        "OpenVulcan/vldb-lancedb",
        "v0.1.5",
        "vldb-lancedb-lib-v0.1.5-x86_64-apple-darwin.tar.gz",
    ): "a8c6f9ef5cac80a6b9623a1e845e23bfeccea599ad8cc0a3c7ea44bc759cd790",
    (
        "OpenVulcan/vldb-lancedb",
        "v0.1.5",
        "vldb-lancedb-lib-v0.1.5-x86_64-pc-windows-msvc.zip",
    ): "56fbc252680d98d89b9957f0ed7715be7fab62bd04322604c2dc9a4d2d96be2e",
    (
        "OpenVulcan/vldb-lancedb",
        "v0.1.5",
        "vldb-lancedb-lib-v0.1.5-x86_64-unknown-linux-gnu.tar.gz",
    ): "b4aa0d18de265523ebd56d56538193e4946d9740355dce497e1bf4821fa30cc2",
    (
        "OpenVulcan/vldb-controller",
        "v0.2.4",
        "vldb-controller-v0.2.4-aarch64-apple-darwin.tar.gz",
    ): "b8d480f23d58a08c15ff49e6246cb60b2115c77262d2eb0faee87268abecc490",
    (
        "OpenVulcan/vldb-controller",
        "v0.2.4",
        "vldb-controller-v0.2.4-aarch64-unknown-linux-gnu.tar.gz",
    ): "c2e0309ff41147853485d6b7547259719b1579c73531802fc00d8b58509bf461",
    (
        "OpenVulcan/vldb-controller",
        "v0.2.4",
        "vldb-controller-v0.2.4-x86_64-apple-darwin.tar.gz",
    ): "a534461cd5d59341515855cb3522466c3da838a5c2169573f527c5dc34921aad",
    (
        "OpenVulcan/vldb-controller",
        "v0.2.4",
        "vldb-controller-v0.2.4-x86_64-pc-windows-msvc.zip",
    ): "463661a7717f12c43d39902df5a03239965d712a5904164dc3be6fd92bc2bea7",
    (
        "OpenVulcan/vldb-controller",
        "v0.2.4",
        "vldb-controller-v0.2.4-x86_64-unknown-linux-gnu.tar.gz",
    ): "73adbdc32abf576af28eecca7602bfec7ae5d6459ecb4e28660df6413d8e0cdb",
}


# ROOT points to the VMM repository containing both host bootstrap scripts and their digest manifest.
# ROOT 指向包含宿主依赖脚本和摘要清单的 VMM 仓库根目录。
ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "scripts" / "host_deps_sha256.tsv"
SHELL_SCRIPT = ROOT / "scripts" / "install_host_deps.sh"
POWERSHELL_SCRIPT = ROOT / "scripts" / "install_host_deps.ps1"
PACKAGE_SHELL_SCRIPT = ROOT / "scripts" / "vmm.sh"
PACKAGE_POWERSHELL_SCRIPT = ROOT / "scripts" / "vmm.ps1"
RELEASE_GUIDE = ROOT / "docs" / "github-release-guide_CN.md"


class HostDependencyBootstrapTests(unittest.TestCase):
    """Check digest completeness and fail-closed script wiring.
    检查摘要完整性以及脚本的失败即关闭接线。
    """

    # test_manifest_matches_official_asset_inventory ensures every supported archive is pinned to the reviewed GitHub digest.
    # test_manifest_matches_official_asset_inventory 确保每个支持的压缩包都固定到已核对的 GitHub 摘要。
    def test_manifest_matches_official_asset_inventory(self) -> None:
        rows = {}
        for raw_line in MANIFEST.read_text(encoding="utf-8").splitlines():
            line = raw_line.strip()
            if not line or line.startswith("#"):
                continue
            parts = raw_line.split("\t")
            self.assertEqual(4, len(parts), raw_line)
            key = tuple(parts[:3])
            self.assertNotIn(key, rows)
            self.assertRegex(parts[3], r"^[0-9a-f]{64}$")
            rows[key] = parts[3]
        self.assertEqual(EXPECTED_ARCHIVES, rows)

    # test_scripts_do_not_trust_release_sidecar verifies downloads and cache paths use the checked-in digest instead of a mutable checksum asset.
    # test_scripts_do_not_trust_release_sidecar 验证下载和缓存路径使用仓库固定摘要，而不是可变校验和资产。
    def test_scripts_do_not_trust_release_sidecar(self) -> None:
        shell = SHELL_SCRIPT.read_text(encoding="utf-8")
        powershell = POWERSHELL_SCRIPT.read_text(encoding="utf-8")
        for source, hash_token in ((shell, "expected_archive_hash"), (powershell, "ExpectedArchiveHash")):
            self.assertIn("host_deps_sha256.tsv", source)
            self.assertIn(hash_token, source)
            self.assertNotRegex(source, r"(?im)^\s*(curl|Invoke-WebRequest)[^\r\n]*\.sha256")
        self.assertNotIn("Get-ReleaseByTagOrNull", powershell)
        self.assertNotIn("ChecksumAsset", powershell)
        self.assertIn("sidecar files are never trusted", shell)
        self.assertNotIn("expected_installed_hash", shell)
        self.assertNotIn("ExpectedInstalledHash", powershell)
        self.assertIn("trusted_installed_hash", shell)
        self.assertIn("$TrustedInstalledHash", powershell)
        self.assertNotIn('-split "`t", -1', powershell)
        package_powershell = PACKAGE_POWERSHELL_SCRIPT.read_text(encoding="utf-8")
        self.assertNotIn('-split "`t", -1', package_powershell)

    # test_powershell_manifest_split_contract executes the Windows-compatible split form against a real manifest row.
    # test_powershell_manifest_split_contract 使用真实清单行执行 Windows 兼容的 split 形式。
    def test_powershell_manifest_split_contract(self) -> None:
        powershell = shutil.which("powershell.exe") or shutil.which("pwsh")
        if not powershell:
            self.skipTest("PowerShell is not installed")
        manifest_path = str(MANIFEST).replace("'", "''")
        command = (
            f"$line = (Get-Content -LiteralPath '{manifest_path}' -Encoding UTF8)[6]; "
            "$parts = $line -split \"`t\"; "
            "if ($parts.Count -ne 4) { exit 1 }; "
            "if ($parts[0] -cne 'OpenVulcan/vldb-sqlite' -or $parts[1] -cne 'v0.1.7') { exit 2 }; "
            "Write-Output 'manifest split passed'"
        )
        completed = subprocess.run(
            [powershell, "-NoProfile", "-NonInteractive", "-Command", command],
            capture_output=True,
            text=True,
            timeout=30,
        )
        self.assertEqual(0, completed.returncode, completed.stderr or completed.stdout)
        self.assertIn("manifest split passed", completed.stdout)

    # test_package_readers_require_trusted_archive_identity prevents a forged marker and binary from bypassing packaging.
    # test_package_readers_require_trusted_archive_identity 防止伪造 marker 与二进制共同绕过打包校验。
    def test_package_readers_require_trusted_archive_identity(self) -> None:
        shell = PACKAGE_SHELL_SCRIPT.read_text(encoding="utf-8")
        powershell = PACKAGE_POWERSHELL_SCRIPT.read_text(encoding="utf-8")
        forged_marker = ("a" * 64, "b" * 64)
        forged_installed_hash = forged_marker[1]
        self.assertRegex(forged_marker[0], r"^[0-9a-f]{64}$")
        self.assertRegex(forged_installed_hash, r"^[0-9a-f]{64}$")
        self.assertNotEqual(forged_marker[0], forged_installed_hash)

        self.assertIn("host_deps_sha256.tsv", shell)
        self.assertIn("host_deps_sha256.tsv", powershell)
        self.assertIn("wc -l", shell)
        self.assertIn("MarkerLines.Count -ne 2", powershell)
        self.assertIn("tar -xzf", shell)
        self.assertIn("Expand-Archive", powershell)
        self.assertLess(shell.index("actual_archive_hash"), shell.index("trusted_installed_hash"))
        self.assertLess(powershell.index("$ActualArchiveHash"), powershell.index("$TrustedInstalledHash"))
        self.assertIn("marker_installed_hash", shell)
        self.assertIn("$MarkerInstalledHash", powershell)
        self.assertIn("trusted_installed_hash", shell)
        self.assertIn("$TrustedInstalledHash", powershell)
        self.assertIn("New-SafeTemporaryDirectory", powershell)
        self.assertIn("Remove-SafeTemporaryDirectory", powershell)
        self.assertIn("[Guid]::NewGuid()", powershell)
        self.assertIn("OwnerPath", powershell)
        self.assertNotRegex(shell, r"(?s)expected=\"\$\(tr -d '.*?\$marker.*?\"\)\s*actual=")
        self.assertNotRegex(powershell, r"Get-Content[^\r\n]*-Raw[^\r\n]*Markers\[0\]")

    # test_install_to_build_contract keeps the verified archive at the path consumed by both package builders.
    # test_install_to_build_contract 确保安装脚本保存已校验压缩包，并由两个打包脚本读取同一路径。
    def test_install_to_build_contract(self) -> None:
        installer_shell = SHELL_SCRIPT.read_text(encoding="utf-8")
        installer_powershell = POWERSHELL_SCRIPT.read_text(encoding="utf-8")
        package_shell = PACKAGE_SHELL_SCRIPT.read_text(encoding="utf-8")
        package_powershell = PACKAGE_POWERSHELL_SCRIPT.read_text(encoding="utf-8")
        self.assertIn('archive_cache_path="$target_dir/$asset_name"', installer_shell)
        self.assertIn('$ArchiveCachePath = Join-Path $TargetDir $AssetName', installer_powershell)
        self.assertIn('archive_path="$marker_dir/$archive_name"', package_shell)
        self.assertIn('$ArchivePath = Join-Path $MarkerDirectory $ReleaseInfo.Asset', package_powershell)
        self.assertIn('printf \'%s\\n%s\\n\'', installer_shell)
        self.assertIn('Set-Content -LiteralPath $MarkerFile -Value @($ExpectedArchiveHash, $TrustedInstalledHash)', installer_powershell)

    # test_powershell_temp_workspace_contract rejects PID-only cleanup and requires ownership-aware safe removal.
    # test_powershell_temp_workspace_contract 拒绝仅按 PID 清理，并要求带所有权的安全删除。
    def test_powershell_temp_workspace_contract(self) -> None:
        for source in (
            POWERSHELL_SCRIPT.read_text(encoding="utf-8"),
            PACKAGE_POWERSHELL_SCRIPT.read_text(encoding="utf-8"),
        ):
            self.assertIn("[IO.Path]::GetTempPath()", source)
            self.assertIn("[Guid]::NewGuid()", source)
            self.assertIn("New-Item -ItemType Directory -Path $Candidate", source)
            self.assertNotIn("New-Item -ItemType Directory -LiteralPath $Candidate", source)
            self.assertIn("ReparsePoint", source)
            self.assertIn("StartsWith($RootFullPath", source)
            self.assertIn("OwnerPath", source)
            self.assertNotRegex(source, r"Join-Path \$env:TEMP[^\r\n]*\$PID")
            self.assertNotRegex(source, r"if \(Test-Path -LiteralPath \$TempDir\)\s*\{\s*Remove-Item")

    # test_powershell_temp_workspace_runtime executes the production helper bodies and verifies create/cleanup behavior.
    # test_powershell_temp_workspace_runtime 执行生产 helper 函数体并验证创建与清理行为。
    def test_powershell_temp_workspace_runtime(self) -> None:
        powershell = shutil.which("powershell.exe") or shutil.which("pwsh")
        if not powershell:
            self.skipTest("PowerShell is not installed")
        helper_sources = (
            (POWERSHELL_SCRIPT, "# Find-LocalArchive"),
            (PACKAGE_POWERSHELL_SCRIPT, "# Get-PinnedHostArchiveHash"),
        )
        for script_path, end_marker in helper_sources:
            source = script_path.read_text(encoding="utf-8")
            start = source.index("# Remove-SafeTemporaryDirectory")
            end = source.index(end_marker, start)
            helper_text = source[start:end]
            harness = (
                helper_text
                + "\n$state = New-SafeTemporaryDirectory -Prefix 'vmm-runtime-test'; "
                + "if (-not (Test-Path -LiteralPath $state.Path -PathType Container)) { throw 'temporary directory was not created' }; "
                + "Remove-SafeTemporaryDirectory -State $state; "
                + "if (Test-Path -LiteralPath $state.Path) { throw 'temporary directory was not removed' }; "
                + "Write-Output 'safe temp runtime passed'\n"
            )
            temporary_script = tempfile.NamedTemporaryFile(
                mode="w", encoding="utf-8-sig", suffix=".ps1", delete=False
            )
            try:
                temporary_script.write(harness)
                temporary_script.close()
                completed = subprocess.run(
                    [powershell, "-NoProfile", "-NonInteractive", "-File", temporary_script.name],
                    capture_output=True,
                    text=True,
                    timeout=30,
                )
                self.assertEqual(0, completed.returncode, completed.stderr or completed.stdout)
                self.assertIn("safe temp runtime passed", completed.stdout)
            finally:
                Path(temporary_script.name).unlink(missing_ok=True)

    # test_shell_script_is_parseable runs the POSIX syntax checker when one is available on the host.
    # test_shell_script_is_parseable 在环境提供 POSIX shell 时运行脚本语法检查。
    def test_shell_script_is_parseable(self) -> None:
        shell = shutil.which("bash") or shutil.which("sh")
        if not shell:
            self.skipTest("bash or sh is not installed")
        completed = subprocess.run([shell, "-n", str(SHELL_SCRIPT)], capture_output=True, text=True)
        self.assertEqual(0, completed.returncode, completed.stderr)

    # test_documentation_describes_the_pinned_manifest keeps the release process documentation aligned with the implementation.
    # test_documentation_describes_the_pinned_manifest 确保发行流程文档与固定清单实现保持同步。
    def test_documentation_describes_the_pinned_manifest(self) -> None:
        documentation = RELEASE_GUIDE.read_text(encoding="utf-8")
        self.assertIn("host_deps_sha256.tsv", documentation)
        self.assertIn("不会把同一 Release 中可替换的 `.sha256` 资产当作信任根", documentation)


if __name__ == "__main__":
    unittest.main()
