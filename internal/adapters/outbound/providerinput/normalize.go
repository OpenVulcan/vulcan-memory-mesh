// normalize.go centralizes provider-independent request cleanup at the outbound adapter boundary.
// normalize.go 用于集中管理出站适配器边界上与具体 provider 无关的请求清洗逻辑。
package providerinput

import (
	"strings"

	appports "github.com/openvulcan/vmm/internal/app/ports"
)

// NormalizeEmbeddingTexts trims embedding inputs and drops blanks before provider-specific batching or SDK mapping.
// NormalizeEmbeddingTexts 用于在 provider 拆批或 SDK 映射前裁剪 embedding 输入并剔除空白项。
func NormalizeEmbeddingTexts(texts []string) []string {
	normalized := make([]string, 0, len(texts))
	for _, text := range texts {
		if text = strings.TrimSpace(text); text != "" {
			normalized = append(normalized, text)
		}
	}
	return normalized
}

// NormalizeRerankerDocuments trims document identifiers and text, dropping candidates that cannot be sent meaningfully to a reranker.
// NormalizeRerankerDocuments 用于裁剪文档标识与文本，并丢弃无法有效发送给重排序服务的候选项。
func NormalizeRerankerDocuments(documents []appports.RerankerDocument) []appports.RerankerDocument {
	if len(documents) == 0 {
		return nil
	}
	normalized := make([]appports.RerankerDocument, 0, len(documents))
	for _, document := range documents {
		document.ID = strings.TrimSpace(document.ID)
		document.Text = strings.TrimSpace(document.Text)
		if document.ID == "" || document.Text == "" {
			continue
		}
		normalized = append(normalized, document)
	}
	return normalized
}
