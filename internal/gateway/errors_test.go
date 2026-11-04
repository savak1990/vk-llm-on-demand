package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestErrorBodyShape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusNotFound, "invalid_request_error", "model_not_found", "the model does not exist")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body struct {
		Error struct {
			Message, Type, Code string
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Message == "" || body.Error.Type == "" || body.Error.Code == "" {
		t.Fatalf("got empty field(s) in %+v", body.Error)
	}
}

func TestBackendUnavailableBodyIsVerbatim(t *testing.T) {
	rec := httptest.NewRecorder()
	writeBackendUnavailable(rec)

	want := `{"error": {"type": "server_error", "code": "backend_unavailable", "message": "GPU backend is stopped; start it with ` + "`vk-llm start`" + `"}}`
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") != "" {
		t.Fatal("backend_unavailable must not set Retry-After")
	}
	if rec.Body.String() != want {
		t.Fatalf("body = %q, want %q", rec.Body.String(), want)
	}
}
