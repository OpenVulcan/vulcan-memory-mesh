//go:build linux

// service_linux_test.go verifies deterministic systemd rendering and status serialization.
// service_linux_test.go 用于验证 systemd 定义渲染与状态序列化的确定性。
package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// TestClassifySystemdUnitFileStateRejectsAmbiguousResults covers failed and empty enablement probes.
// TestClassifySystemdUnitFileStateRejectsAmbiguousResults 覆盖失败及空输出的自启状态查询。
func TestClassifySystemdUnitFileStateRejectsAmbiguousResults(t *testing.T) {
	for _, example := range []struct {
		name   string
		output string
		runErr error
		want   string
		bad    bool
	}{
		{name: "disabled exit", output: "disabled\n", runErr: &exec.ExitError{}, want: "disabled"},
		{name: "static exit", output: "static\n", runErr: &exec.ExitError{}, want: "static"},
		{name: "enabled", output: "enabled\n", want: "enabled"},
		{name: "empty success", bad: true},
		{name: "empty exit", runErr: &exec.ExitError{}, bad: true},
		{name: "unknown", output: "mystery\n", bad: true},
		{name: "enabled exit", output: "enabled\n", runErr: &exec.ExitError{}, bad: true},
		{name: "launch failure", output: "disabled\n", runErr: errors.New("spawn failed"), bad: true},
		{name: "multiple", output: "disabled\nwarning\n", runErr: &exec.ExitError{}, bad: true},
	} {
		t.Run(example.name, func(t *testing.T) {
			got, err := classifySystemdUnitFileState([]byte(example.output), example.runErr)
			if example.bad && err == nil || !example.bad && (err != nil || got != example.want) {
				t.Fatalf("classifySystemdUnitFileState() = %q, %v", got, err)
			}
		})
	}
}

// TestParseSystemdLoadedIdentity retains every field used to reject external definitions.
// TestParseSystemdLoadedIdentity 保留拒绝外部运行定义所需的全部 systemd 字段。
func TestParseSystemdLoadedIdentity(t *testing.T) {
	status := parseSystemdStatusOutput("LoadState=loaded\nFragmentPath=/etc/systemd/system/VMM.service\nDropInPaths=/etc/systemd/system/VMM.service.d/override.conf\nNeedDaemonReload=yes\n")
	if status.LoadState != "loaded" || status.FragmentPath != "/etc/systemd/system/VMM.service" ||
		status.DropInPaths == "" || status.NeedDaemonReload != "yes" {
		t.Fatalf("parseSystemdStatusOutput() = %+v", status)
	}
}

// TestRenderSystemdUnitPersistsConfigArgument verifies the service command line carries the registered absolute config root.
// TestRenderSystemdUnitPersistsConfigArgument 用于验证服务命令行会携带注册时的绝对配置根目录。
func TestRenderSystemdUnitPersistsConfigArgument(t *testing.T) {
	unit := renderSystemdUnit("VMM", "/opt/vmm local/bin/vmm-local", "/opt/vmm local/bin", "/srv/vmm config")
	for _, expected := range []string{
		`WorkingDirectory=/opt/vmm local/bin`,
		`ExecStart="/opt/vmm local/bin/vmm-local" "service" "run" "VMM" "-config" "/srv/vmm config"`,
	} {
		if !strings.Contains(unit, expected) {
			t.Fatalf("rendered unit missing %q:\n%s", expected, unit)
		}
	}
}

// TestRenderSystemdUnitPersistsExplicitUser verifies the Unix service account is stored in the unit and resolved during ownership checks.
// TestRenderSystemdUnitPersistsExplicitUser 验证 Unix 服务账户会写入 unit，并在所有权校验时重新解析。
func TestRenderSystemdUnitPersistsExplicitUser(t *testing.T) {
	identity, err := resolveServiceUser("root")
	if err != nil {
		t.Skipf("root account is unavailable in this test environment: %v", err)
	}
	unit := renderSystemdUnitWithUser("VMM", "/opt/vmm/bin/vmm-local", "/opt/vmm/bin", "/srv/vmm/config.yaml", identity.Username)
	if !strings.Contains(unit, "User="+identity.Username+"\n") {
		t.Fatalf("rendered unit missing explicit User directive:\n%s", unit)
	}
	parsed, err := parseSystemdUnitIdentity(unit, "VMM", "/opt/vmm/bin/vmm-local", "/opt/vmm/bin")
	if err != nil {
		t.Fatalf("generated user unit rejected: %v", err)
	}
	if parsed.User != identity.Username {
		t.Fatalf("parsed user = %q, want %q", parsed.User, identity.Username)
	}
}

// TestSystemdWorkingDirectoryKeepsDollarLiteral verifies directory encoding does not reuse ExecStart environment escaping.
// TestSystemdWorkingDirectoryKeepsDollarLiteral 验证工作目录编码不会复用 ExecStart 的环境变量转义。
func TestSystemdWorkingDirectoryKeepsDollarLiteral(t *testing.T) {
	binDir := "/opt/vmm$prod/bin"
	unit := renderSystemdUnit("VMM", binDir+"/vmm-local", binDir, "/srv/vmm/config.yaml")
	if !strings.Contains(unit, `WorkingDirectory=/opt/vmm$prod/bin`) {
		t.Fatalf("working directory changed dollar literal:\n%s", unit)
	}
	if err := validateSystemdUnitIdentity(unit, "VMM", binDir+"/vmm-local", binDir); err != nil {
		t.Fatalf("unit with dollar path rejected: %v", err)
	}
}

// TestValidateSystemdWorkingDirectoryRejectsDirectiveInjection verifies newline-bearing paths fail before unit writing.
// TestValidateSystemdWorkingDirectoryRejectsDirectiveInjection 验证含换行的路径会在写入 unit 前失败。
func TestValidateSystemdWorkingDirectoryRejectsDirectiveInjection(t *testing.T) {
	if err := validateSystemdWorkingDirectory("/opt/vmm\n[Service]"); err == nil {
		t.Fatal("newline-bearing working directory should be rejected")
	}
	if err := validateSystemdWorkingDirectory("/opt/vmm local/bin"); err != nil {
		t.Fatalf("space-bearing absolute path should be accepted: %v", err)
	}
}

// TestSystemdQuoteEscapesExpansionCharacters verifies unit paths cannot trigger systemd specifier or environment expansion.
// TestSystemdQuoteEscapesExpansionCharacters 用于验证 unit 路径不会触发 systemd specifier 或环境变量展开。
func TestSystemdQuoteEscapesExpansionCharacters(t *testing.T) {
	got := systemdQuote(`/srv/vmm/%data/$cache`)
	want := `"/srv/vmm/%%data/$$cache"`
	if got != want {
		t.Fatalf("systemdQuote(%q) = %q, want %q", `/srv/vmm/%data/$cache`, got, want)
	}
}

// TestRenderSystemdStatusUsesStableKeys verifies manager-facing status output contains explicit auto-start information.
// TestRenderSystemdStatusUsesStableKeys 用于验证管理器状态输出包含明确的自启动信息。
func TestRenderSystemdStatusUsesStableKeys(t *testing.T) {
	status := renderSystemdStatus(systemdStatus{
		ActiveState:   "active",
		SubState:      "running",
		UnitFileState: "enabled",
		User:          "root",
	})
	if status != "state=running\nsubstate=running\nauto_start=enabled\nuser=root\n" {
		t.Fatalf("unexpected status: %q", status)
	}
}

// TestValidateSystemdUnitIdentityRejectsForeignDirectives verifies an unrelated unit cannot be controlled by name collision.
// TestValidateSystemdUnitIdentityRejectsForeignDirectives 验证同名的非 VMM unit 不会被接管。
func TestValidateSystemdUnitIdentityRejectsForeignDirectives(t *testing.T) {
	content := renderSystemdUnit("VMM", "/opt/vmm/bin/vmm-local", "/opt/vmm/bin", "/srv/vmm/config.yaml")
	if err := validateSystemdUnitIdentity(content, "VMM", "/opt/vmm/bin/vmm-local", "/opt/vmm/bin"); err != nil {
		t.Fatalf("generated VMM unit rejected: %v", err)
	}
	foreign := content + "ExecStartPre=/usr/bin/foreign\n"
	if err := validateSystemdUnitIdentity(foreign, "VMM", "/opt/vmm/bin/vmm-local", "/opt/vmm/bin"); err == nil {
		t.Fatal("foreign ExecStartPre should be rejected")
	}
	wrongExecutable := renderSystemdUnit("VMM", "/opt/other/bin/daemon", "/opt/vmm/bin", "/srv/vmm/config.yaml")
	if err := validateSystemdUnitIdentity(wrongExecutable, "VMM", "/opt/vmm/bin/vmm-local", "/opt/vmm/bin"); err == nil {
		t.Fatal("foreign executable should be rejected")
	}
}

// TestLegacySystemdUnitIdentityRemainsManageable verifies the exact previously released unit is safe to migrate.
// TestLegacySystemdUnitIdentityRemainsManageable 验证精确历史 unit 仍可安全管理并迁移。
func TestLegacySystemdUnitIdentityRemainsManageable(t *testing.T) {
	name := "VMM"
	exePath := `/opt/vmm local/bin/vmm-local`
	binDir := `/opt/vmm local/bin`
	legacy := fmt.Sprintf(`[Unit]
Description=%s local gRPC runtime service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%s
ExecStart=%s service run %s
Restart=on-failure
RestartSec=5s
KillSignal=SIGTERM

[Install]
WantedBy=multi-user.target
`, name, legacySystemdQuote(binDir), legacySystemdQuote(exePath), name)
	identity, err := parseSystemdUnitIdentity(legacy, name, exePath, binDir)
	if err != nil {
		t.Fatalf("legacy unit rejected: %v", err)
	}
	if identity.User != "" {
		t.Fatalf("legacy unit user = %q, want empty", identity.User)
	}
	foreign := strings.Replace(legacy, "ExecStart=\"/opt/vmm local/bin/vmm-local\" service run VMM", "ExecStart=\"/usr/bin/foreign\" service run VMM", 1)
	if _, err := parseSystemdUnitIdentity(foreign, name, exePath, binDir); err == nil {
		t.Fatal("foreign legacy executable should be rejected")
	}
	withUser := strings.Replace(legacy, "Type=simple\n", "Type=simple\nUser=root\n", 1)
	if _, err := parseSystemdUnitIdentity(withUser, name, exePath, binDir); err == nil {
		t.Fatal("legacy unit with an unrecognized User directive should be rejected")
	}
}

// TestValidateSystemdInstallLoadStateFailsClosed verifies empty and unknown systemd responses cannot shadow a unit.
// TestValidateSystemdInstallLoadStateFailsClosed 验证空响应和未知状态不会允许覆盖同名 unit。
func TestValidateSystemdInstallLoadStateFailsClosed(t *testing.T) {
	for _, status := range []systemdStatus{{}, {LoadState: "mystery"}} {
		if err := validateSystemdInstallLoadState(status); err == nil {
			t.Fatalf("load state %q should fail closed", status.LoadState)
		}
	}
	if err := validateSystemdInstallLoadState(systemdStatus{LoadState: "not-found"}); err != nil {
		t.Fatalf("not-found should permit a fresh install: %v", err)
	}
	if err := validateSystemdInstallLoadState(systemdStatus{LoadState: "loaded"}); err != nil {
		t.Fatalf("loaded should continue to fragment identity validation: %v", err)
	}
}

// TestSystemdStateNameNormalizesNativeStates verifies only active maps to running for manager decisions.
// TestSystemdStateNameNormalizesNativeStates 验证只有 active 会映射为 running 供管理器决策。
func TestSystemdStateNameNormalizesNativeStates(t *testing.T) {
	cases := map[string]string{
		"active":        "running",
		"inactive":      "stopped",
		"failed":        "failed",
		"activating":    "start-pending",
		"deactivating":  "stop-pending",
		"unknown-state": "unknown",
	}
	for input, want := range cases {
		if got := systemdStateName(input); got != want {
			t.Fatalf("systemdStateName(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestParseSystemdStatusOutputIgnoresPropertyOrder verifies systemctl output remains order-independent.
// TestParseSystemdStatusOutputIgnoresPropertyOrder 用于验证 systemctl 输出解析不依赖属性顺序。
func TestParseSystemdStatusOutputIgnoresPropertyOrder(t *testing.T) {
	status := parseSystemdStatusOutput("UnitFileState=disabled\nFragmentPath=/usr/lib/systemd/system/VMM.service\nSubState=dead\nActiveState=inactive\n")
	if status.ActiveState != "inactive" || status.SubState != "dead" || status.UnitFileState != "disabled" {
		t.Fatalf("unexpected parsed status: %#v", status)
	}
	if status.FragmentPath != "/usr/lib/systemd/system/VMM.service" {
		t.Fatalf("unexpected fragment path: %q", status.FragmentPath)
	}
}
