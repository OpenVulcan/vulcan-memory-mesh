// response.go implements the inbound HTTP adapter layer.
// response.go 用于实现入站 HTTP 适配层。
package httpapi

import (
	"encoding/json"
	"net/http"
)

// Envelope is the shared HTTP response wrapper returned by every public endpoint.
// Envelope 用于表示所有公开接口统一返回的 HTTP 响应包。
type Envelope struct {
	Code          int    `json:"code"`
	Msg           string `json:"msg"`
	ErrorID       string `json:"error_id,omitempty"`
	ErrorCategory string `json:"error_category,omitempty"`
	Data          any    `json:"data,omitempty"`
	TraceID       string `json:"trace_id"`
}

// writeJSON executes the writeJSON logic.
// writeJSON 用于执行 writeJSON 逻辑。
func writeJSON(w http.ResponseWriter, status int, payload Envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
