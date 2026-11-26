package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const rentalsJSON = `{"rentals":[
 {"gen":12,"startedAt":"2026-11-25T12:54:00Z","outcome":"failed_status","geo":"South Korea, KR","dph":0.637,
  "stages":{"search":0,"create":1,"boot":60}},
 {"gen":11,"startedAt":"2026-11-25T09:37:40Z","readyAt":"2026-11-25T09:44:12Z","outcome":"ready","geo":"Quebec, CA","dph":0.643,
  "stages":{"search":1,"create":1,"boot":105,"link":6,"download":285,"load":9,"ready":296},"downloadMBps":100.3}]}`

func TestRentalsGolden(t *testing.T) {
	var doc rentalsDoc
	if err := json.Unmarshal([]byte(rentalsJSON), &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	var buf bytes.Buffer
	renderRentals(&buf, doc.Rentals)
	want, err := os.ReadFile(filepath.Join("testdata", "rentals.golden"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if buf.String() != string(want) {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestRentalsEmpty(t *testing.T) {
	var buf bytes.Buffer
	renderRentals(&buf, nil)
	if buf.String() != "No rentals recorded yet.\n" {
		t.Fatalf("got %q", buf.String())
	}
}
