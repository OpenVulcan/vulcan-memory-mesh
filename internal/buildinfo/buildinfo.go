// Package buildinfo exposes the build identity shared by managed runtime binaries and manifests.
// buildinfo 包用于暴露托管运行时二进制与清单共享的构建身份。
package buildinfo

import (
	"encoding/json"
	"io"
	"runtime"
)

const (
	// BuildID identifies the exact managed runtime family accepted by Vulcan Code.
	// BuildID 标识 Vulcan Code 接受的精确托管运行时族。
	BuildID = "vmm-runtime-v1"

	// ManagedContractVersion is the single-file managed manifest revision.
	// ManagedContractVersion 是单文件托管清单版本。
	ManagedContractVersion = 1

	// InferenceProtocolVersion is the protected loopback inference contract revision.
	// InferenceProtocolVersion 是受保护回环推理契约版本。
	InferenceProtocolVersion = 1
)

// SourceRevision is injected by the official build script and remains unknown for ad hoc builds.
// SourceRevision 由正式构建脚本注入，临时构建默认保持 unknown。
var SourceRevision = "unknown"

// SourceStateDigest is injected by the official build script for freshness diagnostics.
// SourceStateDigest 由正式构建脚本注入，用于新鲜度诊断。
var SourceStateDigest = "unknown"

// VersionDocument describes one packaged VMM executable without exposing credentials.
// VersionDocument 描述一个不暴露凭据的打包 VMM 可执行文件。
type VersionDocument struct {
	Name                     string `json:"name"`
	BuildID                  string `json:"build_id"`
	ManagedContractVersion   int    `json:"managed_contract_version"`
	InferenceProtocolVersion int    `json:"inference_protocol_version"`
	GoVersion                string `json:"go_version"`
	GOOS                     string `json:"goos"`
	GOARCH                   string `json:"goarch"`
	SourceRevision           string `json:"source_revision"`
	SourceStateDigest        string `json:"source_state_digest"`
}

// WriteVersionJSON writes one deterministic machine-readable build identity document.
// WriteVersionJSON 写出一份确定性的机器可读构建身份文档。
//
// # Parameters
// 参数
// `writer` receives the JSON document and `name` identifies the executable.
// `writer` 接收 JSON 文档，`name` 标识可执行文件。
//
// # Returns
// 返回值
// A serialization or writer failure.
// 序列化或写出失败。
func WriteVersionJSON(writer io.Writer, name string) error {
	document := VersionDocument{
		Name:                     name,
		BuildID:                  BuildID,
		ManagedContractVersion:   ManagedContractVersion,
		InferenceProtocolVersion: InferenceProtocolVersion,
		GoVersion:                runtime.Version(),
		GOOS:                     runtime.GOOS,
		GOARCH:                   runtime.GOARCH,
		SourceRevision:           SourceRevision,
		SourceStateDigest:        SourceStateDigest,
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(document)
}
