// native_packaged_integration_test.go verifies the standard native output as a real isolated executable artifact.
// native_packaged_integration_test.go 用真实隔离的标准输出目录验证原生运行时可执行文件与动态库产物。
package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	vmmv1 "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi/proto/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	// packagedNativeHealthTimeout bounds the time spent waiting for a real packaged process to bind gRPC and pass storage health checks.
	// packagedNativeHealthTimeout 限制真实打包进程绑定 gRPC 并通过存储健康检查所允许的等待时间。
	packagedNativeHealthTimeout = 30 * time.Second

	// packagedNativeStopTimeout bounds graceful shutdown before the acceptance test forcefully terminates its own child process.
	// packagedNativeStopTimeout 限制验收测试等待子进程优雅退出后再终止自身子进程的时间。
	packagedNativeStopTimeout = 5 * time.Second
)

// packagedNativeManifest is the portion of the native artifact manifest required to prove the packaged ABI and file identity.
// packagedNativeManifest 保存验收原生 ABI 与动态库文件身份所需的产物清单字段。
type packagedNativeManifest struct {
	SchemaVersion int    `json:"schema_version"`
	ABIVersion    int    `json:"abi_version"`
	EngineVersion string `json:"engine_version"`
	Target        string `json:"target"`
	LibraryFile   string `json:"library_file"`
	LibrarySHA256 string `json:"library_sha256"`
}

// packagedNativeVersionDocument is the machine-readable identity returned by the packaged executable.
// packagedNativeVersionDocument 表示打包可执行文件返回的机器可读构建身份。
type packagedNativeVersionDocument struct {
	Name              string `json:"name"`
	GoVersion         string `json:"go_version"`
	GOOS              string `json:"goos"`
	GOARCH            string `json:"goarch"`
	SourceRevision    string `json:"source_revision"`
	SourceStateDigest string `json:"source_state_digest"`
}

// threadSafePackagedBuffer captures child-process output without allowing stdout and stderr writers to race with test diagnostics.
// threadSafePackagedBuffer 捕获子进程输出，并避免 stdout 与 stderr 写入和测试诊断之间产生数据竞争。
type threadSafePackagedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends one child-process output fragment under a mutex and implements io.Writer for os/exec.
// Write 在互斥保护下追加一段子进程输出，并为 os/exec 实现 io.Writer 接口。
func (b *threadSafePackagedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns a consistent snapshot of all output captured so far.
// String 返回当前已捕获全部输出的一致性快照。
func (b *threadSafePackagedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestPackagedNativeRuntimeUsesIsolatedNativeArtifacts starts the exact packaged executable and verifies native plus all-profile legacy storage paths.
// TestPackagedNativeRuntimeUsesIsolatedNativeArtifacts 使用精确的打包可执行文件验证原生路径及 all 配置中的 legacy 存储链路。
func TestPackagedNativeRuntimeUsesIsolatedNativeArtifacts(t *testing.T) {
	packagedRoot := packagedNativeRootOrSkip(t)
	binaryPath := filepath.Join(packagedRoot, "bin", packagedNativeExecutableName())
	validatePackagedNativeArtifacts(t, packagedRoot, binaryPath)
	validatePackagedVersionJSON(t, packagedRoot, binaryPath)

	dataRoot := t.TempDir()
	configRoot := filepath.Join(t.TempDir(), "config-root")
	if err := os.MkdirAll(configRoot, 0o700); err != nil {
		t.Fatalf("create temporary config root: %v", err)
	}
	grpcAddress := reservePackagedGRPCAddress(t)

	// The local server makes accidental startup AI traffic observable while keeping every configured provider endpoint deterministic and offline.
	// 本地服务器让意外的启动期 AI 流量可观测，同时保证所有 provider endpoint 都是确定且离线的。
	var embeddingRequests atomic.Int64
	var chatCompletionRequests atomic.Int64
	var unexpectedAIRequests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/embeddings":
			embeddingRequests.Add(1)
			if err := writePackagedDeterministicEmbedding(w, r); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
		case "/v1/chat/completions":
			chatCompletionRequests.Add(1)
			if err := writePackagedDeterministicChatCompletion(w, r); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
		default:
			unexpectedAIRequests.Add(1)
			http.Error(w, "unexpected packaged acceptance provider request", http.StatusInternalServerError)
		}
	}))
	defer provider.Close()

	sqlitePath := filepath.Join(dataRoot, "sqlite.db")
	lancePath := filepath.Join(dataRoot, "lancedb")
	if err := writePackagedAcceptanceConfig(configRoot, grpcAddress, sqlitePath, lancePath, filepath.Join(packagedRoot, "libs", packagedNativeLibraryName(t)), provider.URL); err != nil {
		t.Fatalf("write packaged acceptance config: %v", err)
	}

	cmd, waitCh, stdout, stderr, err := startPackagedRuntime(binaryPath, packagedRoot, configRoot, provider.URL)
	if err != nil {
		t.Fatalf("start packaged runtime: %v", err)
	}
	stopped := false
	t.Cleanup(func() {
		if stopped {
			return
		}
		if stopErr := stopPackagedRuntime(cmd, waitCh); stopErr != nil {
			t.Logf("stop packaged runtime: %v", stopErr)
		}
	})

	conn, err := waitForPackagedHealth(grpcAddress)
	if err != nil {
		t.Fatalf("wait for packaged health: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := vmmv1.NewVMMServiceClient(conn)

	// Admin RPCs exercise the native SQLite relation store without entering an embedding, search, or post-action pipeline.
	// 管理 RPC 只验证原生 SQLite 关系存储，不进入 embedding、搜索或 post-action 流程。
	projectPath := "PackagedIT/NativeStorage/Acceptance"
	projectCtx, projectCancel := context.WithTimeout(context.Background(), 10*time.Second)
	projectResult, err := client.EnsureProject(projectCtx, &vmmv1.EnsureProjectRequest{ProjectPath: projectPath, ConfirmCreate: true})
	projectCancel()
	if err != nil {
		t.Fatalf("ensure project: %v", err)
	}
	if projectResult.GetProject() == nil || projectResult.GetProject().GetProjectId() == 0 || !projectResult.GetCreatedProject() {
		t.Fatalf("ensure project did not create a durable project: %s", projectResult.String())
	}

	projectCtx, projectCancel = context.WithTimeout(context.Background(), 10*time.Second)
	existingProject, err := client.EnsureProject(projectCtx, &vmmv1.EnsureProjectRequest{ProjectPath: projectPath})
	projectCancel()
	if err != nil {
		t.Fatalf("resolve existing project: %v", err)
	}
	if existingProject.GetProject() == nil || existingProject.GetProject().GetProjectId() != projectResult.GetProject().GetProjectId() || !existingProject.GetExists() {
		t.Fatalf("existing project was not resolved from SQLite: %s", existingProject.String())
	}

	userName := "packaged-acceptance-user-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	userCtx, userCancel := context.WithTimeout(context.Background(), 10*time.Second)
	userResult, err := client.ResolveUser(userCtx, &vmmv1.ResolveUserRequest{UserRef: userName, ConfirmCreate: true})
	userCancel()
	if err != nil {
		t.Fatalf("resolve user: %v", err)
	}
	if userResult.GetUser() == nil || userResult.GetUser().GetUserId() == 0 || !userResult.GetCreated() {
		t.Fatalf("resolve user did not create a durable user: %s", userResult.String())
	}

	listCtx, listCancel := context.WithTimeout(context.Background(), 10*time.Second)
	projects, err := client.ListProjects(listCtx, &emptypb.Empty{})
	listCancel()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if !packagedProjectListed(projects.GetProjects(), projectResult.GetProject().GetProjectId()) {
		t.Fatalf("created project is absent from ListProjects: %s", projects.String())
	}

	listCtx, listCancel = context.WithTimeout(context.Background(), 10*time.Second)
	users, err := client.ListUsers(listCtx, &emptypb.Empty{})
	listCancel()
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if !packagedUserListed(users.GetUsers(), userResult.GetUser().GetUserId()) {
		t.Fatalf("created user is absent from ListUsers: %s", users.String())
	}

	// A deterministic local embedding response lets this acceptance test prove the complete native vector write/read path with Chinese content without any external model.
	// 确定性的本地 embedding 响应让验收测试可以在不调用外部模型的情况下验证中文内容的原生向量写入与读取链路。
	sessionID := "packaged-acceptance-session-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	writeCtx, writeCancel := context.WithTimeout(context.Background(), 15*time.Second)
	writeResult, err := client.WriteMemories(writeCtx, &vmmv1.WriteMemoriesRequest{
		SessionId: sessionID,
		UserId:    userResult.GetUser().GetUserId(),
		ProjectId: projectResult.GetProject().GetProjectId(),
		Items: []*vmmv1.WriteMemoryItem{{
			ScopeLevel:  2,
			Abstract:    "原生 SQLite 与 LanceDB 支持中文分词",
			Details:     "打包验收验证中文记忆可以写入并通过原生向量检索返回。",
			Category:    0,
			Priority:    3,
			MemoryLevel: 3,
		}},
	})
	writeCancel()
	if err != nil {
		t.Fatalf("write Chinese memory: %v", err)
	}
	if len(writeResult.GetItems()) != 1 || writeResult.GetItems()[0] == nil || writeResult.GetItems()[0].GetMemoryId() == 0 {
		t.Fatalf("write Chinese memory returned no durable id: %s", writeResult.String())
	}

	searchCtx, searchCancel := context.WithTimeout(context.Background(), 15*time.Second)
	searchResult, err := client.SearchMemoryEvents(searchCtx, &vmmv1.SearchMemoryEventsRequest{
		UserId:    userResult.GetUser().GetUserId(),
		ProjectId: projectResult.GetProject().GetProjectId(),
		Queries:   []string{"中文分词"},
		TopK:      5,
	})
	searchCancel()
	if err != nil {
		t.Fatalf("search Chinese memory: %v", err)
	}
	if !packagedSearchContainsAbstract(searchResult.GetResults(), "原生 SQLite") {
		t.Fatalf("Chinese memory is absent from SearchMemoryEvents: %s", searchResult.String())
	}
	_ = conn.Close()

	assertPackagedNativeDataPaths(t, sqlitePath, lancePath)
	if got := embeddingRequests.Load(); got == 0 {
		t.Fatalf("deterministic embedding endpoint received no request")
	}
	if got := chatCompletionRequests.Load(); got == 0 {
		t.Fatalf("deterministic chat completion endpoint received no request")
	}
	if got := unexpectedAIRequests.Load(); got != 0 {
		t.Fatalf("unexpected background or LLM provider requests: %d", got)
	}

	if err := stopPackagedRuntime(cmd, waitCh); err != nil {
		t.Fatalf("stop packaged runtime: %v", err)
	}
	stopped = true
	combinedOutput := strings.ToLower(stdout.String() + "\n" + stderr.String())
	if strings.Contains(combinedOutput, "vldb-controller") {
		t.Fatalf("packaged native runtime mentioned or started the retired controller:\n%s", combinedOutput)
	}
	if got := unexpectedAIRequests.Load(); got != 0 {
		t.Fatalf("unexpected background or LLM provider requests during shutdown: %d", got)
	}

	// The all-profile package must prove the two legacy modes through the same packaged executable, not only by listing their files.
	// all 配置发行包必须通过同一个打包可执行文件真实验证两种 legacy 模式，不能只检查文件清单。
	if os.Getenv("VMM_PACKAGED_STORAGE_PROFILE") == "all" {
		for _, profile := range []string{"split", "controller"} {
			t.Run("legacy-"+profile, func(t *testing.T) {
				runPackagedLegacyStorageAcceptance(t, packagedRoot, binaryPath, profile, provider.URL)
			})
		}
	}
}

// runPackagedLegacyStorageAcceptance starts one staged release in split or controller mode and proves relational plus vector write/read behavior.
// runPackagedLegacyStorageAcceptance 启动一个暂存发行包的 split 或 controller 模式，并验证关系与向量存储的写入读取。
func runPackagedLegacyStorageAcceptance(t *testing.T, packagedRoot, binaryPath, profile, providerEndpoint string) {
	t.Helper()
	if profile != "split" && profile != "controller" {
		t.Fatalf("unsupported legacy acceptance profile %q", profile)
	}
	dataRoot := t.TempDir()
	configRoot := filepath.Join(t.TempDir(), "config-root")
	if err := os.MkdirAll(configRoot, 0o700); err != nil {
		t.Fatalf("create temporary %s config root: %v", profile, err)
	}
	grpcAddress := reservePackagedGRPCAddress(t)
	controllerEndpoint := ""
	controllerExecutable := ""
	if profile == "controller" {
		controllerEndpoint = "http://" + reservePackagedGRPCAddress(t)
		controllerExecutable = filepath.Join(packagedRoot, "bin", packagedControllerExecutableName())
	}
	if err := writePackagedLegacyAcceptanceConfig(configRoot, grpcAddress, dataRoot, profile, controllerEndpoint, controllerExecutable, providerEndpoint); err != nil {
		t.Fatalf("write packaged %s acceptance config: %v", profile, err)
	}

	cmd, waitCh, stdout, stderr, err := startPackagedRuntime(binaryPath, packagedRoot, configRoot, providerEndpoint)
	if err != nil {
		t.Fatalf("start packaged %s runtime: %v", profile, err)
	}
	stopped := false
	t.Cleanup(func() {
		if stopped {
			return
		}
		if stopErr := stopPackagedRuntime(cmd, waitCh); stopErr != nil {
			t.Logf("stop packaged %s runtime: %v", profile, stopErr)
		}
	})

	conn, err := waitForPackagedHealth(grpcAddress)
	if err != nil {
		t.Fatalf("wait for packaged %s health: %v\nstdout:\n%s\nstderr:\n%s", profile, err, stdout.String(), stderr.String())
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := vmmv1.NewVMMServiceClient(conn)

	// EnsureProject and ResolveUser prove durable relation writes and reads before the vector pipeline is exercised.
	// EnsureProject 与 ResolveUser 先验证关系存储的持久写入读取，再进入向量流水线。
	projectPath := "PackagedIT/Legacy/" + profile
	projectCtx, projectCancel := context.WithTimeout(context.Background(), 10*time.Second)
	projectResult, err := client.EnsureProject(projectCtx, &vmmv1.EnsureProjectRequest{ProjectPath: projectPath, ConfirmCreate: true})
	projectCancel()
	if err != nil {
		t.Fatalf("ensure %s project: %v", profile, err)
	}
	if projectResult.GetProject() == nil || projectResult.GetProject().GetProjectId() == 0 || !projectResult.GetCreatedProject() {
		t.Fatalf("%s project was not durably created: %s", profile, projectResult.String())
	}
	projectCtx, projectCancel = context.WithTimeout(context.Background(), 10*time.Second)
	existingProject, err := client.EnsureProject(projectCtx, &vmmv1.EnsureProjectRequest{ProjectPath: projectPath})
	projectCancel()
	if err != nil {
		t.Fatalf("resolve existing %s project: %v", profile, err)
	}
	if existingProject.GetProject() == nil || existingProject.GetProject().GetProjectId() != projectResult.GetProject().GetProjectId() || !existingProject.GetExists() {
		t.Fatalf("existing %s project was not resolved from storage: %s", profile, existingProject.String())
	}

	userName := "packaged-" + profile + "-user-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	userCtx, userCancel := context.WithTimeout(context.Background(), 10*time.Second)
	userResult, err := client.ResolveUser(userCtx, &vmmv1.ResolveUserRequest{UserRef: userName, ConfirmCreate: true})
	userCancel()
	if err != nil {
		t.Fatalf("resolve %s user: %v", profile, err)
	}
	if userResult.GetUser() == nil || userResult.GetUser().GetUserId() == 0 || !userResult.GetCreated() {
		t.Fatalf("%s user was not durably created: %s", profile, userResult.String())
	}

	// A deterministic local provider keeps the packaged legacy write/search path offline while still exercising embedding and reviewer calls.
	// 确定性的本地 provider 让打包 legacy 写入/检索链路保持离线，同时仍然执行 embedding 与 reviewer 调用。
	sessionID := "packaged-" + profile + "-session-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	writeCtx, writeCancel := context.WithTimeout(context.Background(), 15*time.Second)
	writeResult, err := client.WriteMemories(writeCtx, &vmmv1.WriteMemoriesRequest{
		SessionId: sessionID,
		UserId:    userResult.GetUser().GetUserId(),
		ProjectId: projectResult.GetProject().GetProjectId(),
		Items: []*vmmv1.WriteMemoryItem{{
			ScopeLevel:  2,
			Abstract:    "packaged " + profile + " legacy storage",
			Details:     "offline packaged acceptance memory",
			Category:    0,
			Priority:    3,
			MemoryLevel: 3,
		}},
	})
	writeCancel()
	if err != nil {
		t.Fatalf("write %s memory: %v", profile, err)
	}
	if len(writeResult.GetItems()) != 1 || writeResult.GetItems()[0] == nil || writeResult.GetItems()[0].GetMemoryId() == 0 {
		t.Fatalf("write %s memory returned no durable id: %s", profile, writeResult.String())
	}

	searchCtx, searchCancel := context.WithTimeout(context.Background(), 15*time.Second)
	searchResult, err := client.SearchMemoryEvents(searchCtx, &vmmv1.SearchMemoryEventsRequest{
		UserId:    userResult.GetUser().GetUserId(),
		ProjectId: projectResult.GetProject().GetProjectId(),
		Queries:   []string{"legacy storage"},
		TopK:      5,
	})
	searchCancel()
	if err != nil {
		t.Fatalf("search %s memory: %v", profile, err)
	}
	if !packagedSearchContainsAbstract(searchResult.GetResults(), "packaged "+profile) {
		t.Fatalf("%s memory is absent from SearchMemoryEvents: %s", profile, searchResult.String())
	}
	_ = conn.Close()

	assertPackagedLegacyDataPaths(t, dataRoot)
	if err := stopPackagedRuntime(cmd, waitCh); err != nil {
		t.Fatalf("stop packaged %s runtime: %v", profile, err)
	}
	stopped = true
	combinedOutput := strings.ToLower(stdout.String() + "\n" + stderr.String())
	if strings.Contains(combinedOutput, "panic:") {
		t.Fatalf("packaged %s runtime panicked:\n%s", profile, combinedOutput)
	}
}

// writePackagedLegacyAcceptanceConfig writes the strict override layer for a split or controller packaged run.
// writePackagedLegacyAcceptanceConfig 写入 split 或 controller 打包运行所需的严格覆盖配置层。
func writePackagedLegacyAcceptanceConfig(configRoot, grpcAddress, dataRoot, profile, controllerEndpoint, controllerExecutable, providerEndpoint string) error {
	quote := func(value string) string { return strconv.Quote(filepath.ToSlash(value)) }
	config := []string{
		"grpc:",
		"  listen_addr: " + quote(grpcAddress),
		"storage:",
		"  mode: " + profile,
		"  local_data_root: " + quote(dataRoot),
		"sqlite:",
		"  tokenizer_mode: jieba",
		"lancedb:",
		"  table_name: vmm_memory_vectors",
		"  vector_column: vector",
		"vector:",
		"  provider: lancedb",
		"relational:",
		"  provider: sqlite",
		"embedding:",
		"  provider: openai",
		"  endpoint: " + quote(providerEndpoint+"/v1"),
		"  api_keys:",
		"    - packaged-acceptance-key",
		"  model: packaged-acceptance-embedding",
		"  dimension: 3",
		"  max_batch_size: 4",
		"rerank:",
		"  enabled: false",
		"llm:",
		"  routes:",
		"    - name: packaged-acceptance-route",
		"      provider: openai",
		"      endpoint: " + quote(providerEndpoint+"/v1"),
		"      api_keys:",
		"        - packaged-acceptance-key",
		"      model: packaged-acceptance-llm",
		"      params:",
		"        reasoning_effort: none",
		"noise:",
		"  enabled: false",
		"  semantic_enabled: false",
		"management:",
		"  enabled: false",
		"post_action:",
		"  session_analysis_turn_threshold: 1000000",
		"  session_analysis_token_threshold: 1000000",
		"  session_analysis_idle_timeout: 24h",
		"  max_queue_workers: 1",
		"retention:",
		"  enabled: false",
	}
	if profile == "controller" {
		config = append(config,
			"controller:",
			"  endpoint: "+quote(controllerEndpoint),
			"  auto_spawn: true",
			"  executable: "+quote(controllerExecutable),
			"  process_mode: managed",
			"  minimum_uptime: 1s",
			"  idle_timeout: 1s",
			"  lease_ttl: 2s",
			"  connect_timeout: 2s",
			"  startup_timeout: 20s",
			"  startup_retry_interval: 100ms",
			"  lease_renew_interval: 500ms",
			"  request_timeout: 10s",
			"  space_id: packaged-acceptance-"+profile,
			"  space_label: Packaged Acceptance "+profile,
		)
	}
	config = append(config, "")
	return os.WriteFile(filepath.Join(configRoot, "config.yaml"), []byte(strings.Join(config, "\n")), 0o600)
}

// packagedControllerExecutableName returns the staged controller filename for the current host.
// packagedControllerExecutableName 返回当前主机暂存 controller 的文件名。
func packagedControllerExecutableName() string {
	if runtime.GOOS == "windows" {
		return "vldb-controller.exe"
	}
	return "vldb-controller"
}

// writePackagedDeterministicEmbedding implements the minimal OpenAI-compatible embedding response used by the offline vector acceptance path.
// writePackagedDeterministicEmbedding 实现离线向量验收链路所需的最小 OpenAI 兼容 embedding 响应。
func writePackagedDeterministicEmbedding(w http.ResponseWriter, r *http.Request) error {
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("read embedding request: %w", err)
	}
	var request struct {
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return fmt.Errorf("decode embedding request: %w", err)
	}
	count := 1
	trimmedInput := bytes.TrimSpace(request.Input)
	if len(trimmedInput) > 0 && trimmedInput[0] == '[' {
		var inputs []string
		if err := json.Unmarshal(trimmedInput, &inputs); err != nil {
			return fmt.Errorf("decode embedding input list: %w", err)
		}
		count = len(inputs)
	}
	if count <= 0 {
		return fmt.Errorf("embedding input is empty")
	}
	data := make([]map[string]any, 0, count)
	for index := 0; index < count; index++ {
		data = append(data, map[string]any{
			"object":    "embedding",
			"index":     index,
			"embedding": []float64{0.1, 0.2, 0.3},
		})
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   data,
		"model":  "packaged-acceptance-embedding",
		"usage":  map[string]any{"prompt_tokens": 1, "total_tokens": 1},
	})
}

// writePackagedDeterministicChatCompletion returns the strict reviewer decision needed by direct memory writes without contacting an external model.
// writePackagedDeterministicChatCompletion 返回主动写记忆所需的严格 reviewer 决策，避免访问外部模型。
func writePackagedDeterministicChatCompletion(w http.ResponseWriter, r *http.Request) error {
	defer r.Body.Close()
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		return fmt.Errorf("read chat completion request: %w", err)
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(map[string]any{
		"id":      "chatcmpl-packaged-acceptance",
		"object":  "chat.completion",
		"created": 1,
		"model":   "packaged-acceptance-llm",
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": `{"memory":{"accepted_candidates":[{"candidate_index":0}],"dropped_candidates":[],"reason":"accept packaged native acceptance candidate"}}`,
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	})
}

// packagedNativeRootOrSkip resolves the caller-supplied standard output root and skips the acceptance test when no real package was supplied.
// packagedNativeRootOrSkip 解析调用方提供的标准输出根目录；未提供真实打包产物时跳过验收测试。
func packagedNativeRootOrSkip(t *testing.T) string {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("VMM_NATIVE_PACKAGED_ROOT"))
	if raw == "" {
		t.Skip("VMM_NATIVE_PACKAGED_ROOT is not set; packaged native acceptance requires a standard isolated output directory")
	}
	root, err := filepath.Abs(raw)
	if err != nil {
		t.Fatalf("resolve VMM_NATIVE_PACKAGED_ROOT: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat VMM_NATIVE_PACKAGED_ROOT %q: %v", root, err)
	}
	if !info.IsDir() {
		t.Fatalf("VMM_NATIVE_PACKAGED_ROOT is not a directory: %s", root)
	}
	return filepath.Clean(root)
}

// packagedNativeExecutableName returns the standard binary name for the current host.
// packagedNativeExecutableName 返回当前主机对应的标准可执行文件名。
func packagedNativeExecutableName() string {
	if runtime.GOOS == "windows" {
		return "vmm-local.exe"
	}
	return "vmm-local"
}

// packagedNativeLibraryName returns the one platform-specific library name declared by the application ABI.
// packagedNativeLibraryName 返回应用 ABI 声明的唯一平台原生库文件名。
func packagedNativeLibraryName(t *testing.T) string {
	t.Helper()
	name, err := nativeLanceLibraryName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("resolve native LanceDB library name: %v", err)
	}
	return name
}

// packagedNativeTarget returns the Rust target that the packaging validator associates with the current Go target.
// packagedNativeTarget 返回打包校验器为当前 Go 目标平台关联的 Rust target。
func packagedNativeTarget(t *testing.T) string {
	t.Helper()
	switch {
	case runtime.GOOS == "windows" && runtime.GOARCH == "amd64":
		return "x86_64-pc-windows-msvc"
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		return "x86_64-unknown-linux-gnu"
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64":
		return "aarch64-unknown-linux-gnu"
	case runtime.GOOS == "darwin" && runtime.GOARCH == "amd64":
		return "x86_64-apple-darwin"
	case runtime.GOOS == "darwin" && runtime.GOARCH == "arm64":
		return "aarch64-apple-darwin"
	default:
		t.Fatalf("native packaged target is unsupported on %s/%s", runtime.GOOS, runtime.GOARCH)
		return ""
	}
}

// validatePackagedNativeArtifacts verifies native artifacts and the explicitly selected package dependency profile.
// validatePackagedNativeArtifacts 校验原生工件及明确选择的发行包依赖配置。
func validatePackagedNativeArtifacts(t *testing.T, root, binaryPath string) {
	t.Helper()
	binaryInfo, err := os.Stat(binaryPath)
	if err != nil {
		t.Fatalf("stat packaged executable %q: %v", binaryPath, err)
	}
	if !binaryInfo.Mode().IsRegular() {
		t.Fatalf("packaged executable is not a regular file: %s", binaryPath)
	}

	libsDir := filepath.Join(root, "libs")
	entries, err := os.ReadDir(libsDir)
	if err != nil {
		t.Fatalf("read packaged libs directory %q: %v", libsDir, err)
	}
	// An all-profile release intentionally carries both local storage implementations, while the older native-only acceptance still rejects legacy dependencies.
	// all 配置发行包有意同时携带两套本地存储实现；旧的仅原生验收仍拒绝 legacy 依赖。
	profile := os.Getenv("VMM_PACKAGED_STORAGE_PROFILE")
	if profile != "" && profile != "all" {
		t.Fatalf("unsupported packaged storage profile %q", profile)
	}
	if profile == "" {
		for _, entry := range entries {
			if strings.Contains(strings.ToLower(entry.Name()), "vldb") {
				t.Fatalf("packaged native libs contain a legacy VLDB artifact: %s", entry.Name())
			}
		}
	} else {
		for _, name := range packagedLegacyLibraryNames(t) {
			info, err := os.Stat(filepath.Join(libsDir, name))
			if err != nil || !info.Mode().IsRegular() {
				t.Fatalf("all-profile package is missing regular legacy library %s: %v", name, err)
			}
		}
	}

	libraryName := packagedNativeLibraryName(t)
	libraryPath := filepath.Join(libsDir, libraryName)
	libraryInfo, err := os.Stat(libraryPath)
	if err != nil {
		t.Fatalf("stat packaged native library %q: %v", libraryPath, err)
	}
	if !libraryInfo.Mode().IsRegular() {
		t.Fatalf("packaged native library is not a regular file: %s", libraryPath)
	}

	manifestPath := filepath.Join(libsDir, "manifest.json")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read packaged native manifest %q: %v", manifestPath, err)
	}
	var manifest packagedNativeManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode packaged native manifest: %v", err)
	}
	if manifest.SchemaVersion != 1 || manifest.ABIVersion != 1 {
		t.Fatalf("unexpected packaged native manifest schema/ABI: %+v", manifest)
	}
	if manifest.EngineVersion != "0.39.0" {
		t.Fatalf("unexpected packaged native engine version: %q", manifest.EngineVersion)
	}
	if manifest.Target != packagedNativeTarget(t) || manifest.LibraryFile != libraryName {
		t.Fatalf("packaged native manifest does not match host/library: %+v", manifest)
	}
	if len(manifest.LibrarySHA256) != sha256.Size*2 {
		t.Fatalf("packaged native manifest has invalid library hash: %q", manifest.LibrarySHA256)
	}
	if _, err := hex.DecodeString(manifest.LibrarySHA256); err != nil {
		t.Fatalf("packaged native manifest library hash is not hexadecimal: %v", err)
	}
	libraryBytes, err := os.ReadFile(libraryPath)
	if err != nil {
		t.Fatalf("read packaged native library: %v", err)
	}
	actualHash := sha256.Sum256(libraryBytes)
	if !strings.EqualFold(manifest.LibrarySHA256, hex.EncodeToString(actualHash[:])) {
		t.Fatalf("packaged native library hash mismatch: manifest=%s actual=%s", manifest.LibrarySHA256, hex.EncodeToString(actualHash[:]))
	}

	binEntries, err := os.ReadDir(filepath.Join(root, "bin"))
	if err != nil {
		t.Fatalf("read packaged bin directory: %v", err)
	}
	if profile == "" {
		for _, entry := range binEntries {
			if strings.Contains(strings.ToLower(entry.Name()), "vldb-controller") {
				t.Fatalf("packaged native output contains a controller executable: %s", entry.Name())
			}
		}
	} else {
		name := "vldb-controller"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		info, err := os.Stat(filepath.Join(root, "bin", name))
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("all-profile package is missing regular controller %s: %v", name, err)
		}
	}
}

// packagedLegacyLibraryNames returns the platform-specific dependencies required by the all-profile release.
// packagedLegacyLibraryNames 返回全配置发行包在当前平台必须包含的依赖库名称。
func packagedLegacyLibraryNames(t *testing.T) []string {
	t.Helper()
	switch runtime.GOOS {
	case "windows":
		return []string{"vldb_sqlite.dll", "vldb_lancedb.dll"}
	case "linux":
		return []string{"libvldb_sqlite.so", "libvldb_lancedb.so"}
	case "darwin":
		return []string{"libvldb_sqlite.dylib", "libvldb_lancedb.dylib"}
	default:
		t.Fatalf("unsupported packaged legacy library platform %s", runtime.GOOS)
		return nil
	}
}

// validatePackagedVersionJSON runs the packaged binary itself so the acceptance result includes its build identity contract.
// validatePackagedVersionJSON 直接运行打包二进制，确保验收结果包含其构建身份契约。
func validatePackagedVersionJSON(t *testing.T, root, binaryPath string) {
	t.Helper()
	cmd := exec.Command(binaryPath, "-version-json")
	cmd.Dir = filepath.Join(root, "bin")
	cmd.Env = filteredPackagedEnvironment()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run packaged -version-json: %v\noutput:\n%s", err, output)
	}
	var document packagedNativeVersionDocument
	if err := json.Unmarshal(bytes.TrimSpace(output), &document); err != nil {
		t.Fatalf("decode packaged version JSON: %v\noutput:\n%s", err, output)
	}
	if document.Name != "vmm-local" || document.GoVersion == "" || document.GOOS != runtime.GOOS || document.GOARCH != runtime.GOARCH || document.SourceRevision == "" || document.SourceStateDigest == "" {
		t.Fatalf("packaged version identity mismatch: %+v", document)
	}
}

// writePackagedAcceptanceConfig writes only the temporary override layer needed to force native storage and offline synthetic providers.
// writePackagedAcceptanceConfig 只写入强制原生存储与离线合成 provider 所需的临时覆盖配置层。
func writePackagedAcceptanceConfig(configRoot, grpcAddress, sqlitePath, lancePath, libraryPath, providerEndpoint string) error {
	quote := func(value string) string { return strconv.Quote(filepath.ToSlash(value)) }
	config := strings.Join([]string{
		"grpc:",
		"  listen_addr: " + quote(grpcAddress),
		"storage:",
		"  mode: native",
		"sqlite:",
		"  native:",
		"    path: " + quote(sqlitePath),
		"    tokenizer: gse",
		"lancedb:",
		"  native:",
		"    path: " + quote(lancePath),
		"    library_path: " + quote(libraryPath),
		"vector:",
		"  provider: lancedb",
		"relational:",
		"  provider: sqlite",
		"embedding:",
		"  provider: openai",
		"  endpoint: " + quote(providerEndpoint+"/v1"),
		"  api_keys:",
		"    - packaged-acceptance-key",
		"  model: packaged-acceptance-embedding",
		"  dimension: 3",
		"  max_batch_size: 4",
		"rerank:",
		"  enabled: false",
		"llm:",
		"  routes:",
		"    - name: packaged-acceptance-route",
		"      provider: openai",
		"      endpoint: " + quote(providerEndpoint+"/v1"),
		"      api_keys:",
		"        - packaged-acceptance-key",
		"      model: packaged-acceptance-llm",
		"      params:",
		"        reasoning_effort: none",
		"noise:",
		"  enabled: false",
		"  semantic_enabled: false",
		"management:",
		"  enabled: false",
		"controller:",
		"  auto_spawn: false",
		"post_action:",
		"  session_analysis_turn_threshold: 1000000",
		"  session_analysis_token_threshold: 1000000",
		"  session_analysis_idle_timeout: 24h",
		"  max_queue_workers: 1",
		"retention:",
		"  enabled: false",
		"",
	}, "\n")
	return os.WriteFile(filepath.Join(configRoot, "config.yaml"), []byte(config), 0o600)
}

// filteredPackagedEnvironment removes every VMM_* variable so user-level runtime configuration cannot enter the isolated child process.
// filteredPackagedEnvironment 移除全部 VMM_* 变量，避免用户级运行时配置进入隔离子进程。
func filteredPackagedEnvironment() []string {
	filtered := make([]string, 0, len(os.Environ()))
	for _, item := range os.Environ() {
		key, _, found := strings.Cut(item, "=")
		if found && packagedEnvironmentKeyIsIsolated(key) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

// packagedEnvironmentKeyIsIsolated identifies environment keys that must never leak from the user process into the packaged child.
// packagedEnvironmentKeyIsIsolated 标识绝不能从用户进程泄漏到打包子进程的环境变量。
func packagedEnvironmentKeyIsIsolated(key string) bool {
	upperKey := strings.ToUpper(key)
	if strings.HasPrefix(upperKey, "VMM_") {
		return true
	}
	switch upperKey {
	case "BAILIAN_API_KEY", "BAILIAN_BASE_URL", "BAILIAN_RERANK_URL", "DEEPSEEK_API_KEY":
		return true
	default:
		return false
	}
}

// packagedAcceptanceEnvironment supplies non-secret placeholders for every required packaged configuration reference and points provider traffic at the local test server.
// packagedAcceptanceEnvironment 为打包配置中所有必需引用提供非秘密占位值，并将 provider 流量指向本地测试服务。
func packagedAcceptanceEnvironment(providerEndpoint string) []string {
	env := filteredPackagedEnvironment()
	endpoint := strings.TrimRight(providerEndpoint, "/")
	env = append(env,
		"BAILIAN_API_KEY=packaged-acceptance-key",
		"BAILIAN_BASE_URL="+endpoint+"/v1",
		"BAILIAN_RERANK_URL="+endpoint+"/v1/rerank",
		"DEEPSEEK_API_KEY=packaged-acceptance-key",
		"VMM_POSTGRES_DSN=postgres://127.0.0.1:1/packaged_acceptance_unused",
		"VMM_SQLITE_ADDRESS=127.0.0.1:0",
		"VMM_LANCEDB_ADDRESS=127.0.0.1:0",
	)
	return env
}

// reservePackagedGRPCAddress obtains an unused loopback address before the child starts so the fixed config can be checked through gRPC.
// reservePackagedGRPCAddress 在子进程启动前获取未占用回环地址，以便通过固定配置检查 gRPC。
func reservePackagedGRPCAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve packaged gRPC address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release reserved packaged gRPC address: %v", err)
	}
	return address
}

// startPackagedRuntime starts the exact packaged executable with a filtered environment and captures both output streams.
// startPackagedRuntime 使用过滤后的环境启动精确打包可执行文件，并捕获 stdout 与 stderr 两条输出流。
func startPackagedRuntime(binaryPath, packagedRoot, configRoot, providerEndpoint string) (*exec.Cmd, <-chan error, *threadSafePackagedBuffer, *threadSafePackagedBuffer, error) {
	stdout := &threadSafePackagedBuffer{}
	stderr := &threadSafePackagedBuffer{}
	cmd := exec.Command(binaryPath, "-config", configRoot)
	cmd.Dir = filepath.Join(packagedRoot, "bin")
	cmd.Env = packagedAcceptanceEnvironment(providerEndpoint)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, stdout, stderr, err
	}
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	return cmd, waitCh, stdout, stderr, nil
}

// waitForPackagedHealth polls the child gRPC endpoint until storage-backed Healthz succeeds or the bounded deadline expires.
// waitForPackagedHealth 轮询子进程 gRPC endpoint，直到存储支持的 Healthz 成功或达到有界截止时间。
func waitForPackagedHealth(address string) (*grpc.ClientConn, error) {
	deadline := time.Now().Add(packagedNativeHealthTimeout)
	for time.Now().Before(deadline) {
		dialContext, dialCancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		conn, err := grpc.DialContext(dialContext, address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
		dialCancel()
		if err == nil {
			healthContext, healthCancel := context.WithTimeout(context.Background(), 2*time.Second)
			response, healthErr := vmmv1.NewVMMServiceClient(conn).Healthz(healthContext, &emptypb.Empty{})
			healthCancel()
			if healthErr == nil && response.GetStatus() == "ok" {
				return conn, nil
			}
			_ = conn.Close()
		}
		time.Sleep(150 * time.Millisecond)
	}
	return nil, fmt.Errorf("Healthz did not become ready at %s within %s", address, packagedNativeHealthTimeout)
}

// stopPackagedRuntime interrupts the child when possible, then forcefully kills it after a short bounded grace period.
// stopPackagedRuntime 尽可能先中断子进程，并在短暂有界宽限期后强制结束它。
func stopPackagedRuntime(cmd *exec.Cmd, waitCh <-chan error) error {
	if cmd == nil || waitCh == nil {
		return nil
	}
	select {
	case <-waitCh:
		return nil
	default:
	}
	if cmd.Process == nil {
		return nil
	}
	if runtime.GOOS == "windows" {
		_ = cmd.Process.Kill()
	} else {
		_ = cmd.Process.Signal(os.Interrupt)
	}
	select {
	case <-waitCh:
		return nil
	case <-time.After(packagedNativeStopTimeout):
	}
	_ = cmd.Process.Kill()
	select {
	case <-waitCh:
		return nil
	case <-time.After(packagedNativeStopTimeout):
		return fmt.Errorf("packaged child did not exit after termination")
	}
}

// packagedProjectListed checks that the newly created project is present in the relation-backed admin listing.
// packagedProjectListed 检查新创建项目是否出现在关系存储支持的管理列表中。
func packagedProjectListed(projects []*vmmv1.ProjectEntry, projectID uint64) bool {
	for _, project := range projects {
		if project != nil && project.GetProjectId() == projectID {
			return true
		}
	}
	return false
}

// packagedUserListed checks that the newly created user is present in the relation-backed admin listing.
// packagedUserListed 检查新创建用户是否出现在关系存储支持的管理列表中。
func packagedUserListed(users []*vmmv1.UserEntry, userID uint64) bool {
	for _, user := range users {
		if user != nil && user.GetUserId() == userID {
			return true
		}
	}
	return false
}

// packagedSearchContainsAbstract checks that at least one vector-search hit contains the written Chinese memory text.
// packagedSearchContainsAbstract 检查向量搜索命中中至少有一条包含刚写入的中文记忆文本。
func packagedSearchContainsAbstract(groups []*vmmv1.MemorySearchGroupResult, fragment string) bool {
	for _, group := range groups {
		if group == nil {
			continue
		}
		for _, hit := range group.GetHits() {
			if hit != nil && strings.Contains(hit.GetAbstract(), fragment) {
				return true
			}
		}
	}
	return false
}

// assertPackagedNativeDataPaths proves that startup created the pair only under the temporary paths supplied by this test.
// assertPackagedNativeDataPaths 证明启动仅在本测试提供的临时路径下创建了原生数据配对文件。
func assertPackagedNativeDataPaths(t *testing.T, sqlitePath, lancePath string) {
	t.Helper()
	for _, path := range []string{sqlitePath, sqlitePath + ".pair.json", filepath.Join(lancePath, ".vmm-pair.json")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("expected packaged native path was not created: %s: %v", path, err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("expected packaged native path is not a regular file: %s", path)
		}
	}
}

// assertPackagedLegacyDataPaths proves that split/controller writes reached the selected external data root.
// assertPackagedLegacyDataPaths 证明 split/controller 写入已经落到所选的外部数据根目录。
func assertPackagedLegacyDataPaths(t *testing.T, dataRoot string) {
	t.Helper()
	sqlitePath := filepath.Join(dataRoot, "sqlite.db")
	info, err := os.Stat(sqlitePath)
	if err != nil {
		t.Fatalf("expected packaged legacy SQLite path was not created: %s: %v", sqlitePath, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("expected packaged legacy SQLite path is not a regular file: %s", sqlitePath)
	}
	lancePath := filepath.Join(dataRoot, "lancedb")
	info, err = os.Stat(lancePath)
	if err != nil {
		t.Fatalf("expected packaged legacy LanceDB path was not created: %s: %v", lancePath, err)
	}
	if !info.IsDir() {
		t.Fatalf("expected packaged legacy LanceDB path is not a directory: %s", lancePath)
	}
	entries, err := os.ReadDir(lancePath)
	if err != nil {
		t.Fatalf("read packaged legacy LanceDB path: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("packaged legacy LanceDB path is empty: %s", lancePath)
	}
}
