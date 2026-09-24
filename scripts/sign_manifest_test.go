// Test the VMM release-manifest signer without network access or production secrets.
// 在无网络和无生产密钥的条件下测试 VMM 发行清单签名器。
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSigningSeedHex = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"

// TestCreateAndSignReleaseWritesProtocolV1 verifies the five archive records and detached envelope.
// TestCreateAndSignReleaseWritesProtocolV1 验证五个平台记录和分离式签名封装。
func TestCreateAndSignReleaseWritesProtocolV1(t *testing.T) {
	dist := t.TempDir()
	for _, platform := range releasePlatforms {
		filename := "vulcan-memory-mesh-v1.2.3-" + platform.ID + platform.Extension
		if err := os.WriteFile(filepath.Join(dist, filename), []byte(platform.ID), 0o644); err != nil {
			t.Fatalf("write archive: %v", err)
		}
	}
	seed := mustTestHex(t, testSigningSeedHex)
	privateKey := ed25519.NewKeyFromSeed(seed)
	t.Setenv("TEST_VMM_PRIVATE", base64.StdEncoding.EncodeToString(seed))
	t.Setenv("TEST_VMM_KEY_ID", trustedVMMKeyID)
	t.Setenv("TEST_VMM_PUBLIC", base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)))
	if err := createAndSignRelease(dist, "v1.2.3", strings.Repeat("a", 40), "TEST_VMM_PRIVATE", "TEST_VMM_KEY_ID"); err != nil {
		t.Fatalf("createAndSignRelease() failed: %v", err)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(dist, manifestFileName))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest releaseManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.ProtocolVersion != manifestProtocolVersion || manifest.Product != manifestProduct || len(manifest.Artifacts) != 5 {
		t.Fatalf("manifest identity = %#v", manifest)
	}
	if err := verifyManifest(filepath.Join(dist, manifestFileName), filepath.Join(dist, signatureFileName), "TEST_VMM_PUBLIC"); err != nil {
		t.Fatalf("verifyManifest() failed: %v", err)
	}
}

// TestCreateAndSignReleaseRejectsMissingSecret verifies that publication cannot proceed without a secret.
// TestCreateAndSignReleaseRejectsMissingSecret 验证缺少密钥时发行流程会拒绝继续。
func TestCreateAndSignReleaseRejectsMissingSecret(t *testing.T) {
	dist := t.TempDir()
	for _, platform := range releasePlatforms {
		filename := "vulcan-memory-mesh-v1.2.3-" + platform.ID + platform.Extension
		if err := os.WriteFile(filepath.Join(dist, filename), []byte(platform.ID), 0o644); err != nil {
			t.Fatalf("write archive: %v", err)
		}
	}
	t.Setenv("TEST_VMM_PRIVATE_MISSING", "")
	t.Setenv("TEST_VMM_KEY_ID", trustedVMMKeyID)
	if err := createAndSignRelease(dist, "v1.2.3", strings.Repeat("a", 40), "TEST_VMM_PRIVATE_MISSING", "TEST_VMM_KEY_ID"); err == nil {
		t.Fatal("createAndSignRelease() accepted a missing private key")
	}
	if _, err := os.Stat(filepath.Join(dist, manifestFileName)); !os.IsNotExist(err) {
		t.Fatalf("manifest exists after failed signing, stat error = %v", err)
	}
}

// TestCreateAndSignReleaseRefusesOverwrite protects an existing signed release from accidental replacement.
// TestCreateAndSignReleaseRefusesOverwrite 保护已有签名发行物，避免意外覆盖。
func TestCreateAndSignReleaseRefusesOverwrite(t *testing.T) {
	dist := t.TempDir()
	for _, platform := range releasePlatforms {
		filename := "vulcan-memory-mesh-v1.2.3-" + platform.ID + platform.Extension
		if err := os.WriteFile(filepath.Join(dist, filename), []byte(platform.ID), 0o644); err != nil {
			t.Fatalf("write archive: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dist, manifestFileName), []byte("existing\n"), 0o644); err != nil {
		t.Fatalf("write existing manifest: %v", err)
	}
	t.Setenv("TEST_VMM_PRIVATE", base64.StdEncoding.EncodeToString(mustTestHex(t, testSigningSeedHex)))
	t.Setenv("TEST_VMM_KEY_ID", trustedVMMKeyID)
	if err := createAndSignRelease(dist, "v1.2.3", strings.Repeat("a", 40), "TEST_VMM_PRIVATE", "TEST_VMM_KEY_ID"); err == nil {
		t.Fatal("createAndSignRelease() overwrote an existing manifest")
	}
}

// TestDecodeSignatureEnvelopeRejectsDuplicateKeys protects the signed wire format from JSON ambiguity.
// TestDecodeSignatureEnvelopeRejectsDuplicateKeys 保护签名线协议，拒绝 JSON 重复键歧义。
func TestDecodeSignatureEnvelopeRejectsDuplicateKeys(t *testing.T) {
	duplicate := []byte(`{"version":1,"version":1,"key_id":"vmm-test","signature":""}`)
	if _, _, err := decodeSignatureEnvelope(duplicate); err == nil {
		t.Fatal("decodeSignatureEnvelope() accepted duplicate keys")
	}
}

// TestKeyIDMismatchFailsClosed prevents a valid private key from publishing under an untrusted ID.
// TestKeyIDMismatchFailsClosed 防止有效私钥以不受信任的 ID 发布。
func TestKeyIDMismatchFailsClosed(t *testing.T) {
	t.Setenv("TEST_VMM_WRONG_KEY_ID", "vmm-test")
	if _, err := keyIDFromEnvironment("TEST_VMM_WRONG_KEY_ID"); err == nil {
		t.Fatal("keyIDFromEnvironment() accepted an untrusted key ID")
	}
	if _, _, err := decodeSignatureEnvelope([]byte(`{"version":1,"key_id":"vmm-test","signature":""}`)); err == nil {
		t.Fatal("decodeSignatureEnvelope() accepted an untrusted key ID")
	}
}

// mustTestHex decodes deterministic fixture bytes and fails the test on invalid input.
// mustTestHex 解码确定性测试字节，输入无效时终止测试。
func mustTestHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode test seed: %v", err)
	}
	return decoded
}
