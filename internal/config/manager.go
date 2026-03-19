// manager.go implements configuration and prompt loading.
// manager.go 用于实现配置与提示词加载。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PromptManager resolves prompt folders and loads scene files for processors at runtime.
// PromptManager 用于在运行时为处理器解析提示词目录并加载场景文件。
type PromptManager struct {
	systemDir     string
	userDir       string
	routes        RouteMap
	matchers      []routeMatcher
	defaultFolder string
}

// routeMatcher stores one compiled prefix rule used during model-to-folder prompt routing.
// routeMatcher 用于保存模型到提示词目录路由时使用的一条已编译前缀规则。
type routeMatcher struct {
	key    string
	prefix string
	folder string
}

// NewPromptManager creates a PromptManager instance.
// NewPromptManager 用于创建 PromptManager 实例。
func NewPromptManager(systemDir, userDir string) (*PromptManager, error) {
	// Validate the required system prompt base before merging any routes.
	// 在合并任何路由之前，先校验系统级默认提示词底座。
	validation := &ValidationErrors{}
	validateSystemDefault(systemDir, validation)

	// Load system routes first and then overlay user-defined prompt routes.
	// 先加载系统路由，再叠加用户定义的提示词路由。
	systemRoutesPath := filepath.Join(systemDir, "prompts-routes.json")
	userRoutesPath := filepath.Join(userDir, "prompts-routes.json")

	systemRoutes, err := loadRoutes(systemRoutesPath)
	if err != nil {
		return nil, err
	}
	userRoutes, err := loadRoutes(userRoutesPath)
	if err != nil {
		return nil, err
	}

	// Merge and validate the final route table before the manager becomes usable.
	// 在管理器可用之前，对最终路由表进行合并和完整性校验。
	mergedRoutes := mergeRoutes(systemRoutes, userRoutes)
	if len(mergedRoutes) == 0 {
		mergedRoutes["*"] = "default"
	}
	if folder := strings.TrimSpace(mergedRoutes["*"]); folder != "" && folder != "default" {
		validation.Add(`route "*" must point to "default", got %q`, folder)
	}

	validateRouteFolders(systemDir, userDir, mergedRoutes, validation)
	if validation.HasAny() {
		return nil, validation
	}

	return &PromptManager{
		systemDir:     systemDir,
		userDir:       userDir,
		routes:        mergedRoutes,
		matchers:      buildMatchers(mergedRoutes),
		defaultFolder: "default",
	}, nil
}

// MatchFolder executes the MatchFolder logic.
// MatchFolder 用于执行 MatchFolder 逻辑。
func (m *PromptManager) MatchFolder(modelName string) string {
	// Match the longest configured model prefix and fall back to the default folder.
	// 匹配最长的模型前缀，并在未命中时回退到默认目录。
	modelName = strings.TrimSpace(modelName)
	for _, matcher := range m.matchers {
		if strings.HasPrefix(modelName, matcher.prefix) {
			return matcher.folder
		}
	}
	return m.defaultFolder
}

// GetPrompt executes the GetPrompt logic.
// GetPrompt 用于执行 GetPrompt 逻辑。
func (m *PromptManager) GetPrompt(scene, modelName string) (string, error) {
	// Normalize the requested scene name into an on-disk markdown filename.
	// 将请求的场景名归一成磁盘上的 markdown 文件名。
	sceneFile := scene
	if filepath.Ext(sceneFile) == "" {
		sceneFile += ".md"
	}

	// Prefer the user prompt tree and fall back to the system prompt tree.
	// 优先读取用户提示词目录，未命中时再回退到系统目录。
	folder := m.MatchFolder(modelName)
	userPath := filepath.Join(m.userDir, "prompts", folder, sceneFile)
	if body, err := os.ReadFile(userPath); err == nil {
		return string(body), nil
	}

	systemPath := filepath.Join(m.systemDir, "prompts", folder, sceneFile)
	body, err := os.ReadFile(systemPath)
	if err != nil {
		return "", fmt.Errorf("read prompt folder=%q scene=%q: %w", folder, sceneFile, err)
	}
	return string(body), nil
}

// validateSystemDefault validates the input value.
// validateSystemDefault 用于校验输入值。
func validateSystemDefault(systemDir string, validation *ValidationErrors) {
	defaultDir := filepath.Join(systemDir, "prompts", "default")
	missing := missingScenes(defaultDir)
	if len(missing) == 0 {
		return
	}
	for _, scene := range missing {
		validation.Add("system default prompt missing: %s", filepath.Join(defaultDir, scene))
	}
}

// validateRouteFolders validates the input value.
// validateRouteFolders 用于校验输入值。
func validateRouteFolders(systemDir, userDir string, routes RouteMap, validation *ValidationErrors) {
	// Enforce complete prompt bundles for every non-default route target.
	// 对每个非 default 路由目标强制执行完整提示词闭环校验。
	for _, folder := range uniqueRouteFolders(routes) {
		if folder == "default" {
			continue
		}

		userFolder := filepath.Join(userDir, "prompts", folder)
		systemFolder := filepath.Join(systemDir, "prompts", folder)

		if dirExists(userFolder) {
			for _, scene := range missingScenes(userFolder) {
				validation.Add("user prompt folder %q is incomplete, missing %s", userFolder, scene)
			}
			continue
		}

		for _, scene := range missingScenes(systemFolder) {
			validation.Add("system prompt folder %q is incomplete, missing %s", systemFolder, scene)
		}
	}
}

// buildMatchers builds the target dependency.
// buildMatchers 用于构建目标依赖。
func buildMatchers(routes RouteMap) []routeMatcher {
	// Convert route keys into sortable prefix matchers for runtime lookup.
	// 将路由键转换成可排序的前缀匹配器，供运行时查找使用。
	matchers := make([]routeMatcher, 0, len(routes))
	for key, folder := range routes {
		if key == "*" {
			continue
		}
		prefix := strings.TrimSuffix(key, "*")
		if prefix == "" {
			continue
		}
		matchers = append(matchers, routeMatcher{
			key:    key,
			prefix: prefix,
			folder: folder,
		})
	}

	sort.SliceStable(matchers, func(i, j int) bool {
		if len(matchers[i].prefix) == len(matchers[j].prefix) {
			return matchers[i].key < matchers[j].key
		}
		return len(matchers[i].prefix) > len(matchers[j].prefix)
	})
	return matchers
}
