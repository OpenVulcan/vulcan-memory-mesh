package httpapi

import (
	"encoding/json"
	"net/http"
)

type Envelope struct {
	Code    int    `json:"code"`
	Msg     string `json:"msg"`
	Data    any    `json:"data,omitempty"`
	TraceID string `json:"trace_id"`
}

func writeJSON(w http.ResponseWriter, status int, payload Envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
