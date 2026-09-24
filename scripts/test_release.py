"""Verify that release gates reject skipped acceptance and incomplete or tampered assets.
验证发行门禁会拒绝跳过验收、缺失产物及被篡改的文件。
"""

import json
import base64
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
import zipfile
from unittest.mock import patch

try:
    from scripts import release
except ModuleNotFoundError:
    import release


class ReleaseGateTests(unittest.TestCase):
    """Exercise publication refusal before any network mutation can happen.
    验证任何网络修改之前的发行拒绝条件。
    """

    def test_missing_and_skipped_native_tests_are_rejected(self):
        """Require actual pass events for every native acceptance test.
        每个强制原生验收测试都必须具有实际通过事件。
        """
        with tempfile.TemporaryDirectory() as directory:
            log = Path(directory) / "tests.jsonl"
            log.write_text('', encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "Missing"):
                release.check_tests(log)
            log.write_text(json.dumps({"Test": "TestNativeLibraryRoundTrip", "Action": "skip"}) + "\n", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "skipped"):
                release.check_tests(log)
            pass_tests = [
                "TestNativeLibraryABIAndErrorBoundary",
                "TestNativeLibraryRoundTrip",
                "TestNativeLibraryPendingSchemaRecovery",
                "TestPackagedNativeRuntimeUsesIsolatedNativeArtifacts",
                "TestPackagedNativeRuntimeUsesIsolatedNativeArtifacts/legacy-split",
            ]
            log.write_text(
                "".join(json.dumps({"Test": name, "Action": "pass"}) + "\n" for name in pass_tests),
                encoding="utf-8",
            )
            with self.assertRaisesRegex(ValueError, "Missing"):
                release.check_tests(log)
            log.write_text(
                "".join(
                    json.dumps({"Test": name, "Action": "pass"}) + "\n"
                    for name in pass_tests + ["TestPackagedNativeRuntimeUsesIsolatedNativeArtifacts/legacy-controller"]
                ),
                encoding="utf-8",
            )
            release.check_tests(log)

    def test_verified_release_remains_draft(self):
        """Require an uploaded release to remain a draft after its remote bytes are verified.
        要求远端资产逐字节验证完成后，发行版仍保持草稿状态。
        """
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            dist = root / "dist"
            dist.mkdir()
            asset = dist / "verified.zip"
            asset.write_bytes(b"signed release fixture")
            tag, commit = "v0.2.0", "a" * 40
            calls = []
            is_draft = True

            def fake_run(*args):
                """Return fixed GitHub responses and copy the staged asset into the download directory.
                返回固定 GitHub 响应，并将暂存资产复制到模拟下载目录。
                """
                calls.append(args)
                if args[:2] == ("gh", "api"):
                    return json.dumps({"object": {"type": "commit", "sha": commit}})
                if args[:3] == ("gh", "release", "download"):
                    shutil.copyfile(asset, Path(args[-1]) / asset.name)
                    return ""
                if args[:3] == ("gh", "release", "view"):
                    if "isDraft" in args:
                        return "true\n" if is_draft else "false\n"
                    return "https://github.com/OpenVulcan/vulcan-memory-mesh/releases/tag/" + tag
                return ""

            missing_release = subprocess.CompletedProcess([], 1, "", "HTTP 404")
            with (
                patch.object(release, "__file__", str(root / "scripts" / "release.py")),
                patch.object(release, "verify_assets", return_value=[asset]),
                patch.object(release, "run", side_effect=fake_run),
                patch.object(release.subprocess, "run", return_value=missing_release),
                patch.dict(os.environ, {"GH_REPO": "OpenVulcan/vulcan-memory-mesh"}),
                patch("builtins.print"),
            ):
                release.create_draft(tag, commit)
                self.assertTrue(any(call[:3] == ("gh", "release", "create") and "--draft" in call for call in calls))
                self.assertFalse(any(call[:3] == ("gh", "release", "edit") for call in calls))
                is_draft = False
                with self.assertRaisesRegex(ValueError, "no longer a draft"):
                    release.create_draft(tag, commit)

    def test_incomplete_and_tampered_release_cannot_publish(self):
        """Verify five identities and content digests, then reject missing, foreign and altered files.
        校验五个平台身份及内容摘要，然后拒绝缺失、外来和被修改的文件。
        """
        with tempfile.TemporaryDirectory() as directory:
            dist = Path(directory)
            tag, commit = "v0.1.0", "a" * 40
            with self.assertRaises(FileNotFoundError):
                release.verify_assets(dist, tag, commit)
            for platform, (_, _, target, _) in release.PLATFORMS.items():
                base = f"vulcan-memory-mesh-{tag}-{platform}"
                archive = dist / (base + (".zip" if platform == "windows-x64" else ".tar.gz"))
                if platform == "windows-x64":
                    with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as handle:
                        for relative in release.WINDOWS_SIGNED_FILES:
                            handle.writestr(f"{base}/{relative}", (relative + "\n").encode())
                else:
                    archive.write_bytes(platform.encode())
                metadata = {
                    "manifest_schema": release.RELEASE_MANIFEST_SCHEMA,
                    "version": tag,
                    "commit": commit,
                    "platform": platform,
                    "target": target,
                    "archive": archive.name,
                    "storage_mode": "native",
                    "storage_profile": "all",
                    "capabilities": release.RELEASE_CAPABILITIES,
                    "sha256": release.digest(archive),
                }
                (dist / (base + ".json")).write_text(json.dumps(metadata), encoding="utf-8")
            manifest = {
                "protocol_version": release.MANIFEST_PROTOCOL_VERSION,
                "product": release.MANIFEST_PRODUCT,
                "tag": tag,
                "commit": commit,
                "artifacts": [
                    {
                        "platform": platform,
                        "filename": f"vulcan-memory-mesh-{tag}-{platform}{'.zip' if platform == 'windows-x64' else '.tar.gz'}",
                        "bytes": (dist / f"vulcan-memory-mesh-{tag}-{platform}{'.zip' if platform == 'windows-x64' else '.tar.gz'}").stat().st_size,
                        "sha256": release.digest(dist / f"vulcan-memory-mesh-{tag}-{platform}{'.zip' if platform == 'windows-x64' else '.tar.gz'}"),
                    }
                    for platform in release.PLATFORMS
                ],
            }
            (dist / release.RELEASE_MANIFEST_NAME).write_text(json.dumps(manifest) + "\n", encoding="utf-8")
            (dist / release.RELEASE_SIGNATURE_NAME).write_text(
                json.dumps({"version": 1, "key_id": "release-test", "signature": base64.b64encode(b"0" * 64).decode()}) + "\n",
                encoding="utf-8",
            )
            windows_archive = dist / f"vulcan-memory-mesh-{tag}-windows-x64.zip"
            (dist / release.WINDOWS_ATTESTATION_NAME).write_text(
                json.dumps(
                    {
                        "schema_version": 1,
                        "platform": "windows-x64",
                        "archive": windows_archive.name,
                        "archive_sha256": release.digest(windows_archive),
                        "certificate": {
                            "subject": release.SIGNING_POLICY["certificate_subject"],
                            "issuer": release.SIGNING_POLICY["certificate_issuer"],
                            "thumbprint": release.SIGNING_POLICY["certificate_thumbprint"],
                            "chain_status": "Valid",
                            "timestamp_status": "Valid",
                        },
                        "files": [
                            {
                                "path": relative,
                                "bytes": len((relative + "\n").encode()),
                                "sha256": hashlib.sha256((relative + "\n").encode()).hexdigest(),
                                "status": "Valid",
                            }
                            for relative in release.WINDOWS_SIGNED_FILES
                        ],
                    }
                ) + "\n",
                encoding="utf-8",
            )
            for platform in ("linux-x64", "linux-arm64"):
                (dist / f"vulcan-memory-mesh-{tag}-{platform}.tar.gz.asc").write_bytes(b"test signature")
            with patch.object(release, "verify_external_manifest_signature"), patch.object(release, "verify_gpg_assets"):
                with patch.dict(
                    "os.environ",
                    {
                        "VMM_CERTUM_SUBJECT": "CN=VMM Test Certum",
                        "VMM_CERTUM_ISSUER": "CN=VMM Test Certum Issuer",
                        "VMM_CERTUM_THUMBPRINT": "A" * 40,
                    },
                ):
                    self.assertEqual(len(release.verify_assets(dist, tag, commit)), 16)
                foreign = dist / "unrelated.txt"
                foreign.write_text("unrelated", encoding="utf-8")
                with patch.dict(
                    "os.environ",
                    {
                        "VMM_CERTUM_SUBJECT": "CN=VMM Test Certum",
                        "VMM_CERTUM_ISSUER": "CN=VMM Test Certum Issuer",
                        "VMM_CERTUM_THUMBPRINT": "A" * 40,
                    },
                ):
                    with self.assertRaisesRegex(ValueError, "Unexpected"):
                        release.verify_assets(dist, tag, commit)
                foreign.unlink()
                archive.write_bytes(b"modified after build")
                with patch.dict(
                    "os.environ",
                    {
                        "VMM_CERTUM_SUBJECT": "CN=VMM Test Certum",
                        "VMM_CERTUM_ISSUER": "CN=VMM Test Certum Issuer",
                        "VMM_CERTUM_THUMBPRINT": "A" * 40,
                    },
                ):
                    with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                        release.verify_assets(dist, tag, commit)

    def test_shell_and_path_inputs_are_rejected(self):
        """Reject version values that could escape asset names or alter shell syntax.
        拒绝可能逃逸产物名称或改变命令语义的版本值。
        """
        for tag in ("../v0.1.0", "v0.1.0;echo", "--help", "v01.1.0", "main"):
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                release.validate_identity(tag, "a" * 40)

    def test_release_staging_excludes_development_override_and_keeps_assets(self):
        """Stage the real checkout configs and verify the public package boundary.
        暂存当前检出的真实配置并验证公开发行包边界。
        """
        root = Path(__file__).resolve().parent.parent
        with tempfile.TemporaryDirectory() as directory:
            stage = Path(directory) / "package"
            release_configs = release.stage_release_configs(root, stage)
            self.assertNotIn("configs/config.yaml", release_configs)
            self.assertFalse((stage / "configs/config.yaml").exists())
            self.assertTrue((stage / "configs/base.yaml").is_file())
            self.assertTrue((stage / "configs/noise_rules/common.json").is_file())
            self.assertTrue((stage / "configs/pii_rules/common.json").is_file())
            self.assertTrue((stage / "configs/prompts/default_cn/postaction_l1_main.md").is_file())
            base_text = (stage / "configs/base.yaml").read_text(encoding="utf-8")
            self.assertEqual(base_text.count('  mode: "split"'), 1)
            staged_text = "\n".join(
                path.read_text(encoding="utf-8")
                for path in stage.rglob("*")
                if path.is_file()
            )
            self.assertNotIn("${DEEPSEEK_API_KEY}", staged_text)

    def test_release_config_allowlist_rejects_unknown_tracked_file(self):
        """Reject a newly tracked configuration until its release intent is reviewed.
        新增配置文件必须先经过发行审核，否则直接拒绝打包。
        """
        with self.assertRaisesRegex(ValueError, "Unapproved"):
            release.select_release_configs(
                ["configs/base.yaml", "configs/noise_rules/common.json", "configs/unknown.yaml"]
            )

    def test_runtime_staging_requires_complete_platform_dependencies(self):
        """Stage a complete fake platform and reject a missing controller dependency.
        暂存一份完整的模拟平台产物，并拒绝缺失 controller 依赖的发行包。
        """
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "output"
            stage = Path(directory) / "stage"
            platform = "windows-x64"
            support_relative = "native_lancedb-support/include/vmm_lancedb.h"
            for name in ("vmm-local.exe", "vmm-migrate.exe", "vmm-pii-tester.exe", "vldb-controller.exe"):
                path = output / "bin" / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(name.encode("ascii"))
            for name in (
                "manifest.json",
                "vldb_sqlite.dll",
                "vldb_lancedb.dll",
                "vmm_lancedb_native.dll",
            ):
                path = output / "libs" / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(name.encode("ascii"))
            support_path = output / "libs" / support_relative
            support_path.parent.mkdir(parents=True, exist_ok=True)
            support_path.write_bytes(b"validated native support")
            manifest = {"artifacts": [{"path": support_relative, "sha256": release.digest(support_path)}]}
            release.stage_runtime_artifacts(output, stage, platform, manifest)
            self.assertTrue((stage / "bin/vldb-controller.exe").is_file())
            self.assertTrue((stage / "libs/vldb_sqlite.dll").is_file())
            support_path.write_bytes(b"tampered native support")
            with self.assertRaisesRegex(ValueError, "digest mismatch"):
                release.stage_runtime_artifacts(output, Path(directory) / "tampered-support", platform, manifest)
            with self.assertRaisesRegex(ValueError, "Unsafe native artifact path"):
                release.stage_runtime_artifacts(
                    output,
                    Path(directory) / "unsafe-artifact",
                    platform,
                    {"artifacts": [{"path": "../escape.bin", "sha256": "0" * 64}]},
                )
            (output / "bin/vldb-controller.exe").unlink()
            with self.assertRaisesRegex(ValueError, "controller binary"):
                release.stage_runtime_artifacts(output, Path(directory) / "missing-controller", platform, manifest)

    def test_refreshes_native_manifest_after_signed_windows_library_changes(self):
        """Refresh only library_sha256 after signing and preserve native identity fields.
        模拟 Windows 签名改变动态库字节，只刷新 library_sha256 并保留原生身份字段。
        """
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "output"
            libraries = output / "libs"
            libraries.mkdir(parents=True)
            library = libraries / "vmm_lancedb_native.dll"
            library.write_bytes(b"unsigned native library")
            manifest = {
                "schema_version": 1,
                "abi_version": 1,
                "engine_version": "0.39.0",
                "target": "x86_64-pc-windows-msvc",
                "source_digest": "1" * 64,
                "cargo_lock_sha256": "2" * 64,
                "library_file": library.name,
                "library_sha256": release.digest(library),
                "artifacts": [{"path": "native_lancedb-support/include/vmm_lancedb.h", "sha256": "3" * 64}],
            }
            (libraries / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
            original_identity = {key: value for key, value in manifest.items() if key != "library_sha256"}
            library.write_bytes(b"unsigned native library plus Authenticode signature")
            actual_digest = release.refresh_signed_native_manifest(output, "windows-x64")
            refreshed = json.loads((libraries / "manifest.json").read_text(encoding="utf-8"))
            self.assertEqual(actual_digest, release.digest(library))
            self.assertEqual(refreshed["library_sha256"], actual_digest)
            self.assertEqual(
                {key: value for key, value in refreshed.items() if key != "library_sha256"},
                original_identity,
            )

    def test_windows_attestation_rechecks_actual_signer_identity(self):
        """Keep post-package attestation tied to signtool and observed certificate fields.
        保证打包后证明重新执行 signtool，并绑定实际读取的证书字段。
        """
        workflow_path = Path(__file__).resolve().parent.parent / ".github" / "workflows" / "release.yml"
        workflow = workflow_path.read_text(encoding="utf-8")
        start = workflow.index("- name: Write Windows Authenticode attestation")
        end = workflow.index("- uses: actions/upload-artifact", start)
        attestation = workflow[start:end]
        self.assertIn("$verifyOutput = & $signTool verify /pa /all /tw $target.Absolute 2>&1", attestation)
        self.assertIn("$fileSubject = [string]$signature.SignerCertificate.Subject", attestation)
        self.assertIn("$fileIssuer = [string]$signature.SignerCertificate.Issuer", attestation)
        self.assertIn("$fileThumbprint = ($signature.SignerCertificate.Thumbprint -replace '\\s', '').ToUpperInvariant()", attestation)
        self.assertIn("if ($fileSubject -ne $expectedSubject)", attestation)
        self.assertIn("if ($fileIssuer -ne $expectedIssuer)", attestation)
        self.assertIn("if ($fileThumbprint -ne $expectedThumbprint)", attestation)
        self.assertIn("subject = $attestedSubject", attestation)
        self.assertIn("issuer = $attestedIssuer", attestation)
        self.assertIn("thumbprint = $attestedThumbprint", attestation)
        self.assertNotIn("subject = $env:VMM_CERTUM_SUBJECT", attestation)
        self.assertNotIn("issuer = $env:VMM_CERTUM_ISSUER", attestation)
        self.assertNotIn("thumbprint = ($env:VMM_CERTUM_THUMBPRINT", attestation)

    def test_external_manifest_requires_exact_archive_digests_and_signature(self):
        """Reject a missing detached signature and a manifest digest mismatch.
        拒绝缺失分离式签名以及清单摘要不匹配的发行目录。
        """
        with tempfile.TemporaryDirectory() as directory:
            dist = Path(directory)
            tag, commit = "v0.1.0", "a" * 40
            archives = []
            artifacts = []
            for platform in release.PLATFORMS:
                suffix = ".zip" if platform == "windows-x64" else ".tar.gz"
                archive = dist / f"vulcan-memory-mesh-{tag}-{platform}{suffix}"
                archive.write_bytes(platform.encode("ascii"))
                archives.append(archive)
                artifacts.append(
                    {
                        "platform": platform,
                        "filename": archive.name,
                        "bytes": archive.stat().st_size,
                        "sha256": release.digest(archive),
                    }
                )
            manifest = {
                "protocol_version": 1,
                "product": "vmm",
                "tag": tag,
                "commit": commit,
                "artifacts": artifacts,
            }
            (dist / "manifest.json").write_text(json.dumps(manifest) + "\n", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "signature"):
                release.verify_external_manifest(dist, tag, commit, archives)
            (dist / "manifest.sig").write_text(
                json.dumps({
                    "version": 1,
                    "key_id": "vmm-test",
                    "signature": base64.b64encode(b"0" * 64).decode(),
                }) + "\n",
                encoding="utf-8",
            )
            with patch.object(release, "verify_external_manifest_signature") as verify_signature:
                release.verify_external_manifest(dist, tag, commit, archives)
                verify_signature.assert_called_once_with(dist / "manifest.json", dist / "manifest.sig")
            manifest["artifacts"][0]["sha256"] = "0" * 64
            (dist / "manifest.json").write_text(json.dumps(manifest) + "\n", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "SHA-256 mismatch"):
                release.verify_external_manifest(dist, tag, commit, archives)

    def test_external_manifest_signature_rejects_tampering_with_fixed_trust_root(self):
        """Run the Go verifier through release.py and reject a changed signature.
        通过 release.py 调用 Go 验证器，并拒绝被篡改的签名。
        """
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            manifest = root / "manifest.json"
            signature = root / "manifest.sig"
            manifest.write_bytes(b"test release manifest\n")
            signing_environment = {
                "TEST_PRIVATE": base64.b64encode(b"\x00" * 32).decode("ascii"),
                "TEST_KEY_ID": "vmm-2026-09-23-01",
            }
            subprocess.run(
                [
                    "go",
                    "run",
                    "./scripts/sign_manifest.go",
                    "sign",
                    "--manifest",
                    str(manifest),
                    "--signature",
                    str(signature),
                    "--private-key-env",
                    "TEST_PRIVATE",
                    "--key-id-env",
                    "TEST_KEY_ID",
                ],
                cwd=Path(__file__).resolve().parent.parent,
                env={**os.environ, **signing_environment},
                check=True,
                capture_output=True,
                text=True,
            )
            with patch.object(release, "VMM_RELEASE_PUBLIC_KEY_B64", "O2onvM62pC1io6jQKm8Nc2UyFXcd4kOmOsBIoYtZ2ik="):
                release.verify_external_manifest_signature(manifest, signature)
                envelope = json.loads(signature.read_text(encoding="utf-8"))
                envelope["signature"] = base64.b64encode(b"1" * 64).decode("ascii")
                signature.write_text(json.dumps(envelope) + "\n", encoding="utf-8")
                with self.assertRaisesRegex(ValueError, "Ed25519 verification failed"):
                    release.verify_external_manifest_signature(manifest, signature)


if __name__ == "__main__":
    unittest.main()
