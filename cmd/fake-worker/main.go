package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"
)

func newHandler(readyAfter time.Duration, modelAlias string, now func() time.Time) http.Handler {
	mux := http.NewServeMux()
	start := now()

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		if now().Sub(start) < readyAfter {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": modelAlias}},
		})
	})

	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		if !body.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "fake response"}}},
			})
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, chunk := range []string{"fake ", "streamed ", "response"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", chunk)
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	})

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "llamacpp:prompt_tokens_total 0\nllamacpp:generated_tokens_total 0\n")
	})

	return mux
}

func main() {
	readyAfter := 0 * time.Second
	if v := os.Getenv("READY_AFTER"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			readyAfter = d
		}
	}
	alias := os.Getenv("MODEL_ALIAS")
	if alias == "" {
		alias = "qwen3.6-35b-a3b"
	}

	addr := ":8080"
	if p := os.Getenv("PORT"); p != "" {
		if _, err := strconv.Atoi(p); err == nil {
			addr = ":" + p
		}
	}

	handler := newHandler(readyAfter, alias, time.Now)
	if err := http.ListenAndServe(addr, handler); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
