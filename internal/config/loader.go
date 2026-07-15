// loader.go implements configuration and prompt loading.
// loader.go 用于实现配置与提示词加载。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RequiredScenes lists the prompt scene files that every valid prompt bundle must provide.
// RequiredScenes 用于列出每个有效提示词包都必须提供的场景文件。
var RequiredScenes = []string{
	"precheck_l1_main.md",
	"precheck_l2_main.md",
	"postaction_l1_main.md",
	"postaction_l2_main.md",
	"profile_instruction_main.md",
}

const (
	// defaultBaseConfigFileName identifies the packaged baseline configuration shared by every local runtime entrypoint.
	// defaultBaseConfigFileName 用于标识所有本地运行入口共享的打包基础配置文件。
	defaultBaseConfigFileName = "base.yaml"
	// defaultAppConfigFileName identifies the packaged and user-overridable application configuration file.
	// defaultAppConfigFileName 用于标识打包层与用户覆盖层共用的应用配置文件。
	defaultAppConfigFileName = "config.yaml"
)

// PromptLayout captures the resolved system/user prompt roots and the final configuration chain.
// PromptLayout 用于保存解析后的系统/用户提示词根目录以及最终配置链。
type PromptLayout struct {
	SystemDir          string
	BaseConfigPath     string
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

// ValidationErrors aggregates prompt-layout validation failures before startup aborts.
// ValidationErrors 用于聚合启动前的提示词布局校验失败项。
type ValidationErrors struct {
	Items []string
}

// Add formats and appends one prompt-layout validation failure.
// Add 用于格式化并追加一项提示词布局校验失败。
func (e *ValidationErrors) Add(format string, args ...any) {
	e.Items = append(e.Items, fmt.Sprintf(format, args...))
}

// HasAny reports whether startup validation collected at least one failure.
// HasAny 用于报告启动校验是否已收集至少一项失败。
func (e *ValidationErrors) HasAny() bool {
	return len(e.Items) > 0
}

// Error renders all collected validation failures as one bullet-list error message.
// Error 用于把所有已收集的校验失败渲染为一条项目列表错误消息。
func (e *ValidationErrors) Error() string {
	if len(e.Items) == 0 {
		return ""
	}
	return "prompt validation failed:\n - " + strings.Join(e.Items, "\n - ")
}

// ResolvePromptLayout resolves packaged and user configuration roots from the executable path, working directory, and optional override argument.
// ResolvePromptLayout 根据可执行文件路径、工作目录和可选覆盖参数解析打包层与用户层配置根目录。
func ResolvePromptLayout(executablePath, cwd, configArg string) (PromptLayout, error) {
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

	// Build the final config chain with project base first, packaged config second, and user override last.
	// 构建最终配置链，保证项目 base 在前、项目 config 在中、用户覆盖层在后。
	baseConfigPath := filepath.Join(systemDir, defaultBaseConfigFileName)
	systemConfigPath := resolveBundledConfigPath(systemDir, defaultAppConfigFileName)
	overrideConfigPath := resolveOverrideConfigPath(userDir, explicitConfigPath)
	appConfigPath := systemConfigPath
	if strings.TrimSpace(appConfigPath) == "" {
		appConfigPath = baseConfigPath
	}
	if strings.TrimSpace(overrideConfigPath) != "" {
		appConfigPath = overrideConfigPath
	}

	return PromptLayout{
		SystemDir:          systemDir,
		BaseConfigPath:     baseConfigPath,
		UserDir:            userDir,
		SystemConfigPath:   systemConfigPath,
		OverrideConfigPath: overrideConfigPath,
		AppConfigPath:      appConfigPath,
	}, nil
}

// ConfigPaths returns the unique base, packaged, and user override config layers in merge order.
// ConfigPaths 用于按合并顺序返回去重后的基础、打包和用户覆盖配置层。
func (l PromptLayout) ConfigPaths() []string {
	paths := make([]string, 0, 3)
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
	addPath(l.BaseConfigPath)
	addPath(l.SystemConfigPath)
	addPath(l.OverrideConfigPath)
	return paths
}

// resolveSystemDir locates a valid packaged or go-run configs root from the executable path and working directory.
// resolveSystemDir 用于根据可执行文件路径和工作目录定位有效的打包或 go-run configs 根目录。
func resolveSystemDir(executablePath, cwd string) (string, error) {
	// Prefer the packaged ../configs layout next to the executable.
	// 优先命中位于可执行文件旁边的 ../configs 打包结构。
	candidates := []string{}
	if strings.TrimSpace(executablePath) != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(executablePath), "..", "configs"))
	}

	for _, candidate := range candidates {
		absCandidate, err := filepath.Abs(candidate)
		if err == nil && hasSystemConfigBase(absCandidate) {
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
		if hasSystemConfigBase(candidate) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("cannot locate system config dir from executable=%q cwd=%q", executablePath, cwd)
}

// resolveUserDir interprets the optional override as a config root or YAML file and otherwise falls back to the user VMM directory.
// resolveUserDir 用于把可选覆盖参数解释为配置根目录或 YAML 文件，否则回退到用户 VMM 目录。
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
		// Treat one explicit YAML file as both the override file and the user override root's config.yaml so the main runtime config stays YAML-only even when callers bypass the default filename.
		// 将显式传入的 YAML 文件同时视为覆盖文件及其所在覆盖根目录中的 config.yaml，以便调用方绕过默认文件名时，主运行时配置仍保持 YAML-only 约束。
		if err := validateExplicitMainConfigFilePath(path); err != nil {
			return "", "", err
		}
		return userDirWithConfig(path), path, nil
	case errors.Is(statErr, os.ErrNotExist) && pathLooksLikeFile(path):
		// Preserve explicit YAML file-style arguments even before the file is created, but reject legacy JSON/TOML suffixes up front so startup behavior matches the documented YAML-only main-config contract.
		// 即使文件尚未创建，也继续支持显式 YAML 文件式参数；但要前置拒绝旧的 JSON/TOML 后缀，确保启动行为与文档声明的主配置 YAML-only 契约一致。
		if err := validateExplicitMainConfigFilePath(path); err != nil {
			return "", "", err
		}
		return userDirWithConfig(path), path, nil
	case statErr == nil:
		return path, "", nil
	case errors.Is(statErr, os.ErrNotExist):
		return path, "", nil
	default:
		return "", "", fmt.Errorf("stat config path %q: %w", path, statErr)
	}
}

// defaultUserDir returns the current user's ~/.vmm override root and home directory.
// defaultUserDir 用于返回当前用户的 ~/.vmm 覆盖根目录及主目录。
func defaultUserDir() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve user home dir: %w", err)
	}
	return filepath.Join(home, ".vmm"), "", nil
}

// userDirWithConfig returns the directory that owns an explicitly selected config file.
// userDirWithConfig 用于返回显式配置文件所属的目录。
func userDirWithConfig(configPath string) string {
	return filepath.Dir(filepath.Clean(configPath))
}

// normalizePath expands home-relative input and resolves relative paths against the supplied working directory.
// normalizePath 用于展开主目录相对路径，并基于给定工作目录解析相对路径。
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

// expandHome replaces a leading tilde with the current user's home directory while leaving other paths unchanged.
// expandHome 用于把开头的波浪号替换为当前用户主目录，并保持其他路径不变。
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

// pathLooksLikeFile reports whether one -config argument is likely intended as a file path so layout resolution can reject unsupported main-config suffixes even before the target file exists.
// pathLooksLikeFile 用于判断某个 -config 参数是否更像文件路径，让布局解析在目标文件尚未存在时也能拒绝不支持的主配置后缀。
func pathLooksLikeFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".json" || ext == ".yaml" || ext == ".yml" || ext == ".toml" {
		return true
	}
	return strings.Contains(filepath.Base(path), ".")
}

// validateExplicitMainConfigFilePath verifies one explicit -config file path still honors the repository-wide YAML-only contract for the main runtime config.
// validateExplicitMainConfigFilePath 用于校验显式传入的 -config 文件路径仍然遵守仓库范围内“主运行时配置只接受 YAML”的约束。
func validateExplicitMainConfigFilePath(path string) error {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(path)))
	if ext == ".yaml" || ext == ".yml" {
		return nil
	}
	return fmt.Errorf("explicit -config file %q must end with .yaml or .yml", path)
}

// looksLikeGoRunExecutable detects temporary go-build executable paths used by go run.
// looksLikeGoRunExecutable 用于识别 go run 使用的临时 go-build 可执行文件路径。
func looksLikeGoRunExecutable(path string) bool {
	slashPath := strings.ToLower(filepath.ToSlash(path))
	return strings.Contains(slashPath, "/go-build")
}

// resolveBundledConfigPath returns the packaged project-level config path only when the file actually exists.
// resolveBundledConfigPath 用于仅在文件真实存在时返回打包后的项目级 config 路径。
func resolveBundledConfigPath(systemDir, name string) string {
	if strings.TrimSpace(systemDir) == "" || strings.TrimSpace(name) == "" {
		return ""
	}
	candidate := filepath.Join(systemDir, name)
	info, err := os.Stat(candidate)
	if err != nil || info.IsDir() {
		return ""
	}
	return candidate
}

// resolveOverrideConfigPath prefers an explicit YAML file and otherwise selects an existing config.yaml under the user root.
// resolveOverrideConfigPath 用于优先选择显式 YAML 文件，否则选择用户根目录下已存在的 config.yaml。
func resolveOverrideConfigPath(userDir, explicitConfigPath string) string {
	// Prefer the explicit config file when the caller passed one.
	// 当调用方显式传入配置文件时优先使用该文件。
	if strings.TrimSpace(explicitConfigPath) != "" {
		return filepath.Clean(explicitConfigPath)
	}
	if strings.TrimSpace(userDir) == "" {
		return ""
	}

	// Otherwise look for the conventional config.yaml inside the resolved user directory.
	// 否则在解析出的用户目录下查找约定的 config.yaml。
	candidate := filepath.Join(userDir, defaultAppConfigFileName)
	info, err := os.Stat(candidate)
	if err != nil || info.IsDir() {
		return ""
	}
	return candidate
}

// hasSystemConfigBase reports whether one directory is a usable system config root before prompt selection is loaded from config.
// hasSystemConfigBase 用于在提示词选择尚未从配置加载前，判断一个目录是否可作为系统配置根目录。
func hasSystemConfigBase(systemDir string) bool {
	info, err := os.Stat(filepath.Join(systemDir, defaultBaseConfigFileName))
	return err == nil && !info.IsDir()
}

// missingScenes lists required prompt scene files that are absent from one bundle directory.
// missingScenes 用于列出一个提示词包目录中缺失的必需场景文件。
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

// dirExists reports whether a path exists and is a directory.
// dirExists 用于报告路径是否存在且为目录。
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
