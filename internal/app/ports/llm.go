// llm.go re-exports the logic-owned model-generation contract for the application layer.
// llm.go 用于为应用层重导出由 logic 层拥有的模型生成契约。
package ports

import logicports "github.com/openvulcan/vmm/internal/logic/ports"

// LLMResponseFormat describes the response shape that processors expect from the model call.
// LLMResponseFormat 用于描述处理器期望模型返回的响应形态。
type LLMResponseFormat = logicports.LLMResponseFormat

// Constants enumerate the supported response formats for the LLM port.
// Constants 用于枚举 LLM 端口支持的响应格式。
const (
	LLMResponseFormatText = logicports.LLMResponseFormatText
	LLMResponseFormatJSON = logicports.LLMResponseFormatJSON
)

// LLMRouteSelectionLevel describes the business call tier carried through the application-facing LLM contract.
// LLMRouteSelectionLevel 用于描述应用层 LLM 契约里携带的业务调用层级。
type LLMRouteSelectionLevel = logicports.LLMRouteSelectionLevel

// Constants enumerate the supported business-facing LLM route selection levels.
// Constants 用于枚举面向业务语义的 LLM 路由选择层级。
const (
	LLMRouteSelectionLevelPreCheckL1   = logicports.LLMRouteSelectionLevelPreCheckL1
	LLMRouteSelectionLevelPreCheckL2   = logicports.LLMRouteSelectionLevelPreCheckL2
	LLMRouteSelectionLevelPostActionL1 = logicports.LLMRouteSelectionLevelPostActionL1
	LLMRouteSelectionLevelPostActionL2 = logicports.LLMRouteSelectionLevelPostActionL2
	LLMRouteSelectionLevelReserve      = logicports.LLMRouteSelectionLevelReserve
)

// LLMRequest carries the provider-neutral generation payload built by processors and use cases.
// LLMRequest 用于承载处理器和用例层构建的 provider 无关生成请求。
type LLMRequest = logicports.LLMRequest

// LLMResponse returns the raw model output, actual model identity, and token usage in the internal contract shape.
// LLMResponse 用于返回内部契约形态的原始模型输出、实际模型标识和 token 用量。
type LLMResponse = logicports.LLMResponse

// LLMClient is the port that lets processors call a model without binding to a specific provider SDK.
// LLMClient 用于让处理器在不绑定具体模型 SDK 的前提下调用大模型。
type LLMClient = logicports.LLMClient
