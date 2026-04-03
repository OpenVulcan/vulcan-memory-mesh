// payload_protection.go implements optional payload encryption helpers for runtime logs.
// payload_protection.go 用于实现运行时日志里可选的载荷加密助手。
package logx

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// payloadProtectionVersion identifies the current protected-log envelope format so future readers can evolve parsing without ambiguity.
// payloadProtectionVersion 用于标识当前受保护日志载荷的封装格式版本，便于后续演进时保持可区分性。
const payloadProtectionVersion = "v1"

// payloadProtectionAlgorithm identifies the symmetric algorithm used to seal protected payload snapshots.
// payloadProtectionAlgorithm 用于标识受保护载荷快照当前使用的对称加密算法。
const payloadProtectionAlgorithm = "AES-256-GCM"

// payloadProtector keeps the AEAD primitive and stable key identifier used by encrypted runtime log payloads.
// payloadProtector 用于保存受保护运行时日志载荷所需的 AEAD 原语和稳定密钥标识。
type payloadProtector struct {
	aead  cipher.AEAD
	keyID string
}

// protectedPayloadEnvelope stores one encrypted payload snapshot together with the metadata required to identify its format and key lineage.
// protectedPayloadEnvelope 用于保存一份加密后的载荷快照，以及识别其格式和密钥来源所需的元信息。
type protectedPayloadEnvelope struct {
	Version    string `json:"version"`
	Algorithm  string `json:"algorithm"`
	KeyID      string `json:"key_id"`
	Ciphertext string `json:"ciphertext"`
}

// newPayloadProtector builds one optional payload protector and quietly disables protection when the feature is off or the key cannot be parsed.
// newPayloadProtector 用于创建可选的载荷保护器；当功能关闭或密钥无法解析时，会静默禁用保护能力。
func newPayloadProtector(enabled bool, rawKey string) *payloadProtector {
	if !enabled {
		return nil
	}
	key, err := decodePayloadProtectionKey(rawKey)
	if err != nil {
		return nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(key)
	return &payloadProtector{
		aead:  aead,
		keyID: hex.EncodeToString(sum[:8]),
	}
}

// protect seals one plaintext payload into a compact JSON envelope whose ciphertext contains the nonce prefix needed for later decryption.
// protect 用于把一段明文载荷封装成紧凑 JSON 信封；其密文里会携带解密所需的 nonce 前缀。
func (p *payloadProtector) protect(plaintext string) (string, error) {
	if p == nil || p.aead == nil {
		return "", fmt.Errorf("payload protector is not initialized")
	}
	nonce := make([]byte, p.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate payload nonce: %w", err)
	}
	sealed := p.aead.Seal(nil, nonce, []byte(plaintext), nil)
	body := append(append([]byte(nil), nonce...), sealed...)
	envelope := protectedPayloadEnvelope{
		Version:    payloadProtectionVersion,
		Algorithm:  payloadProtectionAlgorithm,
		KeyID:      p.keyID,
		Ciphertext: base64.StdEncoding.EncodeToString(body),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("marshal protected payload envelope: %w", err)
	}
	return string(encoded), nil
}

// decodePayloadProtectionKey accepts raw 32-byte keys plus common hex/base64 encodings so operators can inject log keys through environment variables without manual preprocessing.
// decodePayloadProtectionKey 用于接受原始 32 字节密钥以及常见的 hex/base64 编码形式，方便运维直接通过环境变量注入日志密钥。
func decodePayloadProtectionKey(raw string) ([]byte, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("payload encryption key is empty")
	}
	if decoded, err := hex.DecodeString(trimmed); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(trimmed); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if len(trimmed) == 32 {
		return []byte(trimmed), nil
	}
	return nil, fmt.Errorf("payload encryption key must be 32 raw bytes, 64 hex chars, or base64 for 32 bytes")
}

// jsonMarshalPayload materializes one payload into a stable JSON string so both debug logs and encrypted logs observe the same serialized body.
// jsonMarshalPayload 用于把一份载荷物化成稳定 JSON 字符串，确保明文调试和加密日志看到的是同一份序列化结果。
func jsonMarshalPayload(payload any) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}
	return string(body), nil
}
