package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type PromptManager struct {
	systemDir     string
	userDir       string
	routes        RouteMap
	matchers      []routeMatcher
	defaultFolder string
}

type routeMatcher struct {
	key    string
	prefix string
	folder string
}

func NewPromptManager(systemDir, userDir string) (*PromptManager, error) {
	validation := &ValidationErrors{}
	validateSystemDefault(systemDir, validation)

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

func (m *PromptManager) MatchFolder(modelName string) string {
	modelName = strings.TrimSpace(modelName)
	for _, matcher := range m.matchers {
		if strings.HasPrefix(modelName, matcher.prefix) {
			return matcher.folder
		}
	}
	return m.defaultFolder
}

func (m *PromptManager) GetPrompt(scene, modelName string) (string, error) {
	sceneFile := scene
	if filepath.Ext(sceneFile) == "" {
		sceneFile += ".md"
	}

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

func validateRouteFolders(systemDir, userDir string, routes RouteMap, validation *ValidationErrors) {
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

func buildMatchers(routes RouteMap) []routeMatcher {
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
