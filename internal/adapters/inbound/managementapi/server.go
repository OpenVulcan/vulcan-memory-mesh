// server.go implements the isolated HTTP management surface for human-facing VMM administration.
// server.go 用于实现面向人工管理的隔离 VMM HTTP 管理入口。
package managementapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/openvulcan/vmm/internal/app/usecase"
	"github.com/openvulcan/vmm/internal/config"
)

const (
	// ProtocolVersion identifies the stable management HTTP contract exposed by this build.
	// ProtocolVersion 用于标识当前构建暴露的稳定管理 HTTP 契约版本。
	ProtocolVersion = 1

	// capabilitiesPath is the authenticated discovery endpoint for management clients.
	// capabilitiesPath 是管理客户端使用的鉴权能力发现端点。
	capabilitiesPath = "/management/v1/capabilities"
)

// Capabilities describes management limits and storage behavior without exposing credentials or runtime payloads.
// Capabilities 用于描述管理限制与存储行为，且不会暴露凭据或运行时负载。
type Capabilities struct {
	ProtocolVersion     int      `json:"protocol_version"`
	ServiceVersion      string   `json:"service_version"`
	Resources           []string `json:"resources"`
	TrashRetentionHours int64    `json:"trash_retention_hours"`
	MaxPageSize         int      `json:"max_page_size"`
	ContentChunkBytes   int      `json:"content_chunk_bytes"`
	FullTextIndexStatus string   `json:"full_text_index_status"`
	RestoreSupported    bool     `json:"restore_supported"`
	StorageMode         string   `json:"storage_mode"`
}

// Problem is the RFC 9457-compatible error envelope returned by every management endpoint.
// Problem 是每个管理端点统一返回的 RFC 9457 兼容错误信封。
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Code      string `json:"code"`
	Detail    string `json:"detail"`
	RequestID string `json:"request_id"`
	Retryable bool   `json:"retryable"`
}

// Dependencies contains the immutable configuration required by the management HTTP server.
// Dependencies 用于保存管理 HTTP 服务所需的不可变配置。
type Dependencies struct {
	Config       config.ManagementConfig
	Capabilities Capabilities
	Management   *usecase.ManagementUseCase
}

// NewServer builds an authenticated management HTTP server without binding a listener.
// NewServer 用于构建带鉴权的管理 HTTP 服务，但不会在此阶段绑定监听器。
func NewServer(deps Dependencies) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc(capabilitiesPath, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writeProblem(writer, request, http.StatusMethodNotAllowed, "method-not-allowed", "Method not allowed", "This endpoint only accepts GET requests.")
			return
		}
		writeJSON(writer, http.StatusOK, deps.Capabilities)
	})
	registerManagementReadRoutes(mux, deps.Management)
	registerManagementWriteRoutes(mux, deps.Management)
	handler := requestIDMiddleware(authenticationMiddleware(deps.Config.AccessToken, requestLimitMiddleware(deps.Config.MaxRequestBodyBytes, mux)))
	return &http.Server{
		Addr:              deps.Config.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: deps.Config.ReadHeaderTimeout.Duration,
		ReadTimeout:       deps.Config.RequestTimeout.Duration,
		WriteTimeout:      deps.Config.RequestTimeout.Duration,
		IdleTimeout:       deps.Config.IdleTimeout.Duration,
	}
}

// authenticationMiddleware enforces an exact Bearer token match using constant-time digest comparison.
// authenticationMiddleware 使用恒定时间摘要比较强制校验精确的 Bearer Token。
func authenticationMiddleware(expectedToken string, next http.Handler) http.Handler {
	expectedDigest := sha256.Sum256([]byte(expectedToken))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		const prefix = "Bearer "
		header := request.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) {
			writeProblem(writer, request, http.StatusUnauthorized, "unauthorized", "Unauthorized", "A valid management access token is required.")
			return
		}
		actualDigest := sha256.Sum256([]byte(strings.TrimPrefix(header, prefix)))
		if subtle.ConstantTimeCompare(expectedDigest[:], actualDigest[:]) != 1 {
			writeProblem(writer, request, http.StatusUnauthorized, "unauthorized", "Unauthorized", "A valid management access token is required.")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

// requestLimitMiddleware bounds every request body before an endpoint can decode it.
// requestLimitMiddleware 用于在端点解码请求体前限制每个请求体的最大字节数。
func requestLimitMiddleware(maxBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		request.Body = http.MaxBytesReader(writer, request.Body, maxBytes)
		next.ServeHTTP(writer, request)
	})
}

// requestIDMiddleware assigns one opaque request identifier and returns it in every response.
// requestIDMiddleware 用于分配不透明请求标识，并在每个响应中返回该标识。
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestID := newRequestID()
		request.Header.Set("X-VMM-Request-ID", requestID)
		writer.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(writer, request)
	})
}

// newRequestID returns a random request identifier without embedding host, process, or credential information.
// newRequestID 用于返回随机请求标识，且不会嵌入主机、进程或凭据信息。
func newRequestID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(buffer)
}

// writeProblem serializes one uniform management error without reflecting request secrets.
// writeProblem 用于序列化统一管理错误，且不会回显请求中的敏感信息。
func writeProblem(writer http.ResponseWriter, request *http.Request, status int, problemType, title, detail string) {
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(Problem{
		Type:      "https://openvulcan.dev/problems/" + problemType,
		Title:     title,
		Status:    status,
		Code:      strings.ReplaceAll(problemType, "-", "_"),
		Detail:    detail,
		RequestID: request.Header.Get("X-VMM-Request-ID"),
		Retryable: false,
	})
}

// writeJSON serializes one successful management response with a fixed JSON content type.
// writeJSON 用于以固定 JSON 内容类型序列化成功的管理响应。
func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
