//go:build darwin

// service_darwin_test.go verifies launchd plist round-tripping and boot-policy persistence.
// service_darwin_test.go 用于验证 launchd plist 往返解析与开机策略持久化。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderLaunchdPlistPersistsConfigAndManualStart verifies launchd receives argument-array values without shell interpolation.
// TestRenderLaunchdPlistPersistsConfigAndManualStart 用于验证 launchd 通过参数数组接收配置路径，不经过 shell 插值。
func TestRenderLaunchdPlistPersistsConfigAndManualStart(t *testing.T) {
	binDir := filepath.Join(t.TempDir(), "VMM App", "bin")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content, err := renderLaunchdPlist(
		"VMM",
		filepath.Join(binDir, "vmm-local"),
		binDir,
		launchdRenderOptions{configPath: configPath, autoStart: false},
	)
	if err != nil {
		t.Fatalf("renderLaunchdPlist returned error: %v", err)
	}
	for _, expected := range []string{"<key>RunAtLoad</key>", "<key>KeepAlive</key>", "<false></false>", "<key>ProgramArguments</key>", "<string>-config</string>"} {
		if !strings.Contains(content, expected) {
			t.Fatalf("rendered plist missing %q:\n%s", expected, content)
		}
	}
	if strings.Count(content, "<false></false>") != 2 {
		t.Fatalf("manual launchd policy must disable both RunAtLoad and KeepAlive:\n%s", content)
	}

	plistPath := filepath.Join(t.TempDir(), "VMM.plist")
	if err := os.WriteFile(plistPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	spec, err := readLaunchdServiceSpec(plistPath)
	if err != nil {
		t.Fatalf("readLaunchdServiceSpec returned error: %v", err)
	}
	if spec.Label != "VMM" || spec.BinDir != binDir || spec.AutoStart {
		t.Fatalf("unexpected launchd spec: %#v", spec)
	}
	if spec.ConfigPath == "" || spec.Executable == "" {
		t.Fatalf("expected persisted executable and config path: %#v", spec)
	}
	if err := validateLaunchdServiceSpec(spec, "VMM", filepath.Join(binDir, "vmm-local"), binDir); err != nil {
		t.Fatalf("generated launchd identity rejected: %v", err)
	}
	foreign := spec
	foreign.Executable = filepath.Join(binDir, "foreign")
	if err := validateLaunchdServiceSpec(foreign, "VMM", filepath.Join(binDir, "vmm-local"), binDir); err == nil {
		t.Fatal("foreign executable should be rejected")
	}
	foreign = spec
	foreign.KeepAlive = true
	if err := validateLaunchdServiceSpec(foreign, "VMM", filepath.Join(binDir, "vmm-local"), binDir); err == nil {
		t.Fatal("mismatched launchd boot policy should be rejected")
	}
	unknownKey := strings.Replace(content, "</dict>", "<key>EnvironmentVariables</key><dict></dict></dict>", 1)
	if err := os.WriteFile(plistPath, []byte(unknownKey), 0o644); err != nil {
		t.Fatalf("write foreign plist: %v", err)
	}
	if _, err := readLaunchdServiceSpec(plistPath); err == nil {
		t.Fatal("unknown launchd key should be rejected before rewrite")
	}
	unknownRoot := strings.Replace(content, "</plist>", "<extra/>\n</plist>", 1)
	if err := os.WriteFile(plistPath, []byte(unknownRoot), 0o644); err != nil {
		t.Fatalf("write foreign root plist: %v", err)
	}
	if _, err := readLaunchdServiceSpec(plistPath); err == nil {
		t.Fatal("unknown launchd root element should be rejected before rewrite")
	}
}

// TestRenderLaunchdPlistPersistsExplicitUser verifies launchd stores and revalidates the selected local account.
// TestRenderLaunchdPlistPersistsExplicitUser 验证 launchd 会保存并重新校验选定的本机账户。
func TestRenderLaunchdPlistPersistsExplicitUser(t *testing.T) {
	identity, err := resolveServiceUser("root")
	if err != nil {
		t.Skipf("root account is unavailable in this test environment: %v", err)
	}
	binDir := filepath.Join(t.TempDir(), "bin")
	content, err := renderLaunchdPlist("VMM", filepath.Join(binDir, "vmm-local"), binDir, launchdRenderOptions{
		configPath: filepath.Join(t.TempDir(), "config.yaml"),
		autoStart:  true,
		user:       identity.Username,
	})
	if err != nil {
		t.Fatalf("render user plist: %v", err)
	}
	if !strings.Contains(content, "<key>UserName</key>") || !strings.Contains(content, "<string>"+identity.Username+"</string>") {
		t.Fatalf("rendered plist missing UserName:\n%s", content)
	}
	plistPath := filepath.Join(t.TempDir(), "VMM.plist")
	if err := os.WriteFile(plistPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write user plist: %v", err)
	}
	spec, err := readLaunchdServiceSpec(plistPath)
	if err != nil {
		t.Fatalf("read user plist: %v", err)
	}
	if spec.UserName != identity.Username {
		t.Fatalf("plist user = %q, want %q", spec.UserName, identity.Username)
	}
	if spec.StandardOutPath != "/private/var/log/vmmm/VMM/service-stdout.log" || spec.StandardErrorPath != "/private/var/log/vmmm/VMM/service-stderr.log" {
		t.Fatalf("selected account logs must be outside the immutable package: %#v", spec)
	}
	if err := validateLaunchdServiceSpec(spec, "VMM", filepath.Join(binDir, "vmm-local"), binDir); err != nil {
		t.Fatalf("generated user plist rejected: %v", err)
	}
	foreign := spec
	foreign.StandardOutPath = filepath.Join(filepath.Dir(binDir), "logs", "service-stdout.log")
	if err := validateLaunchdServiceSpec(foreign, "VMM", filepath.Join(binDir, "vmm-local"), binDir); err == nil {
		t.Fatal("selected-account plist must reject a package-root log path")
	}
}

// TestParseLaunchctlPrintStatusRequiresLivePID verifies launchd reports running only when state and PID agree.
// TestParseLaunchctlPrintStatusRequiresLivePID 验证只有状态与 PID 同时有效时才报告 launchd 正在运行。
func TestParseLaunchctlPrintStatusRequiresLivePID(t *testing.T) {
	status, err := parseLaunchctlPrintStatus("state = running\npid = 321\n")
	if err != nil {
		t.Fatalf("parse running launchctl output: %v", err)
	}
	if got := launchdStateName(status); got != "running" {
		t.Fatalf("running state = %q, want running", got)
	}
	status, err = parseLaunchctlPrintStatus("state = running\n")
	if err != nil {
		t.Fatalf("parse running output without pid: %v", err)
	}
	if got := launchdStateName(status); got != "unknown" {
		t.Fatalf("running without pid = %q, want unknown", got)
	}
	status, err = parseLaunchctlPrintStatus("state = exited\npid = 0\n")
	if err != nil {
		t.Fatalf("parse exited launchctl output: %v", err)
	}
	if got := launchdStateName(status); got != "stopped" {
		t.Fatalf("exited state = %q, want stopped", got)
	}
	if _, err := parseLaunchctlPrintStatus("state = running\npid = invalid\n"); err == nil {
		t.Fatal("invalid pid should fail closed")
	}
}

// TestRenderLaunchdStatusUsesStableKeys verifies macOS status matches the manager's line-oriented key-value contract.
// TestRenderLaunchdStatusUsesStableKeys 验证 macOS 状态符合管理器逐行键值对契约。
func TestRenderLaunchdStatusUsesStableKeys(t *testing.T) {
	want := "state=running\nauto_start=disabled\nuser=unknown\n"
	if got := renderLaunchdStatus("running", false); got != want {
		t.Fatalf("launchd status = %q, want %q", got, want)
	}
}

// TestParseLaunchctlPrintIdentityRejectsForeignLoadedJob verifies loaded same-label jobs are checked by path and argv.
// TestParseLaunchctlPrintIdentityRejectsForeignLoadedJob 验证已加载同名 job 会按路径和 argv 核对身份。
func TestParseLaunchctlPrintIdentityRejectsForeignLoadedJob(t *testing.T) {
	output := `system/VMM = {
	path = /Library/LaunchDaemons/VMM.plist
	program = /Applications/VMM/bin/vmm-local
	arguments = {
		/Applications/VMM/bin/vmm-local
		service
		run
		VMM
		-config
		/Users/test/vmm/config.yaml
	}
state = running
pid = 42
}`
	identity, err := parseLaunchctlPrintIdentity(output)
	if err != nil {
		t.Fatalf("parse launchctl identity: %v", err)
	}
	expected := []string{"/Applications/VMM/bin/vmm-local", "service", "run", "VMM", "-config", "/Users/test/vmm/config.yaml"}
	if err := validateLaunchdLoadedIdentity(identity, "/Library/LaunchDaemons/VMM.plist", expected); err != nil {
		t.Fatalf("matching launchd identity rejected: %v", err)
	}
	foreign := identity
	foreign.PlistPath = "/Library/LaunchDaemons/other.plist"
	if err := validateLaunchdLoadedIdentity(foreign, "/Library/LaunchDaemons/VMM.plist", expected); err == nil {
		t.Fatal("foreign plist path should be rejected")
	}
}

// TestLaunchdNotFoundRecognitionFailsClosed verifies empty launchctl failures are not treated as absence.
// TestLaunchdNotFoundRecognitionFailsClosed 验证空的 launchctl 失败不会被误判为未加载。
func TestLaunchdNotFoundRecognitionFailsClosed(t *testing.T) {
	if isLaunchdServiceNotFoundOutput("") {
		t.Fatal("empty launchctl output must not be treated as not-found")
	}
	if _, loaded, err := classifyLaunchdPrintResult("VMM", nil, fmt.Errorf("exit status 1")); err == nil || loaded {
		t.Fatalf("empty launchctl failure should fail closed: loaded=%v err=%v", loaded, err)
	}
	if _, loaded, err := classifyLaunchdPrintResult("VMM", nil, nil); err == nil || loaded {
		t.Fatalf("empty successful launchctl output should fail closed: loaded=%v err=%v", loaded, err)
	}
	if !isLaunchdServiceNotFoundOutput(`Could not find service "system/VMM": 113: Could not find specified service`) {
		t.Fatal("explicit launchctl not-found output should be recognized")
	}
	if _, loaded, err := classifyLaunchdPrintResult("VMM", []byte(`Could not find service "system/VMM": 113`), fmt.Errorf("exit status 113")); err != nil || loaded {
		t.Fatalf("explicit launchctl not-found should be absence: loaded=%v err=%v", loaded, err)
	}
	if isLaunchdServiceNotFoundOutput("launchctl permission denied") {
		t.Fatal("permission failure must not be treated as not-found")
	}
}

// TestParseLaunchctlPrintDisabledReadsExactLabel verifies status can reflect a persistent disable override.
// TestParseLaunchctlPrintDisabledReadsExactLabel 验证 status 能读取持久化的精确标签禁用覆盖。
func TestParseLaunchctlPrintDisabledReadsExactLabel(t *testing.T) {
	output := `disabled services = {
	"VMM" => true
	"other" => false
}`
	override, err := parseLaunchctlPrintDisabled(output, "VMM")
	if err != nil {
		t.Fatalf("parse disabled override: %v", err)
	}
	if !override.Found || !override.Disabled {
		t.Fatalf("unexpected disabled override: %#v", override)
	}
	override, err = parseLaunchctlPrintDisabled(`disabled services = {
	"VMM" => disabled
}`, "VMM")
	if err != nil || !override.Found || !override.Disabled {
		t.Fatalf("disabled token override = %#v, err=%v", override, err)
	}
	override, err = parseLaunchctlPrintDisabled(output, "missing")
	if err != nil {
		t.Fatalf("parse missing disabled override: %v", err)
	}
	if override.Found {
		t.Fatalf("missing label should not be reported: %#v", override)
	}
}
