// precheck_origin.go centralizes reviewer-facing explanations for retrieval origin labels used by the pre-check candidate list.
// precheck_origin.go 用于集中管理 pre-check 候选列表里面向 reviewer 的检索来源解释。
package usecase

import "strings"

// preCheckOriginExplanation stores the compact reviewer-facing label and explanation derived from one internal retrieval origin code.
// preCheckOriginExplanation 用于保存从内部检索来源代码推导出的紧凑 reviewer 标签和解释文本。
type preCheckOriginExplanation struct {
	Label       string
	Explanation string
}

// describePreCheckMemoryOrigin converts one internal retrieval origin code into a stable reviewer-facing label plus a concise explanation of the ranking path.
// describePreCheckMemoryOrigin 用于把内部检索来源代码转换成稳定的 reviewer 标签，以及一段简短的排序路径说明。
func describePreCheckMemoryOrigin(origin string) preCheckOriginExplanation {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return preCheckOriginExplanation{
			Label:       "Unknown Retrieval Path",
			Explanation: "当前候选的检索来源未记录，默认按最终排序结果参与第二层评审。",
		}
	}

	hasMMR := strings.HasSuffix(origin, "_mmr")
	base := origin
	if hasMMR {
		base = strings.TrimSuffix(base, "_mmr")
	}

	var explanation preCheckOriginExplanation
	switch base {
	case "vector_search":
		explanation = preCheckOriginExplanation{
			Label:       "Vector Recall",
			Explanation: "候选仅通过向量语义召回命中，未经过 lexical 融合或 rerank 重排。",
		}
	case "lexical_search":
		explanation = preCheckOriginExplanation{
			Label:       "Lexical Recall",
			Explanation: "候选仅通过 lexical/BM25 召回命中，适合关键词锚点更强的场景。",
		}
	case "hybrid_rrf":
		explanation = preCheckOriginExplanation{
			Label:       "Hybrid RRF",
			Explanation: "候选同时受向量或 lexical 路径支持，并通过 RRF 融合进入当前排序。",
		}
	case "vector_rerank":
		explanation = preCheckOriginExplanation{
			Label:       "Vector Recall + Rerank",
			Explanation: "候选先由向量召回命中，再被 rerank 模型重排。",
		}
	case "lexical_rerank":
		explanation = preCheckOriginExplanation{
			Label:       "Lexical Recall + Rerank",
			Explanation: "候选先由 lexical 路径命中，再被 rerank 模型重排。",
		}
	case "hybrid_rrf_rerank":
		explanation = preCheckOriginExplanation{
			Label:       "Hybrid RRF + Rerank",
			Explanation: "候选先经过 hybrid RRF 融合，再被 rerank 模型重新评估顺序。",
		}
	case "rerank":
		explanation = preCheckOriginExplanation{
			Label:       "Rerank",
			Explanation: "候选被 rerank 模型重排，但原始召回路径未保留。",
		}
	default:
		explanation = preCheckOriginExplanation{
			Label:       origin,
			Explanation: "候选来源由检索链内部标记为 " + origin + "，当前按该路径的最终排序进入第二层评审。",
		}
	}

	if hasMMR {
		explanation.Label += " + MMR"
		explanation.Explanation += " 当前顺序还额外经过 MMR 多样性控制，减少近重复候选挤占名额。"
	}
	return explanation
}
