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

var RequiredScenes = []string{
	"extract_intent.md",
	"assemble_context.md",
	"summarize_entry.md",
	"merge_profile.md",
}

type PromptLayout struct {
	SystemDir          string
	UserDir            string
	SystemConfigPath   string
	OverrideConfigPath string
	AppConfigPath      string
}

type RouteMap map[string]string

type ValidationErrors struct {
	Items []string
}

func (e *ValidationErrors) Add(format string, args ...any) {
	e.Items = append(e.Items, fmt.Sprintf(format, args...))
}

func (e *ValidationErrors) HasAny() bool {
	return len(e.Items) > 0
}

func (e *ValidationErrors) Error() string {
	if len(e.Items) == 0 {
		return ""
	}
	return "prompt validation failed:\n - " + strings.Join(e.Items, "\n - ")
}

func ResolvePromptLayout(executablePath, cwd, configArg, mode string) (PromptLayout, error) {
	systemDir, err := resolveSystemDir(executablePath, cwd)
	if err != nil {
		return PromptLayout{}, err
	}

	userDir, explicitConfigPath, err := resolveUserDir(cwd, configArg)
	if err != nil {
		return PromptLayout{}, err
	}

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

func resolveSystemDir(executablePath, cwd string) (string, error) {
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

	if strings.TrimSpace(executablePath) != "" && !looksLikeGoRunExecutable(executablePath) {
		expected, _ := filepath.Abs(filepath.Join(filepath.Dir(executablePath), "..", "configs"))
		return "", fmt.Errorf("system config dir is missing or incomplete: %s", expected)
	}

	start := cwd
	if strings.TrimSpace(start) == "" {
		start, _ = os.Getwd()
	}
	start, _ = filepath.Abs(start)

	for dir := start; dir != "" && dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "configs")
		if hasSystemPromptBase(candidate) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("cannot locate system config dir from executable=%q cwd=%q", executablePath, cwd)
}

func resolveUserDir(cwd, configArg string) (string, string, error) {
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
		return path, "", nil
	case statErr == nil && !info.IsDir():
		return userDirWithConfig(path), path, nil
	case errors.Is(statErr, os.ErrNotExist) && configLooksLikeFile(path):
		return userDirWithConfig(path), path, nil
	case statErr == nil:
		return path, "", nil
	case errors.Is(statErr, os.ErrNotExist):
		return path, "", nil
	default:
		return "", "", fmt.Errorf("stat config path %q: %w", path, statErr)
	}
}

func defaultUserDir() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve user home dir: %w", err)
	}
	return filepath.Join(home, ".vmm"), "", nil
}

func userDirWithConfig(configPath string) string {
	return filepath.Dir(filepath.Clean(configPath))
}

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

func configLooksLikeFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".json" || ext == ".yaml" || ext == ".yml" || ext == ".toml" {
		return true
	}
	return strings.Contains(filepath.Base(path), ".")
}

func looksLikeGoRunExecutable(path string) bool {
	slashPath := strings.ToLower(filepath.ToSlash(path))
	return strings.Contains(slashPath, "/go-build")
}

func defaultAppConfigName(mode string) string {
	_ = mode
	return "local.json"
}

func resolveOverrideConfigPath(userDir, explicitConfigPath, mode string) string {
	if strings.TrimSpace(explicitConfigPath) != "" {
		return filepath.Clean(explicitConfigPath)
	}
	if strings.TrimSpace(userDir) == "" {
		return ""
	}
	candidate := filepath.Join(userDir, defaultAppConfigName(mode))
	info, err := os.Stat(candidate)
	if err != nil || info.IsDir() {
		return ""
	}
	return candidate
}

func hasSystemPromptBase(systemDir string) bool {
	return len(missingScenes(filepath.Join(systemDir, "prompts", "default"))) == 0
}

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

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
