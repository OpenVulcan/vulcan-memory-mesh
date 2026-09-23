// local_storage_layout_windows_test.go verifies Windows ACL isolation for explicit local storage roots.
// local_storage_layout_windows_test.go 验证 Windows 显式本地存储根目录的 ACL 隔离。
//go:build windows

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvulcan/vmm/internal/config"
	"golang.org/x/sys/windows"
)

// TestWindowsPrivateDirectoryRejectsBroadDACLWithoutMutation proves existing broad ACLs fail closed and remain unchanged.
// TestWindowsPrivateDirectoryRejectsBroadDACLWithoutMutation 验证已有宽泛 ACL 会安全拒绝且不会被修改。
func TestWindowsPrivateDirectoryRejectsBroadDACLWithoutMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create data root: %v", err)
	}
	if err := setWindowsTestDACL(root, "D:P(A;OICI;FA;;;WD)"); err != nil {
		t.Fatalf("set broad DACL: %v", err)
	}
	before := readWindowsTestDACL(t, root)
	if err := ensurePrivateDirectory(root); err == nil {
		t.Fatal("expected broad DACL rejection")
	}
	after := readWindowsTestDACL(t, root)
	if before != after {
		t.Fatalf("existing DACL changed after rejection: before=%q after=%q", before, after)
	}
}

// TestWindowsPrivateDirectorySetsProtectedDACLForNewDirectory proves newly created roots receive a protected allow-list.
// TestWindowsPrivateDirectorySetsProtectedDACLForNewDirectory 验证新建根目录会获得受保护的允许列表。
func TestWindowsPrivateDirectorySetsProtectedDACLForNewDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	if err := ensurePrivateDirectoryTree(root, 0o700); err != nil {
		t.Fatalf("set and validate private DACL: %v", err)
	}
	sddl := readWindowsTestDACL(t, root)
	if !strings.Contains(sddl, "D:P") {
		t.Fatalf("new data root DACL is not protected: %q", sddl)
	}
	currentSID, err := currentWindowsUserSID()
	if err != nil {
		t.Fatalf("read current user SID: %v", err)
	}
	for _, trustee := range []string{"SY", "BA", currentSID.String()} {
		// Windows renders the built-in local Administrator SID as the LA alias in canonical SDDL.
		// Windows 会在规范 SDDL 中把内置本机管理员 SID 显示为 LA 别名。
		present := strings.Contains(sddl, trustee)
		if trustee == currentSID.String() && strings.HasSuffix(trustee, "-500") {
			present = present || strings.Contains(sddl, ";;;LA)")
		}
		if !present {
			t.Fatalf("new data root DACL is missing trustee %q: %q", trustee, sddl)
		}
	}
	for _, broadTrustee := range []string{"WD", "BU", "AU", "AN"} {
		if strings.Contains(sddl, broadTrustee) {
			t.Fatalf("new data root DACL contains broad trustee %q: %q", broadTrustee, sddl)
		}
	}
}

// TestEnsurePrivateDirectoryTreeCreatesProtectedDACLUnderPublicParent proves a public parent cannot make new children public.
// TestEnsurePrivateDirectoryTreeCreatesProtectedDACLUnderPublicParent 验证公共父目录不会使新建子目录继承为公共权限。
func TestEnsurePrivateDirectoryTreeCreatesProtectedDACLUnderPublicParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "public-parent")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := setWindowsTestDACL(parent, "D:P(A;OICI;FA;;;WD)"); err != nil {
		t.Fatalf("set public parent DACL: %v", err)
	}
	target := filepath.Join(parent, "nested", "data")
	if err := ensurePrivateDirectoryTree(target, 0o700); err != nil {
		t.Fatalf("create protected directory tree: %v", err)
	}
	for _, path := range []string{filepath.Dir(target), target} {
		sddl := readWindowsTestDACL(t, path)
		if !strings.Contains(sddl, "D:P") || strings.Contains(sddl, "WD") {
			t.Fatalf("new directory inherited public DACL: path=%q sddl=%q", path, sddl)
		}
	}
}

// TestWindowsPrivateDirectoryRejectsInsufficientOrDeniedCurrentAccess prevents ACLs that cannot perform database writes.
// TestWindowsPrivateDirectoryRejectsInsufficientOrDeniedCurrentAccess 拒绝无法执行数据库写入的不足或拒绝当前用户权限。
func TestWindowsPrivateDirectoryRejectsInsufficientOrDeniedCurrentAccess(t *testing.T) {
	currentSID, err := currentWindowsUserSID()
	if err != nil {
		t.Fatalf("read current user SID: %v", err)
	}
	sid := currentSID.String()
	cases := []struct {
		name string
		sddl string
	}{
		{name: "read-only", sddl: fmt.Sprintf("D:P(A;OICI;FR;;;%s)", sid)},
		{name: "required-access-denied", sddl: fmt.Sprintf("D:P(D;OICI;FA;;;%s)(A;OICI;FA;;;%s)", sid, sid)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "data")
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatalf("create data root: %v", err)
			}
			defer func() {
				if err := setWindowsTestDACL(root, "D:P(A;OICI;FA;;;WD)"); err != nil {
					t.Logf("restore cleanup DACL: %v", err)
				}
			}()
			if err := setWindowsTestDACL(root, testCase.sddl); err != nil {
				t.Fatalf("set test DACL: %v", err)
			}
			if err := ensurePrivateDirectory(root); err == nil {
				t.Fatalf("expected %s ACL rejection", testCase.name)
			}
		})
	}
}

// TestPreflightLocalStorageLayoutRejectsBroadWindowsDACL proves preflight checks an existing root without repairing it.
// TestPreflightLocalStorageLayoutRejectsBroadWindowsDACL 验证预检只读检查已有根目录并拒绝宽泛 Windows ACL。
func TestPreflightLocalStorageLayoutRejectsBroadWindowsDACL(t *testing.T) {
	promptLayout, _ := packagedPromptLayout(t)
	root := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create data root: %v", err)
	}
	if err := setWindowsTestDACL(root, "D:P(A;OICI;FA;;;WD)"); err != nil {
		t.Fatalf("set broad DACL: %v", err)
	}
	before := readWindowsTestDACL(t, root)
	cfg := config.DefaultLocal()
	cfg.Storage.Mode = "split"
	cfg.Storage.LocalDataRoot = root
	if err := PreflightLocalStorageLayout(cfg, promptLayout); err == nil {
		t.Fatal("expected broad Windows DACL rejection")
	}
	after := readWindowsTestDACL(t, root)
	if before != after {
		t.Fatalf("preflight changed existing DACL: before=%q after=%q", before, after)
	}
	if _, err := os.Stat(filepath.Join(root, "lancedb")); !os.IsNotExist(err) {
		t.Fatalf("preflight created lancedb directory: %v", err)
	}
}

// setWindowsTestDACL installs a test-only protected DACL through the same Windows API used by runtime checks.
// setWindowsTestDACL 使用与运行时检查相同的 Windows API 写入仅供测试的受保护 DACL。
func setWindowsTestDACL(path, sddl string) error {
	securityDescriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := securityDescriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	)
}

// readWindowsTestDACL returns the current directory DACL in SDDL form for mutation checks.
// readWindowsTestDACL 以 SDDL 形式返回当前目录 DACL，用于验证检查过程没有修改已有权限。
func readWindowsTestDACL(t *testing.T, path string) string {
	t.Helper()
	securityDescriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("read directory DACL: %v", err)
	}
	if securityDescriptor == nil {
		t.Fatal("directory security descriptor is empty")
	}
	return securityDescriptor.String()
}
