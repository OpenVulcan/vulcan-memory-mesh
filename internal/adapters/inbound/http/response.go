package httpapi

type Envelope struct {
	Code    int    `json:"code"`
	Msg     string `json:"msg"`
	Data    any    `json:"data,omitempty"`
	TraceID string `json:"trace_id"`
}
