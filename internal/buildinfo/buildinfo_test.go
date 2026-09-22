// buildinfo_test.go verifies version output exposes static native capability contracts without runtime-loading claims.
// buildinfo_test.go 用于验证版本输出暴露静态原生能力契约，而不声称运行时已加载动态库。
package buildinfo

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestWriteVersionJSONIncludesCompiledNativeCapabilities verifies the machine-readable version document carries the checked-in native contracts.
// TestWriteVersionJSONIncludesCompiledNativeCapabilities 验证机器可读版本文档包含仓库约定的原生能力契约。
func TestWriteVersionJSONIncludesCompiledNativeCapabilities(t *testing.T) {
	var output bytes.Buffer
	if err := WriteVersionJSON(&output, "vmm-test"); err != nil {
		t.Fatalf("write version JSON: %v", err)
	}
	var document VersionDocument
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatalf("decode version JSON: %v", err)
	}
	if document.Capabilities.Scope != "compiled" {
		t.Fatalf("capability scope = %q", document.Capabilities.Scope)
	}
	if document.Capabilities.NativeSQLite.Driver != "modernc.org/sqlite" || document.Capabilities.NativeSQLite.DriverVersion != "v1.59.0" {
		t.Fatalf("SQLite compiled capability = %+v", document.Capabilities.NativeSQLite)
	}
	if document.Capabilities.NativeSQLite.GSEModule != "github.com/go-ego/gse" || document.Capabilities.NativeSQLite.GSEVersion != "v1.0.2" {
		t.Fatalf("GSE compiled capability = %+v", document.Capabilities.NativeSQLite)
	}
	if document.Capabilities.NativeSQLite.TokenizerAlgorithm != "gse-pretokenize-v1" || document.Capabilities.NativeSQLite.NormalizationVersion != "normalize-whitespace-v1" {
		t.Fatalf("tokenizer capability = %+v", document.Capabilities.NativeSQLite)
	}
	lance := document.Capabilities.NativeLanceDB
	if lance.ABIVersion != 1 || lance.EngineVersion != "0.39.0" || !lance.RequiresExternalLibrary {
		t.Fatalf("LanceDB compiled capability = %+v", lance)
	}
}
