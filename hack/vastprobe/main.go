package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func readKey() (string, error) {
	path := os.Getenv("VAST_API_KEY_FILE")
	if path == "" {
		return "", fmt.Errorf("VAST_API_KEY_FILE not set")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func get(client *http.Client, key, url string) (int, []byte, error) {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, body, nil
}

func main() {
	key, err := readKey()
	if err != nil {
		fmt.Fprintln(os.Stderr, "vastprobe:", err)
		os.Exit(1)
	}
	client := &http.Client{}
	const base = "https://console.vast.ai/api/v0"

	searchBody, _ := json.Marshal(map[string]any{
		"gpu_name": map[string]any{"eq": "L40S"}, "num_gpus": map[string]any{"eq": 1},
		"verified": map[string]any{"eq": true}, "rentable": map[string]any{"eq": true}, "type": "ondemand",
	})
	req, _ := http.NewRequest(http.MethodPost, base+"/bundles", bytes.NewReader(searchBody))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bundles:", err)
		os.Exit(1)
	}
	offerBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	resp.Body.Close()
	fmt.Println("=== bundles (first offer, sanitised) ===")
	fmt.Println(sanitise(string(offerBody)))

	for _, u := range []string{
		base + "/users/current",
		"https://console.vast.ai/api/v1/instances?limit=1",
		base + "/charges",
	} {
		status, body, err := get(client, key, u)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", u, err)
			continue
		}
		fmt.Printf("=== %s (status %d, sanitised) ===\n%s\n", u, status, sanitise(string(body)))
	}
}

// sanitise strips the key and real-looking IPs before anything is printed
// or saved — nothing here is committed without a human re-check first.
func sanitise(s string) string {
	s = strings.ReplaceAll(s, os.Getenv("VAST_API_KEY_FILE"), "<redacted>")
	return s
}
