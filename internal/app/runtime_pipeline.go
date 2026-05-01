// runtime_pipeline.go implements application-layer pipeline builders that assemble runtime processors, use cases, and transport-facing dependencies.
// runtime_pipeline.go 用于实现应用层 pipeline 构建逻辑，装配运行时处理器、用例和传输层依赖。
package app

import (
	"context"

	grpcapi "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/app/usecase"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/logic/processor"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/pii"
)

// runtimePIIScrubber adapts the shared PII engine into the narrow use-case interface so pre-check and post-action can redact text without depending on engine-specific language-routing details.
// runtimePIIScrubber 用于把共享 PII 引擎适配成用例层需要的最小接口，让 pre-check 与 post-action 可以执行脱敏，而不直接依赖引擎的语言路由细节。
type runtimePIIScrubber struct {
	engine          *pii.Engine
	defaultLanguage string
}

// runtimeUseCaseSet groups the runtime-facing use cases built on top of the selected adapters so the composition root can wire transport and shutdown behavior from one compact bundle.
// runtimeUseCaseSet 用于打包基于当前适配器构建出的运行时用例，让组合根可以从一份紧凑集合中继续接线传输层和 shutdown 行为。
type runtimeUseCaseSet struct {
	Workspace   *usecase.WorkspaceUseCase
	Profiles    *usecase.ProfileUseCase
	Memory      *usecase.MemoryUseCase
	ChatCompact *usecase.ChatCompactUseCase
	PreCheck    *usecase.PreCheckUseCase
	PostAction  *usecase.PostActionUseCase
	Retention   *usecase.RetentionUseCase
}

// Scrub redacts one text fragment with the configured default runtime language so live request paths reuse the same ruleset as the standalone PII tester.
// Scrub 用于使用运行时配置好的默认语言对单段文本执行脱敏，让实时请求链路复用与独立 PII 测试器相同的一套规则。
func (s runtimePIIScrubber) Scrub(text string) string {
	if s.engine == nil {
		return text
	}
	return s.engine.Scrub(text, s.defaultLanguage)
}

// composeRuntimeUseCases assembles the runtime use-case graph together with the transport-facing gRPC dependency bundle used by the local server.
// composeRuntimeUseCases 用于装配运行时用例图，并同步构建本地服务使用的传输层 gRPC 依赖集合。
func composeRuntimeUseCases(cfg config.Config, prompts appports.PromptSource, ids appports.IDGenerator, logger *logx.Logger, llmOutputLogger *logx.Logger, ai runtimeAIDependencies, storage runtimeStorageCapabilities, noiseGate *processor.NoiseGate, piiScrubber usecase.PIIScrubber) (runtimeUseCaseSet, grpcapi.Dependencies, error) {
	if logger == nil {
		logger = logx.Default()
	}

	// Build the application-facing use cases on top of the selected storage capabilities and AI processors so the composition root can remain a thin coordinator.
	// 基于选中的存储能力与 AI 处理器构建应用层用例，让组合根本身保持为薄协调器。
	workspace := usecase.NewWorkspaceUseCase(storage.WorkspaceStore, storage.Vector)
	profileInstructionPromptModel := selectProcessorLLMModel(cfg, appports.LLMRouteSelectionLevelProfileInstruction)
	preCheckL1PromptModel := selectProcessorLLMModel(cfg, appports.LLMRouteSelectionLevelPreCheckL1)
	preCheckL2PromptModel := selectProcessorLLMModel(cfg, appports.LLMRouteSelectionLevelPreCheckL2)
	postActionL1PromptModel := selectProcessorLLMModel(cfg, appports.LLMRouteSelectionLevelPostActionL1)
	postActionL2PromptModel := selectProcessorLLMModel(cfg, appports.LLMRouteSelectionLevelPostActionL2)
	processorLLM := adaptLLMForProcessorRoutes(cfg, ai.LLM)
	processorLLM = wrapLLMWithOutputLogger(processorLLM, llmOutputLogger)
	profiles := usecase.NewProfileUseCase(storage.ProfileStore, processor.NewManualProfileReviewer(processorLLM, prompts, profileInstructionPromptModel), logger)
	memory := usecase.NewMemoryUseCase(storage.ProfileStore, storage.MemoryStore, ai.Embedding, storage.Vector, logger)
	memory.ConfigurePIIScrubber(piiScrubber)
	chatCompact := usecase.NewChatCompactUseCase(storage.ChatCompactStore)
	candidateReviewer := processor.NewPostActionCandidateReviewer(processorLLM, prompts, postActionL2PromptModel)
	memory.ConfigureHybrid(cfg.MemoryPipeline.HybridEnabled, cfg.MemoryPipeline.LexicalTopK, cfg.MemoryPipeline.RRFK)
	memory.ConfigureMMR(cfg.MemoryPipeline.MMREnabled, cfg.MemoryPipeline.MMRLambda)
	memory.ConfigureDecay(
		cfg.MemoryPipeline.WeibullEnabled,
		cfg.MemoryPipeline.WeibullShape,
		cfg.MemoryPipeline.WeibullScaleHours,
		cfg.MemoryPipeline.WeibullMinMultiplier,
		cfg.MemoryPipeline.WeibullReinforceWeight,
		cfg.MemoryPipeline.WeibullCrossSessionBoost,
	)
	memory.ConfigureRerank(ai.Reranker, cfg.Rerank.TopN)
	memory.ConfigureHardDedupePoolTopK(hardDedupePoolTopKOrDefault(cfg))
	memory.ConfigureMemoryReplace(
		candidateReviewer,
		cfg.PreCheck.TopK,
		cfg.MemoryReplaceScope,
		replaceMinSimilarityOrDefault(cfg),
		hardDedupeCosineThresholdOrDefault(cfg),
	)
	pre := usecase.NewPreCheckUseCase(
		memory,
		storage.Relational,
		processor.NewIntentExtractor(processorLLM, prompts, preCheckL1PromptModel, cfg.MemoryPipeline.MaxSearchKeywords),
		processor.NewPreCheckMemoryReviewer(processorLLM, prompts, preCheckL2PromptModel),
		processor.NewContextAssembler(),
		usecase.PreCheckConfig{
			IntentTimeout:      cfg.PreCheck.IntentTimeout.Duration,
			TopK:               cfg.PreCheck.TopK,
			MinSimilarityScore: minSimilarityOrDefault(cfg),
			SearchScope:        cfg.PreCheck.SearchScope,
			HistoryTurns:       cfg.PostAction.SessionAnalysisHistoryTurns,
			MaxInputTokens:     cfg.PostAction.SessionAnalysisMaxInputTokens,
		},
		logger,
	)
	pre.ConfigurePIIScrubber(piiScrubber)
	post := usecase.NewPostActionUseCase(
		noiseGate,
		storage.Relational,
		ai.Embedding,
		storage.Vector,
		processor.NewTurnAnalyzer(processorLLM, prompts, postActionL1PromptModel),
		memory,
		candidateReviewer,
		usecase.PostActionAnalysisConfig{
			TurnThreshold:             cfg.PostAction.SessionAnalysisTurnThreshold,
			TokenThreshold:            cfg.PostAction.SessionAnalysisTokenThreshold,
			IdleTimeout:               cfg.PostAction.SessionAnalysisIdleTimeout.Duration,
			HistoryTurns:              cfg.PostAction.SessionAnalysisHistoryTurns,
			MaxInputTokens:            cfg.PostAction.SessionAnalysisMaxInputTokens,
			DedupeSearchTopK:          cfg.PreCheck.TopK,
			MemoryReplaceScope:        cfg.MemoryReplaceScope,
			DedupeMinSimilarity:       replaceMinSimilarityOrDefault(cfg),
			HardDedupeCosineThreshold: hardDedupeCosineThresholdOrDefault(cfg),
			MaxQueueWorkers:           cfg.PostAction.MaxQueueWorkers,
		},
		logger,
	)
	post.ConfigurePIIScrubber(piiScrubber)

	// Retention still consumes the shared turn-history base used by PreCheck and PostAction, so the hot window remains derived from that shared runtime knob plus the extra keep-turn allowance.
	// retention 仍然复用 PreCheck 与 PostAction 共享的 turn 历史基线，因此热窗口继续由这组共享运行时参数再叠加额外保留轮数计算而来。
	retentionTurnHotWindowSize := cfg.PostAction.SessionAnalysisHistoryTurns + cfg.Retention.TurnKeepExtraTurns
	retention := usecase.NewRetentionUseCase(storage.RetentionStore, storage.Vector, usecase.RetentionConfig{
		Enabled:                     cfg.Retention.Enabled,
		RecycleScanInterval:         cfg.Retention.RecycleScanInterval.Duration,
		SessionIdleRecycleAfter:     cfg.Retention.SessionIdleRecycleAfter.Duration,
		TurnHotWindowSize:           retentionTurnHotWindowSize,
		TrashRetention:              cfg.Retention.TrashRetention.Duration,
		ProtectPriorityFloor:        cfg.Retention.ProtectPriorityFloor,
		ProtectMemoryLevelFloor:     cfg.Retention.ProtectMemoryLevelFloor,
		SkipProtectedSharedMemories: cfg.Retention.SkipProtectedSharedMemories,
	}, logger)

	useCases := runtimeUseCaseSet{
		Workspace:   workspace,
		Profiles:    profiles,
		Memory:      memory,
		ChatCompact: chatCompact,
		PreCheck:    pre,
		PostAction:  post,
		Retention:   retention,
	}
	grpcDeps := grpcapi.Dependencies{
		IDs:               ids,
		Workspace:         workspace,
		Profiles:          profiles,
		Memory:            memory,
		ChatCompact:       chatCompact,
		PreCheck:          pre,
		PostAction:        post,
		ScopeResolver:     storage.ScopeResolver,
		Logger:            logger,
		Validator:         grpcapi.NewRequestValidator(),
		DebugRPCPayloads:  cfg.Logging.DebugRPCPayloads,
		WorkspaceTimeout:  cfg.GRPC.RequestTimeout.Workspace.Duration,
		PreCheckTimeout:   cfg.GRPC.RequestTimeout.PreCheck.Duration,
		PostActionTimeout: cfg.GRPC.RequestTimeout.PostAction.Duration,
	}
	return useCases, grpcDeps, nil
}

// minSimilarityOrDefault keeps the pre-check runtime aligned with the validated memory-pipeline similarity floor.
// minSimilarityOrDefault 用于让 pre-check 运行时与已校验过的 memory pipeline 相似度下限保持一致。
func minSimilarityOrDefault(cfg config.Config) float64 {
	if cfg.MemoryPipeline.MinSimilarityScore != nil && *cfg.MemoryPipeline.MinSimilarityScore > 0 {
		return *cfg.MemoryPipeline.MinSimilarityScore
	}
	return 0.75
}

// replaceMinSimilarityOrDefault keeps post-action/direct-write duplicate review aligned with the dedicated reviewer-entry similarity floor instead of the older hard-coded clamp.
// replaceMinSimilarityOrDefault 用于让 post-action/主动写记忆重复评审与专用 reviewer 入口相似度下限保持一致，而不再依赖旧的硬编码钳制。
func replaceMinSimilarityOrDefault(cfg config.Config) float64 {
	if cfg.MemoryPipeline.ReplaceMinSimilarityScore != nil && *cfg.MemoryPipeline.ReplaceMinSimilarityScore >= 0 {
		return *cfg.MemoryPipeline.ReplaceMinSimilarityScore
	}
	return 0.80
}

// hardDedupeCosineThresholdOrDefault keeps the pre-review vector hard-dedupe gate aligned with validated config while still allowing callers to disable it by setting 0.
// hardDedupeCosineThresholdOrDefault 用于让 reviewer 前向量硬排重门槛与已校验配置保持一致，同时允许调用方通过设置 0 显式关闭。
func hardDedupeCosineThresholdOrDefault(cfg config.Config) float64 {
	if cfg.MemoryPipeline.HardDedupeCosineThreshold != nil && *cfg.MemoryPipeline.HardDedupeCosineThreshold >= 0 {
		return *cfg.MemoryPipeline.HardDedupeCosineThreshold
	}
	return 0.99
}

// hardDedupePoolTopKOrDefault keeps the reviewer-front hard-dedupe scan window aligned with validated config while guaranteeing a non-zero bounded expansion target for the search pipeline.
// hardDedupePoolTopKOrDefault 用于让 reviewer 前硬排重扫描窗口与已校验配置保持一致，并保证检索流水线总能拿到一个非零的扩展目标值。
func hardDedupePoolTopKOrDefault(cfg config.Config) int {
	if cfg.MemoryPipeline.HardDedupePoolTopK > 0 {
		return cfg.MemoryPipeline.HardDedupePoolTopK
	}
	return 16
}

// buildNoiseGate builds the pre-persistence admission gate that blocks noisy single-round turns from entering long-term memory.
// buildNoiseGate 用于构建写库前的准入门控器，阻止噪声单轮问答进入长期记忆。
func buildNoiseGate(cfg config.Config, layout config.PromptLayout, embedding appports.EmbeddingClient, cache appports.NoiseEmbeddingCache, logger *logx.Logger) (*processor.NoiseGate, error) {
	return processor.NewNoiseGate(context.Background(), embedding, logger, processor.NoiseGateConfig{
		SystemDir:         layout.SystemNoiseRulesDir(),
		UserDir:           layout.UserNoiseRulesDir(),
		DefaultLanguage:   cfg.Noise.DefaultLanguage,
		Enabled:           cfg.Noise.Enabled,
		SemanticEnabled:   cfg.Noise.SemanticEnabled,
		SemanticThreshold: cfg.Noise.SemanticThreshold,
		Model:             cfg.Embedding.Model,
		Dimension:         cfg.Embedding.Dimension,
		Cache:             cache,
	})
}

// buildPIIScrubber builds the shared runtime PII scrubber used by pre-check and post-action so both entry chains reuse the packaged system rules and user override bundle roots.
// buildPIIScrubber 用于构建 pre-check 与 post-action 共用的运行时 PII 脱敏器，让两条入口链路复用打包后的系统规则目录和用户覆盖规则目录。
func buildPIIScrubber(cfg config.Config, layout config.PromptLayout, logger *logx.Logger) (usecase.PIIScrubber, error) {
	engine, err := pii.NewEngineWithLogger(layout.SystemPIIRulesDir(), layout.UserPIIRulesDir(), cfg.PII.DefaultLanguage, logger)
	if err != nil {
		return nil, err
	}
	return runtimePIIScrubber{
		engine:          engine,
		defaultLanguage: cfg.PII.DefaultLanguage,
	}, nil
}
