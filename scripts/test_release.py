"""Verify that release gates reject skipped acceptance and incomplete or tampered assets.
验证发行门禁会拒绝跳过验收、缺失产物及被篡改的文件。
"""

import json
from pathlib import Path
import tempfile
import unittest

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
                archive.write_bytes(platform.encode())
                metadata = {"version": tag, "commit": commit, "platform": platform, "target": target, "archive": archive.name, "storage_mode": "native", "sha256": release.digest(archive)}
                (dist / (base + ".json")).write_text(json.dumps(metadata), encoding="utf-8")
            self.assertEqual(len(release.verify_assets(dist, tag, commit)), 11)
            foreign = dist / "unrelated.txt"
            foreign.write_text("unrelated", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "Unexpected"):
                release.verify_assets(dist, tag, commit)
            foreign.unlink()
            archive.write_bytes(b"modified after build")
            with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                release.verify_assets(dist, tag, commit)

    def test_shell_and_path_inputs_are_rejected(self):
        """Reject version values that could escape asset names or alter shell syntax.
        拒绝可能逃逸产物名称或改变命令语义的版本值。
        """
        for tag in ("../v0.1.0", "v0.1.0;echo", "--help", "v01.1.0", "main"):
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                release.validate_identity(tag, "a" * 40)


if __name__ == "__main__":
    unittest.main()
