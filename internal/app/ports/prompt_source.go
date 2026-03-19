package ports

type PromptSource interface {
	GetPrompt(scene, modelName string) (string, error)
}
