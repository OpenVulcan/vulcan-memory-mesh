// Package buildinfo exposes the build identity shared by standalone VMM binaries.
// buildinfo 包用于暴露独立 VMM 二进制共享的构建身份。
package buildinfo

import (
	"encoding/json"
	"io"
	"runtime"
)

// Version is injected from the repository VERSION file by formal builds.
// Version 由正式构建从仓库 VERSION 文件注入，临时构建保持 dev。
var Version = "dev"

// SourceRevision is injected by the official build script and remains unknown for ad hoc builds.
// SourceRevision 由正式构建脚本注入，临时构建默认保持 unknown。
var SourceRevision = "unknown"

// SourceStateDigest is injected by the official build script for freshness diagnostics.
// SourceStateDigest 由正式构建脚本注入，用于新鲜度诊断。
var SourceStateDigest = "unknown"

// CompiledCapabilities describes native support compiled into the executable; it does not claim that a native library is loaded.
// CompiledCapabilities 描述编译进可执行文件的原生支持，不声称原生动态库当前已经加载。
type CompiledCapabilities struct {
	Scope         string                          `json:"scope"`
	NativeSQLite  NativeSQLiteCompiledCapability  `json:"native_sqlite"`
	NativeLanceDB NativeLanceDBCompiledCapability `json:"native_lancedb"`
}

// NativeSQLiteCompiledCapability records the SQLite driver and lexical tokenizer versions available at build time.
// NativeSQLiteCompiledCapability 记录构建时可用的 SQLite 驱动与词法分词版本。
type NativeSQLiteCompiledCapability struct {
	Driver               string `json:"driver"`
	DriverVersion        string `json:"driver_version"`
	GSEModule            string `json:"gse_module"`
	GSEVersion           string `json:"gse_version"`
	TokenizerAlgorithm   string `json:"tokenizer_algorithm"`
	NormalizationVersion string `json:"normalization_version"`
}

// NativeLanceDBCompiledCapability records the checked-in LanceDB ABI contract and its external-library requirement.
// NativeLanceDBCompiledCapability 记录仓库约定的 LanceDB ABI 契约及其外部库要求。
type NativeLanceDBCompiledCapability struct {
	ABIVersion              int    `json:"abi_version"`
	EngineVersion           string `json:"engine_version"`
	RequiresExternalLibrary bool   `json:"requires_external_library"`
}

// CompiledNativeCapabilities is the single static capability declaration embedded in every standalone binary.
// CompiledNativeCapabilities 是每个独立二进制内嵌的静态原生能力声明。
var CompiledNativeCapabilities = CompiledCapabilities{
	Scope: "compiled",
	NativeSQLite: NativeSQLiteCompiledCapability{
		Driver:               "modernc.org/sqlite",
		DriverVersion:        "v1.59.0",
		GSEModule:            "github.com/go-ego/gse",
		GSEVersion:           "v1.0.2",
		TokenizerAlgorithm:   "gse-pretokenize-v1",
		NormalizationVersion: "normalize-whitespace-v1",
	},
	NativeLanceDB: NativeLanceDBCompiledCapability{
		ABIVersion:              1,
		EngineVersion:           "0.39.0",
		RequiresExternalLibrary: true,
	},
}

// VersionDocument describes one packaged VMM executable without exposing credentials.
// VersionDocument 描述一个不暴露凭据的打包 VMM 可执行文件。
type VersionDocument struct {
	Name              string               `json:"name"`
	Version           string               `json:"version"`
	GoVersion         string               `json:"go_version"`
	GOOS              string               `json:"goos"`
	GOARCH            string               `json:"goarch"`
	SourceRevision    string               `json:"source_revision"`
	SourceStateDigest string               `json:"source_state_digest"`
	Capabilities      CompiledCapabilities `json:"capabilities"`
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
		Name:              name,
		Version:           Version,
		GoVersion:         runtime.Version(),
		GOOS:              runtime.GOOS,
		GOARCH:            runtime.GOARCH,
		SourceRevision:    SourceRevision,
		SourceStateDigest: SourceStateDigest,
		Capabilities:      CompiledNativeCapabilities,
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(document)
}
