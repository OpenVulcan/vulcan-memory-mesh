// server_test.go verifies management authentication, discovery, request identity, and method isolation.
// server_test.go 用于验证管理鉴权、能力发现、请求标识与方法隔离。
package managementapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openvulcan/vmm/internal/app/usecase"
	"github.com/openvulcan/vmm/internal/config"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// managementHTTPReadStore supplies deterministic string-safe ids and bounded content to transport tests.
// managementHTTPReadStore 为传输测试提供确定的字符串安全标识与有界内容。
type managementHTTPReadStore struct{}

// TestProjectManagementTurnProjectsPassedStatus verifies terminal analysis failures are never mislabeled as pending in HTTP responses.
// TestProjectManagementTurnProjectsPassedStatus 用于验证终止分析失败不会在 HTTP 响应中被错误标记为 pending。
func TestProjectManagementTurnProjectsPassedStatus(t *testing.T) {
	management, err := usecase.NewManagementUseCase(&managementHTTPReadStore{}, 30, 100)
	if err != nil {
		t.Fatalf("NewManagementUseCase() error = %v", err)
	}
	response := projectManagementTurn(management, logicdomain.ManagementTurnRecord{
		SessionTurnRecord: logicdomain.SessionTurnRecord{
			ID:              9,
			SessionID:       10,
			ExtractedStatus: logicdomain.TurnExtractedStatusPassed,
		},
	})
	if response.ExtractionStatus != "passed" {
		t.Fatalf("extraction status = %q, want passed", response.ExtractionStatus)
	}
}

// ListManagementSessions returns one identifier above JavaScript's safe integer range.
// ListManagementSessions 返回一个超过 JavaScript 安全整数范围的标识。
func (*managementHTTPReadStore) ListManagementSessions(context.Context, logicdomain.ManagementSessionQuery) (logicdomain.ManagementSessionPage, error) {
	return logicdomain.ManagementSessionPage{Items: []logicdomain.ManagementSessionRecord{{
		SessionRef: logicdomain.SessionRef{
			SessionID: 9007199254740993, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			UserID: 1, UserName: "测试用户", ProjectID: 2, ProjectName: "测试项目", TeamName: "团队", SpaceName: "空间",
		},
	}}}, nil
}

// GetManagementSession returns the same deterministic session projection.
// GetManagementSession 返回同一条确定的会话投影。
func (s *managementHTTPReadStore) GetManagementSession(ctx context.Context, _ uint64) (logicdomain.ManagementSessionRecord, error) {
	page, _ := s.ListManagementSessions(ctx, logicdomain.ManagementSessionQuery{})
	return page.Items[0], nil
}

// ListManagementTurns returns one bounded turn.
// ListManagementTurns 返回一条有界回合。
func (*managementHTTPReadStore) ListManagementTurns(context.Context, logicdomain.ManagementTurnQuery) (logicdomain.ManagementTurnPage, error) {
	return logicdomain.ManagementTurnPage{}, nil
}

// GetManagementTurn returns one UTF-8 content fixture.
// GetManagementTurn 返回一条 UTF-8 内容夹具。
func (*managementHTTPReadStore) GetManagementTurn(context.Context, uint64) (logicdomain.ManagementTurnRecord, error) {
	return logicdomain.ManagementTurnRecord{SessionTurnRecord: logicdomain.SessionTurnRecord{
		ID: 9, SessionID: 9007199254740993, DehydratedContent: `{"user":"火焰测试","timeline":[],"assistant":"完成"}`,
	}}, nil
}

// ListUsers returns an empty bounded identity list.
// ListUsers 返回空的有界身份列表。
func (*managementHTTPReadStore) ListUsers(context.Context) ([]logicdomain.UserRecord, error) {
	return []logicdomain.UserRecord{}, nil
}

// ListProjects returns an empty bounded project list.
// ListProjects 返回空的有界项目列表。
func (*managementHTTPReadStore) ListProjects(context.Context) ([]logicdomain.ProjectRecord, error) {
	return []logicdomain.ProjectRecord{}, nil
}

// testServer builds one deterministic management handler for endpoint-level tests.
// testServer 用于为端点级测试构建确定性的管理处理器。
func testServer() *http.Server {
	return NewServer(Dependencies{
		Config: config.ManagementConfig{
			Enabled:             true,
			ListenAddr:          "127.0.0.1:0",
			AccessToken:         "management-secret",
			MaxRequestBodyBytes: 1 << 20,
			DefaultPageSize:     30,
			MaxPageSize:         100,
			ReadHeaderTimeout:   config.Duration{Duration: 5 * time.Second},
			RequestTimeout:      config.Duration{Duration: 30 * time.Second},
			IdleTimeout:         config.Duration{Duration: 60 * time.Second},
		},
		Capabilities: Capabilities{
			ProtocolVersion:     ProtocolVersion,
			ServiceVersion:      "test",
			Resources:           []string{"sessions", "turns"},
			TrashRetentionHours: 720,
			MaxPageSize:         100,
			ContentChunkBytes:   256 << 10,
			StorageMode:         "controller",
		},
	})
}

// testReadServer builds one authenticated server with the real management use case attached.
// testReadServer 构建一个接入真实管理用例的鉴权服务。
func testReadServer(t *testing.T) *http.Server {
	return testReadServerWithBodyLimit(t, 1<<20)
}

// testReadServerWithBodyLimit builds one authenticated read server with an explicit request-body ceiling.
// testReadServerWithBodyLimit 构建一个带显式请求体上限的鉴权读取服务。
func testReadServerWithBodyLimit(t *testing.T, maxRequestBodyBytes int64) *http.Server {
	t.Helper()
	management, err := usecase.NewManagementUseCase(&managementHTTPReadStore{}, 30, 100)
	if err != nil {
		t.Fatalf("NewManagementUseCase() error = %v", err)
	}
	dependencies := Dependencies{
		Config: config.ManagementConfig{
			Enabled: true, ListenAddr: "127.0.0.1:0", AccessToken: "management-secret", MaxRequestBodyBytes: maxRequestBodyBytes,
			DefaultPageSize: 30, MaxPageSize: 100, ReadHeaderTimeout: config.Duration{Duration: 5 * time.Second},
			RequestTimeout: config.Duration{Duration: 30 * time.Second}, IdleTimeout: config.Duration{Duration: 60 * time.Second},
		},
		Capabilities: Capabilities{ProtocolVersion: ProtocolVersion, ServiceVersion: "test", Resources: []string{"sessions", "turns"}, MaxPageSize: 100},
		Management:   management,
	}
	return NewServer(dependencies)
}

// TestManagementWriteRejectsOversizedBodies verifies request limits produce an explicit 413 problem before mutation dispatch.
// TestManagementWriteRejectsOversizedBodies 验证请求限制会在变更分派前返回明确的 413 问题。
func TestManagementWriteRejectsOversizedBodies(t *testing.T) {
	server := testReadServerWithBodyLimit(t, 8)
	request := httptest.NewRequest(http.MethodPost, "/management/v1/removal-previews", strings.NewReader(`{"target_type":"session"}`))
	request.Header.Set("Authorization", "Bearer management-secret")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge || !strings.Contains(response.Body.String(), `"code":"request_too_large"`) {
		t.Fatalf("response = status %d body %s", response.Code, response.Body.String())
	}
}

// TestCapabilitiesRequiresExactBearerToken verifies missing, malformed, and incorrect credentials share one safe 401 response.
// TestCapabilitiesRequiresExactBearerToken 用于验证缺失、格式错误和不正确的凭据都返回统一安全的 401 响应。
func TestCapabilitiesRequiresExactBearerToken(t *testing.T) {
	server := testServer()
	for _, authorization := range []string{"", "Basic management-secret", "Bearer wrong-secret"} {
		request := httptest.NewRequest(http.MethodGet, capabilitiesPath, nil)
		request.Header.Set("Authorization", authorization)
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("authorization %q status = %d, want %d", authorization, response.Code, http.StatusUnauthorized)
		}
		if strings.Contains(response.Body.String(), "management-secret") || response.Header().Get("X-Request-ID") == "" {
			t.Fatalf("unsafe unauthorized response = headers %#v body %q", response.Header(), response.Body.String())
		}
	}
}

// TestCapabilitiesReturnsVersionedLimits verifies a valid management caller receives the stable discovery envelope.
// TestCapabilitiesReturnsVersionedLimits 用于验证合法管理调用方能收到稳定的能力发现信封。
func TestCapabilitiesReturnsVersionedLimits(t *testing.T) {
	server := testServer()
	request := httptest.NewRequest(http.MethodGet, capabilitiesPath, nil)
	request.Header.Set("Authorization", "Bearer management-secret")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers = %#v", response.Header())
	}
	var capabilities Capabilities
	if err := json.Unmarshal(response.Body.Bytes(), &capabilities); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if capabilities.ProtocolVersion != ProtocolVersion || capabilities.MaxPageSize != 100 || capabilities.StorageMode != "controller" {
		t.Fatalf("capabilities = %#v", capabilities)
	}
}

// TestCapabilitiesRejectsNonGetMethods verifies discovery cannot be repurposed as a write endpoint.
// TestCapabilitiesRejectsNonGetMethods 用于验证能力发现端点不能被误用为写入端点。
func TestCapabilitiesRejectsNonGetMethods(t *testing.T) {
	server := testServer()
	request := httptest.NewRequest(http.MethodPost, capabilitiesPath, strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer management-secret")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("response = status %d headers %#v body %q", response.Code, response.Header(), response.Body.String())
	}
}

// TestSessionListKeepsIdentifiersAsDecimalStrings verifies JSON transport never rounds database identifiers.
// TestSessionListKeepsIdentifiersAsDecimalStrings 验证 JSON 传输不会舍入数据库标识。
func TestSessionListKeepsIdentifiersAsDecimalStrings(t *testing.T) {
	server := testReadServer(t)
	request := httptest.NewRequest(http.MethodGet, "/management/v1/sessions?status=all", nil)
	request.Header.Set("Authorization", "Bearer management-secret")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"session_id":"9007199254740993"`) {
		t.Fatalf("response = status %d body %s", response.Code, response.Body.String())
	}
}

// TestTurnContentUsesUTF8SafeChunks verifies the HTTP endpoint preserves code-point boundaries.
// TestTurnContentUsesUTF8SafeChunks 验证 HTTP 端点保持 UTF-8 码点边界。
func TestTurnContentUsesUTF8SafeChunks(t *testing.T) {
	server := testReadServer(t)
	request := httptest.NewRequest(http.MethodGet, "/management/v1/turns/9/content?section=user&offset=0&limit=4", nil)
	request.Header.Set("Authorization", "Bearer management-secret")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = status %d body %s", response.Code, response.Body.String())
	}
	var content managementTurnContentResponse
	if err := json.Unmarshal(response.Body.Bytes(), &content); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if content.Chunk != "火" || !content.HasMore || content.NextOffset != len([]byte("火")) {
		t.Fatalf("content = %+v", content)
	}
}
