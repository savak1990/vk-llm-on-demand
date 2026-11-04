package gateway

import (
	"encoding/json"
	"net/http"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

func writeError(w http.ResponseWriter, status int, errType, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: errorDetail{Message: message, Type: errType, Code: code}})
}

// backendUnavailableBody is byte-exact so an SDK's retry/exception logic
// sees identical bytes every time; json.Marshal would not reproduce this
// spacing.
const backendUnavailableBody = `{"error": {"type": "server_error", "code": "backend_unavailable", "message": "GPU backend is stopped; start it with ` + "`vk-llm start`" + `"}}`

func writeBackendUnavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(backendUnavailableBody))
}

func backendUnavailableSSE() string {
	return "data: " + backendUnavailableBody + "\n\ndata: [DONE]\n\n"
}
