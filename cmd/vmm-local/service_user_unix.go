//go:build linux || darwin

// service_user_unix.go validates Unix service accounts against the effective VMM storage layout before registration.
// service_user_unix.go 在注册 Unix 服务前根据 VMM 有效存储布局校验服务账户。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/openvulcan/vmm/internal/app"
	"github.com/openvulcan/vmm/internal/config"
)

// validateServiceAccountStorage loads the same merged configuration used by runtime startup and checks target-account ownership before install.
// validateServiceAccountStorage 加载运行时启动使用的同一份合并配置，并在安装前检查目标账户的数据目录归属。
func validateServiceAccountStorage(exePath string, configPath string, account serviceUserIdentity) error {
	if strings.TrimSpace(configPath) == "" {
		return fmt.Errorf("-user requires an explicit -config path for storage ownership validation")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve service install working directory: %w", err)
	}
	layout, err := config.ResolvePromptLayout(exePath, cwd, configPath)
	if err != nil {
		return fmt.Errorf("resolve service configuration layout: %w", err)
	}
	cfg, err := config.LoadPaths(layout.ConfigPaths(), config.Config{})
	if err != nil {
		return fmt.Errorf("load service configuration for storage ownership: %w", err)
	}
	if err := validateServiceAccountRuntimePaths(exePath, configPath, layout, account); err != nil {
		return err
	}

	// Reuse the runtime's path and symlink preflights before adding account-specific ownership checks.
	// 先复用运行时的路径与符号链接预检，再增加目标账户归属检查。
	if err := app.PreflightLocalStorageLayout(cfg, layout); err != nil {
		return fmt.Errorf("preflight local storage for service account: %w", err)
	}
	if err := app.PreflightNativeStorageLayout(cfg, layout); err != nil {
		return fmt.Errorf("preflight native storage for service account: %w", err)
	}
	// File logging starts before the database adapters, so the selected account must be able to create its log root.
	// 文件日志先于数据库适配器启动，因此所选账户必须能够创建日志根目录。
	logDir, err := app.ResolveRuntimeLogDir(cfg, layout)
	if err != nil {
		return fmt.Errorf("resolve service log directory: %w", err)
	}
	if err := validateServiceStoragePath(logDir, account, true); err != nil {
		return fmt.Errorf("validate service log directory ownership: %w", err)
	}

	switch cfg.StorageMode() {
	case "split", "controller":
		root := strings.TrimSpace(cfg.Storage.LocalDataRoot)
		if root == "" {
			return fmt.Errorf("storage.local_data_root must be explicit when installing a service with -user")
		}
		if err := validateServiceStoragePath(root, account, true); err != nil {
			return fmt.Errorf("validate storage.local_data_root ownership: %w", err)
		}
		if err := validateServiceStoragePath(filepath.Join(root, "sqlite.db"), account, false); err != nil {
			return fmt.Errorf("validate SQLite storage ownership: %w", err)
		}
		if err := validateServiceStoragePath(filepath.Join(root, "lancedb"), account, true); err != nil {
			return fmt.Errorf("validate LanceDB storage ownership: %w", err)
		}
	case "native":
		artifactRoot := ""
		if !filepath.IsAbs(strings.TrimSpace(cfg.SQLite.Native.Path)) || !filepath.IsAbs(strings.TrimSpace(cfg.LanceDB.Native.Path)) {
			artifactRoot, err = serviceArtifactRoot(layout)
			if err != nil {
				return err
			}
		}
		sqlitePath, err := resolveServiceStoragePath(artifactRoot, cfg.SQLite.Native.Path)
		if err != nil {
			return fmt.Errorf("resolve native SQLite path: %w", err)
		}
		lancePath, err := resolveServiceStoragePath(artifactRoot, cfg.LanceDB.Native.Path)
		if err != nil {
			return fmt.Errorf("resolve native LanceDB path: %w", err)
		}
		if err := validateServiceStoragePath(filepath.Dir(sqlitePath), account, true); err != nil {
			return fmt.Errorf("validate native SQLite parent ownership: %w", err)
		}
		if err := validateServiceStoragePath(sqlitePath, account, false); err != nil {
			return fmt.Errorf("validate native SQLite ownership: %w", err)
		}
		if err := validateServiceStoragePath(lancePath, account, true); err != nil {
			return fmt.Errorf("validate native LanceDB ownership: %w", err)
		}
	default:
		// Combined or other non-local modes do not expose a local database path for this service check.
		// combined 或其他非本地模式没有可供本检查验证的本地数据库路径。
		return nil
	}
	return nil
}

// validateServiceAccountRuntimePaths checks configuration layers, the requested config root, and the packaged executable before registration.
// validateServiceAccountRuntimePaths 在注册前检查配置层、请求的配置根以及打包可执行文件。
func validateServiceAccountRuntimePaths(exePath string, configPath string, layout config.PromptLayout, account serviceUserIdentity) error {
	if err := validateServiceReadablePath(configPath, account, true); err != nil {
		return fmt.Errorf("validate service config root access: %w", err)
	}
	for _, path := range layout.ConfigPaths() {
		if err := validateServiceReadablePath(path, account, false); err != nil {
			return fmt.Errorf("validate config layer access %q: %w", path, err)
		}
	}
	if err := validateServiceAccountEnvCandidates(layout.ConfigPaths(), account); err != nil {
		return err
	}
	if err := validateServiceUserRuleTrees(layout.UserDir, account); err != nil {
		return err
	}
	if err := validateServiceExecutablePath(exePath, account); err != nil {
		return fmt.Errorf("validate service executable access: %w", err)
	}
	return nil
}

// validateServiceUserRuleTrees checks every user override asset that the runtime can discover below its three rule roots.
// validateServiceUserRuleTrees 检查运行时可能在三个用户覆盖规则根目录中发现的每个资产。
func validateServiceUserRuleTrees(userDir string, account serviceUserIdentity) error {
	for _, name := range []string{"prompts", "pii_rules", "noise_rules"} {
		root := filepath.Join(userDir, name)
		info, err := os.Lstat(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("service user rule root %q is unsafe", root)
		}
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("service user rule asset %q is a symlink", path)
			}
			if entry.IsDir() {
				inspection, err := inspectServicePathTraversal(path, account)
				if err != nil || !inspection.exists || !inspection.info.IsDir() || inspection.info.Mode().Perm()&serviceReadPermissionBits(account, inspection.info) == 0 {
					return fmt.Errorf("service account cannot read rule directory %q", path)
				}
				return nil
			}
			if err := validateServiceReadablePath(path, account, false); err != nil {
				return fmt.Errorf("service account cannot read rule asset %q: %w", path, err)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// validateServiceAccountEnvCandidates checks every existing .env candidate that config.LoadPaths can read.
// validateServiceAccountEnvCandidates 检查 config.LoadPaths 可能读取的每个现存 .env 候选文件。
func validateServiceAccountEnvCandidates(configPaths []string, account serviceUserIdentity) error {
	seen := make(map[string]struct{})
	for _, configPath := range configPaths {
		for _, candidate := range serviceDotEnvCandidates(configPath) {
			if _, duplicate := seen[candidate]; duplicate {
				continue
			}
			seen[candidate] = struct{}{}
			info, err := os.Lstat(candidate)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return fmt.Errorf("inspect .env candidate %q: %w", candidate, err)
			}
			if info.IsDir() {
				return fmt.Errorf(".env candidate %q is a directory", candidate)
			}
			if err := validateServiceReadablePath(candidate, account, false); err != nil {
				return fmt.Errorf("validate .env candidate access %q: %w", candidate, err)
			}
		}
	}
	return nil
}

// serviceDotEnvCandidates mirrors config.dotEnvCandidates without broadening the runtime search path.
// serviceDotEnvCandidates 对齐 config.dotEnvCandidates，绝不扩展运行时搜索路径。
func serviceDotEnvCandidates(configPath string) []string {
	candidates := make([]string, 0, 2)
	seen := make(map[string]struct{})
	addCandidate := func(path string) {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			return
		}
		cleaned := filepath.Clean(trimmed)
		if _, duplicate := seen[cleaned]; duplicate {
			return
		}
		seen[cleaned] = struct{}{}
		candidates = append(candidates, cleaned)
	}
	if strings.TrimSpace(configPath) == "" {
		return candidates
	}
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return candidates
	}
	configDir := filepath.Dir(absPath)
	if strings.EqualFold(filepath.Base(configDir), "configs") {
		addCandidate(filepath.Join(configDir, "..", ".env"))
	}
	addCandidate(filepath.Join(configDir, ".env"))
	return candidates
}

// validateServiceReadablePath checks traversal through every ancestor and read access to an existing configuration file or root directory.
// validateServiceReadablePath 检查穿越每一级祖先目录的权限，以及已有配置文件或根目录的读取权限。
func validateServiceReadablePath(path string, account serviceUserIdentity, allowDirectory bool) error {
	inspection, err := inspectServicePathTraversal(path, account)
	if err != nil {
		return err
	}
	if !inspection.exists {
		return fmt.Errorf("path %q does not exist", inspection.path)
	}
	if inspection.info.IsDir() {
		if !allowDirectory {
			return fmt.Errorf("path %q is a directory", inspection.path)
		}
		return nil
	}
	if !inspection.info.Mode().IsRegular() {
		return fmt.Errorf("file %q is not a regular file", inspection.path)
	}
	if inspection.info.Mode().Perm()&serviceReadPermissionBits(account, inspection.info) == 0 {
		return fmt.Errorf("file %q is not readable by the service account", inspection.path)
	}
	return nil
}

// validateServiceExecutablePath checks traversal and execute permission for the exact binary registered with the service manager.
// validateServiceExecutablePath 检查服务管理器注册的精确二进制路径及其祖先穿越和执行权限。
func validateServiceExecutablePath(path string, account serviceUserIdentity) error {
	inspection, err := inspectServicePathTraversal(path, account)
	if err != nil {
		return err
	}
	if !inspection.exists || !inspection.info.Mode().IsRegular() {
		return fmt.Errorf("executable path %q is not a regular file", inspection.path)
	}
	if inspection.info.Mode().Perm()&serviceExecutePermissionBits(account, inspection.info) == 0 {
		return fmt.Errorf("executable %q is not executable by the service account", inspection.path)
	}
	return nil
}

// serviceArtifactRoot returns the packaged root required to resolve relative native database paths exactly like runtime startup.
// serviceArtifactRoot 返回运行时解析相对原生数据库路径所需的打包根目录。
func serviceArtifactRoot(layout config.PromptLayout) (string, error) {
	systemDir := filepath.Clean(strings.TrimSpace(layout.SystemDir))
	if systemDir == "." || systemDir == "" || !filepath.IsAbs(systemDir) || !strings.EqualFold(filepath.Base(systemDir), "configs") {
		return "", fmt.Errorf("native service installation requires a packaged absolute configs directory")
	}
	if info, err := os.Stat(filepath.Join(systemDir, "base.yaml")); err != nil || info.IsDir() {
		return "", fmt.Errorf("native service installation requires the packaged base.yaml")
	}
	return filepath.Dir(systemDir), nil
}

// resolveServiceStoragePath follows the runtime rule for native paths without probing alternate locations.
// resolveServiceStoragePath 按运行时规则解析原生路径，不探测候选替代位置。
func resolveServiceStoragePath(root string, configured string) (string, error) {
	value := strings.TrimSpace(configured)
	if value == "" {
		return "", fmt.Errorf("storage path is empty")
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	return filepath.Clean(filepath.Join(root, value)), nil
}

// validateServiceStoragePath checks existing objects or the nearest creation parent for the selected account.
// validateServiceStoragePath 检查已有对象，或检查目标路径最近的可创建父目录是否属于选定账户。
func validateServiceStoragePath(path string, account serviceUserIdentity, wantDirectory bool) error {
	inspection, err := inspectServicePathTraversal(path, account)
	if err != nil {
		return err
	}
	if !inspection.exists {
		return validateServiceDirectoryCreation(inspection.parent, account)
	}
	if wantDirectory && !inspection.info.IsDir() {
		return fmt.Errorf("storage path %q is not a directory", inspection.path)
	}
	if !wantDirectory && inspection.info.IsDir() {
		return fmt.Errorf("storage path %q is a directory", inspection.path)
	}
	if !wantDirectory && !inspection.info.Mode().IsRegular() {
		return fmt.Errorf("storage path %q is not a regular file", inspection.path)
	}
	uid, _, err := serviceAccountIDs(account)
	if err != nil {
		return err
	}
	if err := validateServicePathOwner(inspection.info, uid, wantDirectory); err != nil {
		return fmt.Errorf("storage path %q: %w", inspection.path, err)
	}
	return nil
}

// servicePathInspection captures the result of a symlink-free ancestor walk.
// servicePathInspection 保存不跟随符号链接的逐级祖先检查结果。
type servicePathInspection struct {
	// path is the cleaned absolute path that was inspected.
	// path 是被检查的规范绝对路径。
	path string

	// info is the final object metadata when the path exists.
	// info 是路径存在时的最终对象元数据。
	info os.FileInfo

	// exists reports whether the final object exists.
	// exists 表示最终对象是否存在。
	exists bool

	// parent identifies the nearest existing parent when the final object is missing.
	// parent 在最终对象缺失时标识最近的现存父目录。
	parent serviceExistingDirectory
}

// serviceAccountIDs parses the exact numeric identity resolved for the selected local account.
// serviceAccountIDs 解析已选本地账户对应的精确数字身份。
func serviceAccountIDs(account serviceUserIdentity) (uint32, uint32, error) {
	uid, err := strconv.ParseUint(strings.TrimSpace(account.UID), 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid service account uid %q: %w", account.UID, err)
	}
	gid, err := strconv.ParseUint(strings.TrimSpace(account.GID), 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid service account gid %q: %w", account.GID, err)
	}
	return uint32(uid), uint32(gid), nil
}

// inspectServicePathTraversal checks every existing path component without following symlinks.
// inspectServicePathTraversal 逐个检查所有现存路径组件且绝不跟随符号链接。
func inspectServicePathTraversal(path string, account serviceUserIdentity) (servicePathInspection, error) {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return servicePathInspection{}, fmt.Errorf("path %q is not absolute", path)
	}
	if _, _, err := serviceAccountIDs(account); err != nil {
		return servicePathInspection{}, err
	}

	root := filepath.VolumeName(cleaned) + string(filepath.Separator)
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return servicePathInspection{}, fmt.Errorf("inspect filesystem root %q: %w", root, err)
	}
	if err := validateServiceDirectoryTraversal(root, rootInfo, account); err != nil {
		return servicePathInspection{}, err
	}
	if cleaned == root {
		return servicePathInspection{path: cleaned, info: rootInfo, exists: true}, nil
	}

	previousPath := root
	previousInfo := rootInfo
	current := root
	relative := strings.TrimPrefix(cleaned, root)
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return servicePathInspection{
					path:   cleaned,
					exists: false,
					parent: serviceExistingDirectory{path: previousPath, info: previousInfo},
				}, nil
			}
			return servicePathInspection{}, fmt.Errorf("inspect path component %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return servicePathInspection{}, fmt.Errorf("path component %q must not be a symlink", current)
		}
		isFinal := current == cleaned
		if !isFinal && !info.IsDir() {
			return servicePathInspection{}, fmt.Errorf("path component %q is not a directory", current)
		}
		if info.IsDir() {
			if err := validateServiceDirectoryTraversal(current, info, account); err != nil {
				return servicePathInspection{}, err
			}
		}
		if isFinal {
			return servicePathInspection{path: cleaned, info: info, exists: true}, nil
		}
		previousPath = current
		previousInfo = info
	}
	return servicePathInspection{}, fmt.Errorf("path %q has no inspectable final component", cleaned)
}

// servicePermissionBits selects owner, primary-group, or other permission bits for the target account.
// servicePermissionBits 按目标账户的所有者、主组或其他身份选择对应权限位。
func servicePermissionBits(account serviceUserIdentity, info os.FileInfo, ownerBits, groupBits, otherBits os.FileMode) os.FileMode {
	uid, gid, err := serviceAccountIDs(account)
	if err != nil {
		return 0
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	if stat.Uid == uid {
		return ownerBits
	}
	if stat.Gid == gid {
		return groupBits
	}
	return otherBits
}

// serviceReadPermissionBits returns the selected read bit for an existing file.
// serviceReadPermissionBits 返回现存文件按账户身份选择的读取权限位。
func serviceReadPermissionBits(account serviceUserIdentity, info os.FileInfo) os.FileMode {
	return servicePermissionBits(account, info, 0o400, 0o040, 0o004)
}

// serviceExecutePermissionBits returns the selected traversal or execute bit.
// serviceExecutePermissionBits 返回按账户身份选择的穿越或执行权限位。
func serviceExecutePermissionBits(account serviceUserIdentity, info os.FileInfo) os.FileMode {
	return servicePermissionBits(account, info, 0o100, 0o010, 0o001)
}

// serviceWriteExecutePermissionBits returns the selected directory creation bits.
// serviceWriteExecutePermissionBits 返回按账户身份选择的目录创建权限位。
func serviceWriteExecutePermissionBits(account serviceUserIdentity, info os.FileInfo) os.FileMode {
	return servicePermissionBits(account, info, 0o300, 0o030, 0o003)
}

// validateServiceDirectoryTraversal requires directory execute permission for every ancestor.
// validateServiceDirectoryTraversal 要求每个祖先目录都具备目标账户的执行穿越权限。
func validateServiceDirectoryTraversal(path string, info os.FileInfo, account serviceUserIdentity) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("path component %q is not a regular directory", path)
	}
	if info.Mode().Perm()&serviceExecutePermissionBits(account, info) == 0 {
		return fmt.Errorf("directory %q is not traversable by the service account", path)
	}
	return nil
}

// validateServiceDirectoryCreation requires the selected account to create a missing child in the nearest parent.
// validateServiceDirectoryCreation 要求目标账户能够在最近父目录中创建缺失子项。
func validateServiceDirectoryCreation(parent serviceExistingDirectory, account serviceUserIdentity) error {
	if err := validateServiceDirectoryTraversal(parent.path, parent.info, account); err != nil {
		return fmt.Errorf("creation parent %q: %w", parent.path, err)
	}
	selected := serviceWriteExecutePermissionBits(account, parent.info)
	if selected == 0 {
		return fmt.Errorf("creation parent %q has no readable Unix ownership metadata", parent.path)
	}
	if parent.info.Mode().Perm()&selected != selected {
		return fmt.Errorf("creation parent %q is not writable by the service account", parent.path)
	}
	return nil
}

// serviceExistingDirectory records the nearest existing directory used for missing storage paths.
// serviceExistingDirectory 保存缺失存储路径所使用的最近现存目录。
type serviceExistingDirectory struct {
	// path is the existing ancestor path.
	// path 是现存祖先目录路径。
	path string

	// info is the lstat result used for ownership and permission checks.
	// info 是用于归属与权限检查的 lstat 结果。
	info os.FileInfo
}

// validateServicePathOwner requires the selected account to own the object and retain owner access.
// validateServicePathOwner 要求选定账户拥有对象，并保留对应的所有者访问权限。
func validateServicePathOwner(info os.FileInfo, uid uint32, wantDirectory bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("read Unix owner identity")
	}
	if stat.Uid != uid {
		return fmt.Errorf("owner uid %d does not match service account uid %d", stat.Uid, uid)
	}
	if wantDirectory {
		if info.Mode().Perm()&0o700 != 0o700 {
			return fmt.Errorf("directory lacks owner read/write/execute permissions")
		}
		return nil
	}
	if info.Mode().Perm()&0o600 != 0o600 {
		return fmt.Errorf("file lacks owner read/write permissions")
	}
	return nil
}
