// local_storage_acl_windows.go applies and verifies a private DACL for local database directories.
// local_storage_acl_windows.go 为本地数据库目录设置并验证私有 DACL。
//go:build windows

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// privateDirectoryDeleteChildAccess is the directory-specific delete-child bit omitted by x/sys constants.
// privateDirectoryDeleteChildAccess 是 x/sys 常量未导出的目录专用删除子项权限位。
const privateDirectoryDeleteChildAccess windows.ACCESS_MASK = 0x00000040

// privateDirectoryRequiredAccess covers database read/write, child creation, traversal, and cleanup rights.
// privateDirectoryRequiredAccess 覆盖数据库读写、子项创建、遍历与清理所需权限。
const privateDirectoryRequiredAccess windows.ACCESS_MASK = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.FILE_TRAVERSE | windows.DELETE | privateDirectoryDeleteChildAccess

// ensurePrivateDirectoryTree creates each missing directory with a protected DACL in the CreateDirectoryW call.
// ensurePrivateDirectoryTree 在 CreateDirectoryW 调用中为每个缺失目录原子设置受保护 DACL。
func ensurePrivateDirectoryTree(path string, _ os.FileMode) error {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("resolve private storage directory %q: %w", path, err)
	}
	ancestor := absolute
	var missing []string
	for {
		if _, statErr := os.Lstat(ancestor); statErr == nil {
			if err := validateWindowsStorageDirectory(ancestor); err != nil {
				return fmt.Errorf("validate private storage ancestor %q: %w", ancestor, err)
			}
			break
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("inspect private storage ancestor %q: %w", ancestor, statErr)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return fmt.Errorf("private storage directory %q has no existing ancestor", absolute)
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = parent
	}
	if len(missing) == 0 {
		return nil
	}

	securityDescriptor, err := newWindowsPrivateDirectorySecurityDescriptor()
	if err != nil {
		return err
	}
	securityAttributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: securityDescriptor,
	}
	for index := len(missing) - 1; index >= 0; index-- {
		if err := validateWindowsStorageDirectory(ancestor); err != nil {
			return fmt.Errorf("validate private storage parent %q: %w", ancestor, err)
		}
		child := filepath.Join(ancestor, missing[index])
		name, err := windows.UTF16PtrFromString(child)
		if err != nil {
			return fmt.Errorf("encode private storage directory %q: %w", child, err)
		}
		createErr := windows.CreateDirectory(name, securityAttributes)
		if createErr != nil && !errors.Is(createErr, windows.ERROR_ALREADY_EXISTS) {
			return fmt.Errorf("create private storage directory %q: %w", child, createErr)
		}
		if err := validateWindowsStorageDirectory(child); err != nil {
			return fmt.Errorf("validate private storage directory %q: %w", child, err)
		}
		if err := validateWindowsPrivateDirectoryACL(child); err != nil {
			return fmt.Errorf("validate private storage DACL for %q: %w", child, err)
		}
		ancestor = child
	}
	return nil
}

// validatePrivateDirectory verifies a protected DACL without changing an existing directory.
// validatePrivateDirectory 验证受保护 DACL，且不修改已有目录。
func validatePrivateDirectory(path string) error {
	if err := validateWindowsStorageDirectory(path); err != nil {
		return err
	}
	if err := validateWindowsPrivateDirectoryACL(path); err != nil {
		return fmt.Errorf("validate private storage DACL for %q: %w", path, err)
	}
	return nil
}

// validateWindowsStorageDirectory rejects final symlinks, reparse points, and non-directories before ACL access.
// validateWindowsStorageDirectory 在读取 ACL 前拒绝最终符号链接、重解析点和非目录。
func validateWindowsStorageDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat storage.local_data_root %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("storage.local_data_root %q is not a directory", path)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encode storage.local_data_root %q: %w", path, err)
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil {
		return fmt.Errorf("read storage.local_data_root attributes %q: %w", path, err)
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("storage.local_data_root %q is a reparse point", path)
	}
	return nil
}

// validateWindowsPrivateDirectoryACL rejects any DACL trustee outside the current user, SYSTEM, and Administrators.
// validateWindowsPrivateDirectoryACL 拒绝当前用户、SYSTEM 和管理员之外的任何 DACL 主体。
func validateWindowsPrivateDirectoryACL(path string) error {
	securityDescriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read directory DACL: %w", err)
	}
	if securityDescriptor == nil {
		return errors.New("directory security descriptor is empty")
	}
	dacl, _, err := securityDescriptor.DACL()
	if err != nil || dacl == nil {
		if err != nil {
			return fmt.Errorf("read directory DACL entries: %w", err)
		}
		return errors.New("directory has no DACL")
	}
	if dacl.AceCount == 0 {
		return errors.New("directory DACL has no entries")
	}
	control, _, err := securityDescriptor.Control()
	if err != nil {
		return fmt.Errorf("read directory DACL protection: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("directory DACL is inheritable")
	}
	currentSID, err := currentWindowsUserSID()
	if err != nil {
		return err
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("create SYSTEM SID: %w", err)
	}
	administratorsSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return fmt.Errorf("create Administrators SID: %w", err)
	}
	currentGranted := false
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return fmt.Errorf("read directory DACL entry %d: %w", index, err)
		}
		if ace == nil {
			return fmt.Errorf("directory DACL entry %d is empty", index)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE && ace.Header.AceType != windows.ACCESS_DENIED_ACE_TYPE {
			return fmt.Errorf("directory DACL entry %d uses unsupported ACE type %d", index, ace.Header.AceType)
		}
		entrySID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !entrySID.IsValid() {
			return fmt.Errorf("directory DACL entry %d contains an invalid SID", index)
		}
		if !entrySID.Equals(currentSID) && !entrySID.Equals(systemSID) && !entrySID.Equals(administratorsSID) {
			return fmt.Errorf("directory DACL entry %d grants or denies an unapproved SID %s", index, entrySID.String())
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			if ace.Mask&privateDirectoryRequiredAccess != 0 {
				return fmt.Errorf("directory DACL entry %d denies required access for %s", index, entrySID.String())
			}
			continue
		}
		if !entrySID.Equals(currentSID) {
			continue
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if ace.Mask&privateDirectoryRequiredAccess != privateDirectoryRequiredAccess {
			return fmt.Errorf("directory DACL entry %d grants insufficient access for current user", index)
		}
		currentGranted = true
	}
	if !currentGranted && !currentSID.Equals(systemSID) && !currentSID.Equals(administratorsSID) {
		return errors.New("directory DACL does not contain the current user")
	}
	return nil
}

// currentWindowsUserSID returns the SID attached to the current process token.
// currentWindowsUserSID 返回当前进程令牌绑定的用户 SID。
func currentWindowsUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read current Windows user SID: %w", err)
	}
	if user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return nil, errors.New("current Windows user SID is invalid")
	}
	return user.User.Sid, nil
}

// newWindowsPrivateDirectorySecurityDescriptor builds the protected allow-list used for atomic directory creation.
// newWindowsPrivateDirectorySecurityDescriptor 构造原子创建目录时使用的受保护允许列表。
func newWindowsPrivateDirectorySecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	currentSID, err := currentWindowsUserSID()
	if err != nil {
		return nil, err
	}
	sddl := fmt.Sprintf("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;%s)", currentSID.String())
	securityDescriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("build private directory security descriptor: %w", err)
	}
	return securityDescriptor, nil
}
