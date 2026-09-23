// Build and sign VMM release manifests with Go's standard Ed25519 library.
// 使用 Go 标准库 Ed25519 构建并签名 VMM 发布清单。
// This file is a repository-local release trust-boundary tool and has no runtime dependencies.
// 本文件是仓库内的发行信任边界工具，不引入运行时依赖。
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// manifestProtocolVersion is the wire protocol consumed by manager/internal/manifest.
	// manifestProtocolVersion 是 manager/internal/manifest 消费的清单线协议版本。
	manifestProtocolVersion uint = 1

	// signatureVersion is the detached Ed25519 envelope version.
	// signatureVersion 是分离式 Ed25519 签名封装版本。
	signatureVersion uint = 1

	// manifestProduct identifies the VMM runtime release.
	// manifestProduct 标识 VMM 运行时发行物。
	manifestProduct = "vmm"

	// defaultPrivateKeyEnvironment is the only production signing-secret input.
	// defaultPrivateKeyEnvironment 是生产签名私钥唯一允许的输入环境变量。
	defaultPrivateKeyEnvironment = "VMM_RELEASE_ED25519_PRIVATE_KEY"

	// defaultKeyIDEnvironment identifies the public key trusted by the manager.
	// defaultKeyIDEnvironment 标识管理器信任的公钥 ID。
	defaultKeyIDEnvironment = "VMM_RELEASE_ED25519_KEY_ID"

	// trustedVMMKeyID is the committed VMM trust-root identifier used by the manager.
	// trustedVMMKeyID 是管理器提交的 VMM 信任根标识。
	trustedVMMKeyID = "vmm-2026-09-23-01"

	// manifestFileName is the public signed metadata filename uploaded with the archives.
	// manifestFileName 是随发行压缩包上传的公开签名元数据文件名。
	manifestFileName = "manifest.json"

	// signatureFileName is the detached signature filename uploaded with the manifest.
	// signatureFileName 是随清单上传的分离式签名文件名。
	signatureFileName = "manifest.sig"
)

var (
	// tagPattern rejects values that could change release asset names or command syntax.
	// tagPattern 拒绝可能改变发行资产名或命令语义的值。
	tagPattern = regexp.MustCompile(`^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)

	// commitPattern requires the immutable full Git SHA recorded by the workflow.
	// commitPattern 要求工作流记录完整且不可变的 Git SHA。
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

	// keyIDPattern keeps key IDs safe for logs, JSON, and trust-map lookup.
	// keyIDPattern 保证密钥 ID 可安全用于日志、JSON 和信任映射查找。
	keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// releasePlatforms is the closed platform set produced by the VMM workflow.
// releasePlatforms 是 VMM 工作流生成的封闭平台集合。
var releasePlatforms = []struct {
	// ID is the stable installer platform identifier.
	// ID 是安装器使用的稳定平台标识。
	ID string
	// Extension is the archive extension used by that platform.
	// Extension 是该平台使用的压缩包扩展名。
	Extension string
}{
	{ID: "windows-x64", Extension: ".zip"},
	{ID: "linux-x64", Extension: ".tar.gz"},
	{ID: "linux-arm64", Extension: ".tar.gz"},
	{ID: "macos-intel", Extension: ".tar.gz"},
	{ID: "macos-arm64", Extension: ".tar.gz"},
}

// releaseManifest is the exact protocol-v1 JSON document authenticated by Ed25519.
// releaseManifest 是由 Ed25519 认证的 protocol-v1 JSON 文档。
type releaseManifest struct {
	// ProtocolVersion selects the manifest parser contract.
	// ProtocolVersion 选择清单解析器契约。
	ProtocolVersion uint `json:"protocol_version"`
	// Product identifies the runtime product.
	// Product 标识运行时产品。
	Product string `json:"product"`
	// Tag records the immutable release tag.
	// Tag 记录不可变发行标签。
	Tag string `json:"tag"`
	// Commit records the exact source commit.
	// Commit 记录精确源代码提交。
	Commit string `json:"commit"`
	// Artifacts contains one archive for each supported platform.
	// Artifacts 包含每个支持平台的一个压缩包。
	Artifacts []releaseArtifact `json:"artifacts"`
}

// releaseArtifact records the size and digest of one downloadable VMM archive.
// releaseArtifact 记录一个可下载 VMM 压缩包的大小和摘要。
type releaseArtifact struct {
	// Platform is the stable platform identifier.
	// Platform 是稳定平台标识。
	Platform string `json:"platform"`
	// Filename is the archive basename downloaded by the manager.
	// Filename 是管理器下载的压缩包文件名。
	Filename string `json:"filename"`
	// Bytes is the exact archive byte count.
	// Bytes 是压缩包精确字节数。
	Bytes int64 `json:"bytes"`
	// SHA256 is the lowercase archive digest.
	// SHA256 是小写压缩包摘要。
	SHA256 string `json:"sha256"`
}

// signatureEnvelope is the exact detached-signature object consumed by the manager.
// signatureEnvelope 是管理器消费的精确分离式签名对象。
type signatureEnvelope struct {
	// Version identifies the detached-signature protocol.
	// Version 标识分离式签名协议。
	Version uint `json:"version"`
	// KeyID selects the trusted public key.
	// KeyID 选择受信任的公钥。
	KeyID string `json:"key_id"`
	// Signature is the standard Base64 Ed25519 signature.
	// Signature 是标准 Base64 编码的 Ed25519 签名。
	Signature string `json:"signature"`
}

// buildManifest scans exactly the five release archives and records their bytes and SHA-256 values.
// buildManifest 扫描精确的五个发行压缩包，并记录字节数与 SHA-256 值。
func buildManifest(dist string, tag string, commit string) ([]byte, error) {
	if !tagPattern.MatchString(tag) {
		return nil, fmt.Errorf("invalid release tag %q", tag)
	}
	if !commitPattern.MatchString(commit) {
		return nil, errors.New("commit must be a 40-character lowercase hexadecimal SHA-1")
	}
	root, err := regularDirectory(dist)
	if err != nil {
		return nil, err
	}
	artifacts := make([]releaseArtifact, 0, len(releasePlatforms))
	for _, platform := range releasePlatforms {
		filename := fmt.Sprintf("vulcan-memory-mesh-%s-%s%s", tag, platform.ID, platform.Extension)
		path := filepath.Join(root, filename)
		info, err := regularFile(path, "release archive")
		if err != nil {
			return nil, err
		}
		if info.Size() <= 0 {
			return nil, fmt.Errorf("release archive %s is empty", filename)
		}
		digest, err := sha256File(path)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, releaseArtifact{
			Platform: platform.ID,
			Filename: filename,
			Bytes:    info.Size(),
			SHA256:   digest,
		})
	}
	manifest := releaseManifest{
		ProtocolVersion: manifestProtocolVersion,
		Product:         manifestProduct,
		Tag:             tag,
		Commit:          commit,
		Artifacts:       artifacts,
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	return append(encoded, '\n'), nil
}

// regularDirectory verifies that the release directory is a real directory, not a reparse link.
// regularDirectory 验证发行目录是真实目录，而不是重解析点或符号链接。
func regularDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("release directory is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve release directory: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect release directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("release directory is not a real directory: %s", absolute)
	}
	return absolute, nil
}

// regularFile rejects symlinks and non-regular files before hashing an asset.
// regularFile 在计算资产摘要前拒绝符号链接与非普通文件。
func regularFile(path string, description string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect %s %s: %w", description, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file: %s", description, path)
	}
	return info, nil
}

// sha256File hashes a regular asset incrementally without loading it into memory.
// sha256File 增量计算普通资产摘要，不把整个文件载入内存。
func sha256File(path string) (string, error) {
	handle, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open asset %s: %w", path, err)
	}
	defer handle.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, handle); err != nil {
		return "", fmt.Errorf("hash asset %s: %w", path, err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// privateKeyFromEnvironment decodes only the configured CI secret and checks its internal public half.
// privateKeyFromEnvironment 只解码配置的 CI Secret，并校验私钥内置公钥部分。
func privateKeyFromEnvironment(environmentName string) (ed25519.PrivateKey, error) {
	value, ok := os.LookupEnv(environmentName)
	if !ok || strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("required signing secret %s is missing", environmentName)
	}
	value = strings.TrimSpace(value)
	decoded, err := decodeKey(value, environmentName)
	if err != nil {
		return nil, err
	}
	if len(decoded) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(decoded), nil
	}
	if len(decoded) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%s must decode to a 32-byte seed or 64-byte private key", environmentName)
	}
	privateKey := ed25519.PrivateKey(decoded)
	derivedPublic, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok || !bytes.Equal(derivedPublic, privateKey[ed25519.SeedSize:]) {
		return nil, fmt.Errorf("%s contains an inconsistent Ed25519 private key", environmentName)
	}
	return privateKey, nil
}

// decodeKey accepts strict hexadecimal or Base64 text while rejecting all other key encodings.
// decodeKey 接受严格十六进制或 Base64 文本，并拒绝其他密钥编码。
func decodeKey(value string, label string) ([]byte, error) {
	if decoded, err := hex.DecodeString(value); err == nil && (len(decoded) == ed25519.SeedSize || len(decoded) == ed25519.PrivateKeySize) {
		return decoded, nil
	}
	for _, decoder := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		decoded, err := decoder.DecodeString(value)
		if err == nil && (len(decoded) == ed25519.SeedSize || len(decoded) == ed25519.PrivateKeySize) {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("%s must decode to a 32-byte seed or 64-byte private key", label)
}

// keyIDFromEnvironment validates the stable public-key identifier used in the envelope.
// keyIDFromEnvironment 验证封装中使用的稳定公钥 ID。
func keyIDFromEnvironment(environmentName string) (string, error) {
	value, ok := os.LookupEnv(environmentName)
	if !ok || !keyIDPattern.MatchString(value) {
		return "", fmt.Errorf("required signing key ID %s is missing or invalid", environmentName)
	}
	if value != trustedVMMKeyID {
		return "", fmt.Errorf("signing key ID %q does not match the committed VMM trust root", value)
	}
	return value, nil
}

// writeNewFile atomically creates a new public release file without overwriting an existing path.
// writeNewFile 原子创建公开发行文件，不覆盖已经存在的路径。
func writeNewFile(path string, data []byte) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("refusing to overwrite existing output %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect output %s: %w", path, err)
	}
	parent := filepath.Dir(path)
	temporary, err := os.CreateTemp(parent, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o644); err != nil {
		return fmt.Errorf("set output permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync output: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close output: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("publish output: %w", err)
	}
	removeTemporary = false
	return nil
}

// signManifest signs exact manifest bytes and atomically writes the protocol-v1 envelope.
// signManifest 对清单原始字节签名，并原子写入 protocol-v1 封装。
func signManifest(manifestPath string, signaturePath string, privateEnvironment string, keyIDEnvironment string) ([]byte, error) {
	privateKey, err := privateKeyFromEnvironment(privateEnvironment)
	if err != nil {
		return nil, err
	}
	keyID, err := keyIDFromEnvironment(keyIDEnvironment)
	if err != nil {
		return nil, err
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	if len(manifestBytes) == 0 {
		return nil, errors.New("manifest is empty")
	}
	envelope := signatureEnvelope{
		Version:   signatureVersion,
		KeyID:     keyID,
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, manifestBytes)),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode signature: %w", err)
	}
	if err := writeNewFile(signaturePath, append(encoded, '\n')); err != nil {
		return nil, err
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("private key did not derive an Ed25519 public key")
	}
	return publicKey, nil
}

// decodeSignatureEnvelope strictly decodes the detached-signature wire object.
// decodeSignatureEnvelope 严格解析分离式签名线协议对象。
func decodeSignatureEnvelope(data []byte) (signatureEnvelope, []byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil {
		return signatureEnvelope{}, nil, fmt.Errorf("invalid signature JSON: %w", err)
	}
	if delimiter, ok := first.(json.Delim); !ok || delimiter != '{' {
		return signatureEnvelope{}, nil, errors.New("signature must be a JSON object")
	}
	fields := make(map[string]json.RawMessage, 3)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return signatureEnvelope{}, nil, fmt.Errorf("invalid signature key: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return signatureEnvelope{}, nil, errors.New("signature object key is not a string")
		}
		if _, exists := fields[key]; exists {
			return signatureEnvelope{}, nil, fmt.Errorf("signature contains duplicate key %q", key)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return signatureEnvelope{}, nil, fmt.Errorf("invalid signature field %q: %w", key, err)
		}
		fields[key] = raw
	}
	closing, err := decoder.Token()
	if err != nil {
		return signatureEnvelope{}, nil, fmt.Errorf("invalid signature closing delimiter: %w", err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return signatureEnvelope{}, nil, errors.New("signature does not close as an object")
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return signatureEnvelope{}, nil, errors.New("signature contains trailing JSON")
		}
		return signatureEnvelope{}, nil, fmt.Errorf("invalid trailing signature JSON: %w", err)
	}
	if len(fields) != 3 {
		return signatureEnvelope{}, nil, errors.New("signature has unexpected fields")
	}
	for _, required := range []string{"version", "key_id", "signature"} {
		if _, ok := fields[required]; !ok {
			return signatureEnvelope{}, nil, fmt.Errorf("signature is missing field %q", required)
		}
	}
	var envelope signatureEnvelope
	for key, destination := range map[string]any{
		"version":   &envelope.Version,
		"key_id":    &envelope.KeyID,
		"signature": &envelope.Signature,
	} {
		if err := json.Unmarshal(fields[key], destination); err != nil {
			return signatureEnvelope{}, nil, fmt.Errorf("signature field %q is invalid: %w", key, err)
		}
	}
	if envelope.Version != signatureVersion || !keyIDPattern.MatchString(envelope.KeyID) {
		return signatureEnvelope{}, nil, errors.New("signature envelope has invalid version or key ID")
	}
	if envelope.KeyID != trustedVMMKeyID {
		return signatureEnvelope{}, nil, fmt.Errorf("signature key ID %q does not match the committed VMM trust root", envelope.KeyID)
	}
	rawSignature, err := base64.StdEncoding.DecodeString(envelope.Signature)
	if err != nil || len(rawSignature) != ed25519.SignatureSize {
		return signatureEnvelope{}, nil, errors.New("signature is not a valid Ed25519 Base64 value")
	}
	return envelope, rawSignature, nil
}

// createAndSignRelease builds manifest.json from archives and signs it as manifest.sig.
// createAndSignRelease 从压缩包构建 manifest.json，并签名为 manifest.sig。
func createAndSignRelease(dist string, tag string, commit string, privateEnvironment string, keyIDEnvironment string) error {
	root, err := regularDirectory(dist)
	if err != nil {
		return err
	}
	manifestBytes, err := buildManifest(root, tag, commit)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(root, manifestFileName)
	signaturePath := filepath.Join(root, signatureFileName)
	if err := writeNewFile(manifestPath, manifestBytes); err != nil {
		return err
	}
	if _, err := signManifest(manifestPath, signaturePath, privateEnvironment, keyIDEnvironment); err != nil {
		_ = os.Remove(manifestPath)
		return err
	}
	return nil
}

// verifyManifest checks a signed manifest with a configured public key for local CI validation.
// verifyManifest 使用配置的公钥验证签名清单，供本地 CI 校验。
func verifyManifest(manifestPath string, signaturePath string, publicEnvironment string) error {
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	signatureBytes, err := os.ReadFile(signaturePath)
	if err != nil {
		return fmt.Errorf("read signature: %w", err)
	}
	envelope, rawSignature, err := decodeSignatureEnvelope(signatureBytes)
	if err != nil {
		return err
	}
	publicBytes, err := publicKeyFromEnvironment(publicEnvironment)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicBytes, manifestBytes, rawSignature) {
		return fmt.Errorf("Ed25519 verification failed for key ID %s", envelope.KeyID)
	}
	return nil
}

// publicKeyFromPrivateEnvironment derives a public trust key without exposing private material.
// publicKeyFromPrivateEnvironment 派生公钥信任根，不暴露私钥材料。
func publicKeyFromPrivateEnvironment(privateEnvironment string) (ed25519.PublicKey, error) {
	privateKey, err := privateKeyFromEnvironment(privateEnvironment)
	if err != nil {
		return nil, err
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("private key did not derive an Ed25519 public key")
	}
	return publicKey, nil
}

// publicKeyFromEnvironment loads one exact 32-byte public key from the verification environment.
// publicKeyFromEnvironment 从校验环境变量加载精确的 32 字节公钥。
func publicKeyFromEnvironment(environmentName string) (ed25519.PublicKey, error) {
	value, ok := os.LookupEnv(environmentName)
	if !ok || strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("required public key %s is missing", environmentName)
	}
	decoded, err := decodePublicKey(strings.TrimSpace(value), environmentName)
	if err != nil {
		return nil, err
	}
	return ed25519.PublicKey(decoded), nil
}

// decodePublicKey accepts strict hexadecimal or Base64 text for one public key.
// decodePublicKey 接受一个公钥的严格十六进制或 Base64 文本。
func decodePublicKey(value string, label string) ([]byte, error) {
	if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == ed25519.PublicKeySize {
		return decoded, nil
	}
	for _, decoder := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		decoded, err := decoder.DecodeString(value)
		if err == nil && len(decoded) == ed25519.PublicKeySize {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("%s must decode to a 32-byte public key", label)
}

// parseArguments validates the release command and keeps output paths fixed under dist.
// parseArguments 验证发行命令，并将输出路径固定在 dist 目录内。
func parseArguments(arguments []string) (string, string, string, string, string, string, error) {
	flags := flag.NewFlagSet("sign_manifest", flag.ContinueOnError)
	dist := flags.String("dist", "", "release asset directory")
	tag := flags.String("tag", "", "release tag")
	commit := flags.String("commit", "", "full lowercase Git commit SHA")
	privateEnvironment := flags.String("private-key-env", defaultPrivateKeyEnvironment, "environment variable containing the private key")
	keyIDEnvironment := flags.String("key-id-env", defaultKeyIDEnvironment, "environment variable containing the key ID")
	publicEnvironment := flags.String("public-key-env", "VMM_RELEASE_ED25519_PUBLIC_KEY", "environment variable containing the public key")
	if err := flags.Parse(arguments); err != nil {
		return "", "", "", "", "", "", err
	}
	if flags.NArg() != 0 || *dist == "" || *tag == "" || *commit == "" {
		return "", "", "", "", "", "", errors.New("manifest requires --dist, --tag, and --commit")
	}
	return *dist, *tag, *commit, *privateEnvironment, *keyIDEnvironment, *publicEnvironment, nil
}

// run dispatches manifest, sign, verify, and public operations with explicit failure status.
// run 分发 manifest、sign、verify 和 public 操作，并返回明确失败状态。
func run(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("usage: sign_manifest.go manifest|sign|verify|public [options]")
	}
	switch arguments[0] {
	case "manifest":
		dist, tag, commit, privateEnvironment, keyIDEnvironment, _, err := parseArguments(arguments[1:])
		if err != nil {
			return err
		}
		return createAndSignRelease(dist, tag, commit, privateEnvironment, keyIDEnvironment)
	case "sign":
		flags := flag.NewFlagSet("sign", flag.ContinueOnError)
		manifest := flags.String("manifest", "", "path to manifest.json")
		signature := flags.String("signature", "", "path to manifest.sig")
		privateEnvironment := flags.String("private-key-env", defaultPrivateKeyEnvironment, "environment variable containing the private key")
		keyIDEnvironment := flags.String("key-id-env", defaultKeyIDEnvironment, "environment variable containing the key ID")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *manifest == "" || *signature == "" {
			return errors.New("sign requires --manifest and --signature")
		}
		_, err := signManifest(*manifest, *signature, *privateEnvironment, *keyIDEnvironment)
		return err
	case "verify":
		flags := flag.NewFlagSet("verify", flag.ContinueOnError)
		manifest := flags.String("manifest", "", "path to manifest.json")
		signature := flags.String("signature", "", "path to manifest.sig")
		publicEnvironment := flags.String("public-key-env", "VMM_RELEASE_ED25519_PUBLIC_KEY", "environment variable containing the public key")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *manifest == "" || *signature == "" {
			return errors.New("verify requires --manifest and --signature")
		}
		return verifyManifest(*manifest, *signature, *publicEnvironment)
	case "public":
		flags := flag.NewFlagSet("public", flag.ContinueOnError)
		privateEnvironment := flags.String("private-key-env", defaultPrivateKeyEnvironment, "environment variable containing the private key")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("public does not accept positional arguments")
		}
		publicKey, err := publicKeyFromPrivateEnvironment(*privateEnvironment)
		if err != nil {
			return err
		}
		fmt.Println(base64.StdEncoding.EncodeToString(publicKey))
		return nil
	default:
		return fmt.Errorf("unsupported command %q", arguments[0])
	}
}

// main runs the release signer and never prints private key material.
// main 运行发行签名器，并且绝不输出私钥材料。
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "manifest signing failed: %v\n", err)
		os.Exit(1)
	}
}
