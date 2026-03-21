// loader.go implements configuration and prompt loading.
// loader.go 用于实现配置与提示词加载。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RequiredScenes lists the prompt scene files that every valid prompt bundle must provide.
// RequiredScenes 用于列出每个有效提示词包都必须提供的场景文件。
var RequiredScenes = []string{
	"extract_intent.md",
	"assemble_context.md",
	"summarize_entry.md",
	"merge_profile.md",
}

// PromptLayout captures the resolved system/user prompt roots and the final configuration chain.
// PromptLayout 用于保存解析后的系统/用户提示词根目录以及最终配置链。
type PromptLayout struct {
	SystemDir          string
	UserDir            string
	SystemConfigPath   string
	OverrideConfigPath string
	AppConfigPath      string
}

// SystemNoiseRulesDir returns the built-in noise_rules directory that ships with the executable.
// SystemNoiseRulesDir 用于返回随可执行文件一起分发的内置 noise_rules 目录。
func (l PromptLayout) SystemNoiseRulesDir() string {
	if strings.TrimSpace(l.SystemDir) == "" {
		return ""
	}
	return filepath.Join(l.SystemDir, "noise_rules")
}

// UserNoiseRulesDir returns the override noise_rules directory from ~/.vmm or the -config bundle root.
// UserNoiseRulesDir 用于返回来自 ~/.vmm 或 -config 覆盖根目录的 noise_rules 目录。
func (l PromptLayout) UserNoiseRulesDir() string {
	if strings.TrimSpace(l.UserDir) == "" {
		return ""
	}
	return filepath.Join(l.UserDir, "noise_rules")
}

// SystemPIIRulesDir returns the built-in pii_rules directory that ships with the executable.
// SystemPIIRulesDir 用于返回随可执行文件一起分发的内置 pii_rules 目录。
func (l PromptLayout) SystemPIIRulesDir() string {
	if strings.TrimSpace(l.SystemDir) == "" {
		return ""
	}
	return filepath.Join(l.SystemDir, "pii_rules")
}

// UserPIIRulesDir returns the override pii_rules directory from ~/.vmm or the -config bundle root.
// UserPIIRulesDir 用于返回来自 ~/.vmm 或 -config 覆盖根目录的 pii_rules 目录。
func (l PromptLayout) UserPIIRulesDir() string {
	if strings.TrimSpace(l.UserDir) == "" {
		return ""
	}
	return filepath.Join(l.UserDir, "pii_rules")
}

// RouteMap maps model prefixes to prompt folders after route files are loaded from disk.
// RouteMap 用于表示从磁盘路由文件加载后的“模型前缀到提示词目录”映射。
type RouteMap map[string]string

// ValidationErrors aggregates prompt-layout validation failures before startup aborts.
// ValidationErrors 用于聚合启动前的提示词布局校验失败项。
type ValidationErrors struct {
	Items []string
}

// Add executes the Add logic.
// Add 用于执行 Add 逻辑。
func (e *ValidationErrors) Add(format string, args ...any) {
	e.Items = append(e.Items, fmt.Sprintf(format, args...))
}

// HasAny reports whether the condition is true.
// HasAny 用于返回条件是否成立。
func (e *ValidationErrors) HasAny() bool {
	return len(e.Items) > 0
}

// Error executes the Error logic.
// Error 用于执行 Error 逻辑。
func (e *ValidationErrors) Error() string {
	if len(e.Items) == 0 {
		return ""
	}
	return "prompt validation failed:\n - " + strings.Join(e.Items, "\n - ")
}

// ResolvePromptLayout resolves the target value.
// ResolvePromptLayout 用于解析目标值。
func ResolvePromptLayout(executablePath, cwd, configArg, mode string) (PromptLayout, error) {
	// Resolve the system prompt/config base from the executable layout first.
	// 先从可执行文件布局中解析系统提示词与配置底座。
	systemDir, err := resolveSystemDir(executablePath, cwd)
	if err != nil {
		return PromptLayout{}, err
	}

	// Resolve the user override root and any explicit config file override.
	// 解析用户覆盖根目录，以及显式传入的配置文件覆盖路径。
	userDir, explicitConfigPath, err := resolveUserDir(cwd, configArg)
	if err != nil {
		return PromptLayout{}, err
	}

	// Build the final config chain with the system layer first and override layer last.
	// 构建最终配置链，保证系统层在前、覆盖层在后。
	systemConfigPath := filepath.Join(systemDir, defaultAppConfigName(mode))
	overrideConfigPath := resolveOverrideConfigPath(userDir, explicitConfigPath, mode)
	appConfigPath := systemConfigPath
	if strings.TrimSpace(overrideConfigPath) != "" {
		appConfigPath = overrideConfigPath
	}

	return PromptLayout{
		SystemDir:          systemDir,
		UserDir:            userDir,
		SystemConfigPath:   systemConfigPath,
		OverrideConfigPath: overrideConfigPath,
		AppConfigPath:      appConfigPath,
	}, nil
}

// ConfigPaths executes the ConfigPaths logic.
// ConfigPaths 用于执行 ConfigPaths 逻辑。
func (l PromptLayout) ConfigPaths() []string {
	paths := make([]string, 0, 2)
	addPath := func(path string) {
		if strings.TrimSpace(path) == "" {
			return
		}
		cleaned := filepath.Clean(path)
		for _, existing := range paths {
			if existing == cleaned {
				return
			}
		}
		paths = append(paths, cleaned)
	}
	addPath(l.SystemConfigPath)
	addPath(l.OverrideConfigPath)
	return paths
}

// resolveSystemDir resolves the target value.
// resolveSystemDir 用于解析目标值。
func resolveSystemDir(executablePath, cwd string) (string, error) {
	// Prefer the packaged ../configs layout next to the executable.
	// 优先命中位于可执行文件旁边的 ../configs 打包结构。
	candidates := []string{}
	if strings.TrimSpace(executablePath) != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(executablePath), "..", "configs"))
	}

	for _, candidate := range candidates {
		absCandidate, err := filepath.Abs(candidate)
		if err == nil && hasSystemPromptBase(absCandidate) {
			return absCandidate, nil
		}
	}

	// For built binaries, fail fast instead of silently falling back to the workspace.
	// 对正式构建产物直接快速失败，避免悄悄回退到工作区目录。
	if strings.TrimSpace(executablePath) != "" && !looksLikeGoRunExecutable(executablePath) {
		expected, _ := filepath.Abs(filepath.Join(filepath.Dir(executablePath), "..", "configs"))
		return "", fmt.Errorf("system config dir is missing or incomplete: %s", expected)
	}

	start := cwd
	if strings.TrimSpace(start) == "" {
		start, _ = os.Getwd()
	}
	start, _ = filepath.Abs(start)

	// For go run, walk upward from the current workspace until a valid configs root is found.
	// 对 go run 调试场景，从当前工作区向上回溯直到找到有效的 configs 根目录。
	for dir := start; dir != "" && dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "configs")
		if hasSystemPromptBase(candidate) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("cannot locate system config dir from executable=%q cwd=%q", executablePath, cwd)
}

// resolveUserDir resolves the target value.
// resolveUserDir 用于解析目标值。
func resolveUserDir(cwd, configArg string) (string, string, error) {
	// Default to ~/.vmm when the caller does not provide an explicit override path.
	// 当调用方未提供显式覆盖路径时，默认使用 ~/.vmm。
	if strings.TrimSpace(configArg) == "" {
		return defaultUserDir()
	}

	path, err := normalizePath(cwd, configArg)
	if err != nil {
		return "", "", err
	}

	info, statErr := os.Stat(path)
	switch {
	case statErr == nil && info.IsDir():
		// Treat a directory argument as the user override root directly.
		// 将目录参数直接视为用户覆盖根目录。
		return path, "", nil
	case statErr == nil && !info.IsDir():
		// Treat a file argument as both the override file and the user override root's local.json.
		// 将文件参数同时视为覆盖文件，以及对应覆盖根目录中的 local.json。
		return userDirWithConfig(path), path, nil
	case errors.Is(statErr, os.ErrNotExist) && configLooksLikeFile(path):
		// Preserve legacy file-style arguments even before the file is created.
		// 兼容尚未创建文件时的旧式文件参数传法。
		return userDirWithConfig(path), path, nil
	case statErr == nil:
		return path, "", nil
	case errors.Is(statErr, os.ErrNotExist):
		return path, "", nil
	default:
		return "", "", fmt.Errorf("stat config path %q: %w", path, statErr)
	}
}

// defaultUserDir executes the defaultUserDir logic.
// defaultUserDir 用于执行 defaultUserDir 逻辑。
func defaultUserDir() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve user home dir: %w", err)
	}
	return filepath.Join(home, ".vmm"), "", nil
}

// userDirWithConfig executes the userDirWithConfig logic.
// userDirWithConfig 用于执行 userDirWithConfig 逻辑。
func userDirWithConfig(configPath string) string {
	return filepath.Dir(filepath.Clean(configPath))
}

// normalizePath executes the normalizePath logic.
// normalizePath 用于执行 normalizePath 逻辑。
func normalizePath(cwd, raw string) (string, error) {
	path, err := expandHome(raw)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	base := cwd
	if strings.TrimSpace(base) == "" {
		base, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve working dir: %w", err)
		}
	}
	return filepath.Abs(filepath.Join(base, path))
}

// expandHome executes the expandHome logic.
// expandHome 用于执行 expandHome 逻辑。
func expandHome(path string) (string, error) {
	if path == "" || path[0] != '~' {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home for %q: %w", path, err)
	}
	if path == "~" {
		return home, nil
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

// configLooksLikeFile executes the configLooksLikeFile logic.
// configLooksLikeFile 用于执行 configLooksLikeFile 逻辑。
func configLooksLikeFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".json" || ext == ".yaml" || ext == ".yml" || ext == ".toml" {
		return true
	}
	return strings.Contains(filepath.Base(path), ".")
}

// looksLikeGoRunExecutable executes the looksLikeGoRunExecutable logic.
// looksLikeGoRunExecutable 用于执行 looksLikeGoRunExecutable 逻辑。
func looksLikeGoRunExecutable(path string) bool {
	slashPath := strings.ToLower(filepath.ToSlash(path))
	return strings.Contains(slashPath, "/go-build")
}

// defaultAppConfigName executes the defaultAppConfigName logic.
// defaultAppConfigName 用于执行 defaultAppConfigName 逻辑。
func defaultAppConfigName(mode string) string {
	_ = mode
	return "local.json"
}

// resolveOverrideConfigPath resolves the target value.
// resolveOverrideConfigPath 用于解析目标值。
func resolveOverrideConfigPath(userDir, explicitConfigPath, mode string) string {
	// Prefer the explicit config file when the caller passed one.
	// 当调用方显式传入配置文件时优先使用该文件。
	if strings.TrimSpace(explicitConfigPath) != "" {
		return filepath.Clean(explicitConfigPath)
	}
	if strings.TrimSpace(userDir) == "" {
		return ""
	}

	// Otherwise look for the conventional local.json inside the resolved user directory.
	// 否则在解析出的用户目录下查找约定的 local.json。
	candidate := filepath.Join(userDir, defaultAppConfigName(mode))
	info, err := os.Stat(candidate)
	if err != nil || info.IsDir() {
		return ""
	}
	return candidate
}

// hasSystemPromptBase reports whether the condition is true.
// hasSystemPromptBase 用于返回条件是否成立。
func hasSystemPromptBase(systemDir string) bool {
	return len(missingScenes(filepath.Join(systemDir, "prompts", "default"))) == 0
}

// loadRoutes loads related data.
// loadRoutes 用于加载相关数据。
func loadRoutes(path string) (RouteMap, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RouteMap{}, nil
		}
		return nil, fmt.Errorf("read routes %q: %w", path, err)
	}

	routes := RouteMap{}
	if err := json.Unmarshal(body, &routes); err != nil {
		return nil, fmt.Errorf("parse routes %q: %w", path, err)
	}

	cleaned := RouteMap{}
	for rawKey, rawFolder := range routes {
		key := strings.TrimSpace(rawKey)
		folder := strings.TrimSpace(rawFolder)
		if key == "" {
			return nil, fmt.Errorf("routes %q contains empty model key", path)
		}
		if folder == "" {
			return nil, fmt.Errorf("routes %q contains empty folder for key %q", path, key)
		}
		cleaned[key] = folder
	}

	return cleaned, nil
}

// mergeRoutes executes the mergeRoutes logic.
// mergeRoutes 用于执行 mergeRoutes 逻辑。
func mergeRoutes(systemRoutes, userRoutes RouteMap) RouteMap {
	merged := RouteMap{}
	for key, folder := range systemRoutes {
		merged[key] = folder
	}
	for key, folder := range userRoutes {
		merged[key] = folder
	}
	return merged
}

// uniqueRouteFolders executes the uniqueRouteFolders logic.
// uniqueRouteFolders 用于执行 uniqueRouteFolders 逻辑。
func uniqueRouteFolders(routes RouteMap) []string {
	folders := map[string]struct{}{}
	for _, folder := range routes {
		if strings.TrimSpace(folder) == "" {
			continue
		}
		folders[folder] = struct{}{}
	}

	names := make([]string, 0, len(folders))
	for folder := range folders {
		names = append(names, folder)
	}
	sort.Strings(names)
	return names
}

// missingScenes executes the missingScenes logic.
// missingScenes 用于执行 missingScenes 逻辑。
func missingScenes(dir string) []string {
	missing := make([]string, 0, len(RequiredScenes))
	for _, scene := range RequiredScenes {
		path := filepath.Join(dir, scene)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			missing = append(missing, scene)
		}
	}
	return missing
}

// dirExists executes the dirExists logic.
// dirExists 用于执行 dirExists 逻辑。
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
