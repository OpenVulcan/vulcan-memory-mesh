#!/usr/bin/env python3
"""Package verified native builds and publish complete GitHub release sets.
将已验收的原生构建打包，并发布完整的 GitHub 发行集合。
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
import zipfile


# The release matrix is a closed set shared with the workflow's platform names.
# 发行矩阵使用与工作流平台名称一致的封闭集合。
PLATFORMS = {
    "windows-x64": ("windows", "amd64", "x86_64-pc-windows-msvc", "vmm_lancedb_native.dll"),
    "linux-x64": ("linux", "amd64", "x86_64-unknown-linux-gnu", "libvmm_lancedb_native.so"),
    "linux-arm64": ("linux", "arm64", "aarch64-unknown-linux-gnu", "libvmm_lancedb_native.so"),
    "macos-intel": ("darwin", "amd64", "x86_64-apple-darwin", "libvmm_lancedb_native.dylib"),
    "macos-arm64": ("darwin", "arm64", "aarch64-apple-darwin", "libvmm_lancedb_native.dylib"),
}

# Version input must be safe as a Git tag, asset basename and command argument.
# 版本输入必须能安全用于 Git 标签、产物文件名和命令参数。
TAG_PATTERN = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?")


def run(*args, env=None):
    """Run an argument-vector command and return stdout, raising on failure.
    通过参数数组执行命令并返回标准输出，失败时抛出错误。
    """
    return subprocess.run(args, check=True, text=True, capture_output=True, env=env).stdout.strip()


def digest(path):
    """Hash one file incrementally and return its SHA-256 hex digest.
    增量读取文件并返回 SHA-256 十六进制摘要。
    """
    with Path(path).open("rb") as handle:
        return hashlib.file_digest(handle, "sha256").hexdigest()


def validate_identity(tag, commit):
    """Reject unsafe version strings or noncanonical commit IDs before file or API work.
    在文件或接口操作前拒绝不安全版本字符串及非标准提交标识。
    """
    if not TAG_PATTERN.fullmatch(tag) or not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise ValueError("Expected a version tag and a full lowercase Git commit SHA")


def check_tests(path):
    """Require all four named native integration tests to pass without skips.
    要求四个指定原生集成测试全部通过，任何跳过都不能替代验收。
    """
    required = {
        "TestNativeLibraryABIAndErrorBoundary",
        "TestNativeLibraryRoundTrip",
        "TestNativeLibraryPendingSchemaRecovery",
        "TestPackagedNativeRuntimeUsesIsolatedNativeArtifacts",
    }
    passed = set()
    for line in Path(path).read_text(encoding="utf-8").splitlines():
        event = json.loads(line)
        if event.get("Action") == "fail":
            raise ValueError(f"Integration test failure: {event}")
        if event.get("Test") in required:
            if event["Action"] == "skip":
                raise ValueError(f"Required integration test skipped: {event['Test']}")
            if event["Action"] == "pass":
                passed.add(event["Test"])
    if passed != required:
        raise ValueError(f"Missing native acceptance results: {sorted(required - passed)}")


def package(tag, commit, platform):
    """Validate output metadata, stage owned files, test and archive one platform.
    校验输出元数据，暂存自有文件，验收并压缩一个平台发行包。
    """
    validate_identity(tag, commit)
    root = Path(__file__).resolve().parent.parent
    output = root / "output"
    goos, goarch, target, library = PLATFORMS[platform]
    if (root / "VERSION").read_text(encoding="utf-8").strip() != tag:
        raise ValueError("Release tag differs from VERSION")
    if run("git", "-C", str(root), "rev-parse", "HEAD") != commit:
        raise ValueError("Source checkout differs from the resolved tag commit")
    suffix = ".exe" if goos == "windows" else ""
    for name in ("vmm-local", "vmm-migrate"):
        identity = json.loads(run(str(output / "bin" / (name + suffix)), "-version-json"))
        for key, value in {"version": tag, "source_revision": commit, "goos": goos, "goarch": goarch}.items():
            if identity[key] != value:
                raise ValueError(f"{name}: {key} is {identity[key]!r}, expected {value!r}")
    manifest = json.loads((output / "libs/manifest.json").read_text(encoding="utf-8"))
    if manifest["target"] != target or manifest["library_file"] != library:
        raise ValueError("Native library target does not match release platform")
    if digest(output / "libs" / library) != manifest["library_sha256"]:
        raise ValueError("Native library digest mismatch")

    # Copy only known package content; never distribute output/database or local overrides.
    # 仅复制明确的发行内容，绝不分发 output/database 或本地覆盖配置。
    dist = root / "dist"
    dist.mkdir(exist_ok=True)
    basename = f"vulcan-memory-mesh-{tag}-{platform}"
    with tempfile.TemporaryDirectory(prefix="vmm-release-") as temporary:
        stage = Path(temporary) / basename
        (stage / "bin").mkdir(parents=True)
        for name in ("vmm-local", "vmm-migrate", "vmm-pii-tester"):
            shutil.copy2(output / "bin" / (name + suffix), stage / "bin")
        library_paths = ["manifest.json", library] + [item["path"] for item in manifest["artifacts"]]
        for relative in library_paths:
            source = output / "libs" / relative
            destination = stage / "libs" / relative
            if source.is_symlink() or not source.resolve().is_relative_to((output / "libs").resolve()):
                raise ValueError(f"Native artifact escapes library directory: {relative}")
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, destination)
        # The checkout is the authority for system configuration, not a reused local output tree.
        # 系统配置以当前源码检出为准，避免复用的本地 output 混入用户文件。
        tracked_configs = run("git", "-C", str(root), "ls-files", "configs").splitlines()
        for relative in tracked_configs:
            destination = stage / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(root / relative, destination)
        base = stage / "configs/base.yaml"
        config_text, replaced = re.subn(r'(?m)^(  mode:) "split"$', r'\1 "native"', base.read_text(encoding="utf-8"))
        if replaced != 1:
            raise ValueError("Expected exactly one source storage.mode default")
        base.write_text(config_text, encoding="utf-8")
        for name in ("VERSION", "LICENSE", "README.md"):
            shutil.copy2(root / name, stage / name)
        shutil.copy2(root / "docs/native-storage-guide_CN.md", stage / "NATIVE_STORAGE.md")
        shutil.copy2(root / "docs/github-release-guide_CN.md", stage / "RELEASE_GUIDE.md")
        # Verify the staged default configuration and library layout with a real gRPC process.
        # 使用真实 gRPC 进程验收暂存包的默认配置与动态库布局。
        acceptance_env = dict(os.environ, VMM_NATIVE_PACKAGED_ROOT=str(stage))
        print(run("go", "test", "./internal/app", "-run", "^TestPackagedNativeRuntimeUsesIsolatedNativeArtifacts$", "-count=1", env=acceptance_env))
        files = sorted(path for path in stage.rglob("*") if path.is_file())
        file_hashes = {path.relative_to(stage).as_posix(): digest(path) for path in files}
        receipt = {"version": tag, "commit": commit, "platform": platform, "target": target, "storage_mode": "native", "files": file_hashes}
        (stage / "release-manifest.json").write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
        extension = ".zip" if goos == "windows" else ".tar.gz"
        archive = dist / (basename + extension)
        if archive.exists():
            raise ValueError(f"Refusing to overwrite an existing local archive: {archive}")
        if goos == "windows":
            with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as handle:
                for path in sorted(stage.rglob("*")):
                    if path.is_file():
                        handle.write(path, path.relative_to(stage.parent))
            with zipfile.ZipFile(archive) as handle:
                if handle.testzip() is not None:
                    raise ValueError("Archive CRC validation failed")
        else:
            with tarfile.open(archive, "w:gz") as handle:
                handle.add(stage, arcname=basename)
            with tarfile.open(archive) as handle:
                for member in handle.getmembers():
                    if member.isfile() and member.name.startswith(basename + "/bin/") and not member.mode & 0o111:
                        raise ValueError(f"Packaged executable lacks execution permission: {member.name}")
        receipt["archive"] = archive.name
        receipt["sha256"] = digest(archive)
        (dist / (basename + ".json")).write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
        print(f"Packaged {archive.name}: {receipt['sha256']}")


def verify_assets(dist, tag, commit):
    """Return the exact five verified archives and metadata, refusing incomplete or foreign files.
    返回精确的五平台压缩包和元数据，拒绝缺失或外来文件。
    """
    validate_identity(tag, commit)
    assets = []
    checksums = []
    for platform, (_, _, target, _) in PLATFORMS.items():
        basename = f"vulcan-memory-mesh-{tag}-{platform}"
        metadata = dist / (basename + ".json")
        receipt = json.loads(metadata.read_text(encoding="utf-8"))
        archive = dist / (basename + (".zip" if platform == "windows-x64" else ".tar.gz"))
        if any(receipt[key] != expected for key, expected in {"version": tag, "commit": commit, "platform": platform, "target": target, "archive": archive.name, "storage_mode": "native"}.items()):
            raise ValueError(f"Release metadata mismatch: {metadata.name}")
        if digest(archive) != receipt["sha256"]:
            raise ValueError(f"Release checksum mismatch: {archive.name}")
        assets.extend((archive, metadata))
        checksums.extend(f"{digest(path)}  {path.name}" for path in (archive, metadata))
    expected_names = {path.name for path in assets} | {"SHA256SUMS"}
    if any(path.name not in expected_names for path in dist.iterdir()):
        raise ValueError("Unexpected files in the release asset directory")
    sums = dist / "SHA256SUMS"
    sums.write_text("\n".join(sorted(checksums)) + "\n", encoding="utf-8")
    return assets + [sums]


def publish(tag, commit):
    """Upload a validated five-platform set as a draft, verify downloads, then publish.
    将已校验的五平台产物上传为草稿，验证下载摘要后正式发布。
    """
    dist = Path(__file__).resolve().parent.parent / "dist"
    assets = verify_assets(dist, tag, commit)
    repo = os.environ["GH_REPO"]
    ref = json.loads(run("gh", "api", f"repos/{repo}/git/ref/tags/{tag}"))["object"]
    if ref["type"] == "tag":
        ref = json.loads(run("gh", "api", f"repos/{repo}/git/tags/{ref['sha']}"))["object"]
    if ref["type"] != "commit" or ref["sha"] != commit:
        raise ValueError("Remote tag moved during the release build")
    existing = subprocess.run(["gh", "api", f"repos/{repo}/releases/tags/{tag}"], text=True, capture_output=True)
    if existing.returncode == 0:
        release = json.loads(existing.stdout)
        if not release["draft"] or release["target_commitish"] != commit:
            raise ValueError("Refusing to modify an existing published or unrelated release")
        if any(asset["name"] not in {path.name for path in assets} for asset in release["assets"]):
            raise ValueError("Draft contains unrelated assets")
    elif "HTTP 404" in existing.stderr:
        notes = dist.parent / "docs/github-release-notes-v0.1.0_CN.md"
        args = ["gh", "release", "create", tag, "--verify-tag", "--target", commit, "--title", f"VulcanMemoryMesh {tag}", "--draft"]
        if tag == "v0.1.0":
            args += ["--notes-file", str(notes)]
        else:
            args += ["--generate-notes"]
        if "-" in tag:
            args += ["--prerelease"]
        run(*args)
    else:
        raise RuntimeError(existing.stderr)
    run("gh", "release", "upload", tag, *map(str, assets), "--clobber")
    # Publication happens only after GitHub serves every uploaded byte with its expected digest.
    # 仅当 GitHub 返回全部上传内容且摘要匹配后，才将草稿转为正式发行版。
    with tempfile.TemporaryDirectory(prefix="vmm-release-download-") as temporary:
        run("gh", "release", "download", tag, "--dir", temporary)
        downloaded = Path(temporary)
        if {path.name for path in downloaded.iterdir()} != {path.name for path in assets}:
            raise ValueError("Uploaded release asset set differs from the verified local set")
        for asset in assets:
            if digest(downloaded / asset.name) != digest(asset):
                raise ValueError(f"Uploaded release digest mismatch: {asset.name}")
    run("gh", "release", "edit", tag, "--draft=false", "--latest=" + ("false" if "-" in tag else "true"))
    print(run("gh", "release", "view", tag, "--json", "url", "--jq", ".url"))


def main():
    """Dispatch explicit package, publish, or acceptance-check operations from CLI arguments.
    根据命令行参数分发明确的打包、发布或验收检查操作。
    """
    parser = argparse.ArgumentParser(description=__doc__)
    subcommands = parser.add_subparsers(dest="command", required=True)
    for command in ("package", "publish"):
        sub = subcommands.add_parser(command)
        sub.add_argument("--tag", required=True)
        sub.add_argument("--commit", required=True)
        if command == "package":
            sub.add_argument("--platform", choices=PLATFORMS, required=True)
    subcommands.add_parser("check-tests").add_argument("log")
    args = parser.parse_args()
    if args.command == "check-tests":
        check_tests(args.log)
    elif args.command == "package":
        package(args.tag, args.commit, args.platform)
    else:
        publish(args.tag, args.commit)


if __name__ == "__main__":
    main()
