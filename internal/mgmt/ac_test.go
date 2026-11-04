package mgmt

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/savak1990/vk-llm-on-demand/internal/auth"
	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/controller"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

func captureLogs() (*bytes.Buffer, *slog.Logger) {
	buf := &bytes.Buffer{}
	return buf, slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func doAuthedReq(h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAC5_IdleDeadlineUntouchedAcross210Calls(t *testing.T) {
	clk := fakeclock.New(time.Unix(0, 0))
	keys := authKeys(t, "sk-vkllm-clientkey1", "sk-vkllm-mgmttoken1")
	fc := newFakeController()
	deadline := clk.Now().Add(1440 * time.Second)
	ready := controller.Snapshot{Public: "READY", IdleDeadline: &deadline}
	fc.startSnap, fc.statusSnap = ready, ready
	h, _ := newFullServer(t, fc, keys, clk, 10000)

	var deadlines []string
	grab := func(rec *httptest.ResponseRecorder) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		deadlines = append(deadlines, string(doc["idleDeadline"]))
	}

	for i := 0; i < 10; i++ {
		grab(doAuthedReq(h, "POST", "/api/v1/gpu/start", "", "sk-vkllm-mgmttoken1"))
	}
	for i := 0; i < 100; i++ {
		grab(doAuthedReq(h, "GET", "/api/v1/gpu/status", "", "sk-vkllm-mgmttoken1"))
	}
	var costBodies []string
	for i := 0; i < 100; i++ {
		costBodies = append(costBodies, doAuthedReq(h, "GET", "/api/v1/gpu/cost", "", "sk-vkllm-mgmttoken1").Body.String())
	}

	if len(deadlines) != 110 {
		t.Fatalf("collected %d idleDeadline values, want 110", len(deadlines))
	}
	// costDocument carries no idleDeadline field; its byte-identity across
	// all 100 calls is the equivalent proof that nothing moved underneath it.
	for i, got := range costBodies {
		if got != costBodies[0] {
			t.Fatalf("cost body[%d] differs from cost body[0] (byte-identical)", i)
		}
	}
	for i, got := range deadlines {
		if got == "" || got == "null" {
			t.Fatalf("idleDeadline[%d] = %q, want a timestamp", i, got)
		}
		if got != deadlines[0] {
			t.Fatalf("idleDeadline[%d] = %s, want %s (byte-identical)", i, got, deadlines[0])
		}
	}
	if got := fc.Status().IdleDeadline; got == nil || !got.Equal(deadline) {
		t.Fatalf("fake controller IdleDeadline mutated: got %v, want %v", got, deadline)
	}
}

func TestAC10_KeyReloadThroughFullServer(t *testing.T) {
	dir := t.TempDir()
	clientPath := writeKeyFile(t, dir, "client-keys", "sk-vkllm-clientkey1")
	mgmtPath := writeKeyFile(t, dir, "management-tokens", "sk-vkllm-mgmttoken1")
	clk := fakeclock.New(time.Unix(0, 0))
	buf, log := captureLogs()
	keys, err := auth.Load(clientPath, mgmtPath, clk, log)
	if err != nil {
		t.Fatalf("auth.Load: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go keys.Run(ctx)
	clk.BlockUntil(1)

	fc := newFakeController()
	fc.statusSnap = controller.Snapshot{Public: "READY"}
	h, _ := newFullServer(t, fc, keys, clk, 10000)

	if rec := doAuthedReq(h, "GET", "/api/v1/gpu/status", "", "sk-vkllm-mgmttoken1"); rec.Code != http.StatusOK {
		t.Fatalf("status before reload = %d, want 200", rec.Code)
	}

	writeKeyFile(t, dir, "management-tokens", "sk-vkllm-mgmttoken2")
	clk.Advance(10 * time.Second)
	clk.BlockUntil(1)

	if rec := doAuthedReq(h, "GET", "/api/v1/gpu/status", "", "sk-vkllm-mgmttoken1"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status with removed token = %d, want 401", rec.Code)
	}
	if rec := doAuthedReq(h, "GET", "/api/v1/gpu/status", "", "sk-vkllm-mgmttoken2"); rec.Code != http.StatusOK {
		t.Fatalf("status with newly added token = %d, want 200", rec.Code)
	}

	// A key duplicated across both lists is a bad reload: the previous sets stay active.
	writeKeyFile(t, dir, "management-tokens", "sk-vkllm-mgmttoken2", "sk-vkllm-clientkey1")
	clk.Advance(10 * time.Second)
	clk.BlockUntil(1)

	if rec := doAuthedReq(h, "GET", "/api/v1/gpu/status", "", "sk-vkllm-mgmttoken2"); rec.Code != http.StatusOK {
		t.Fatalf("status with mgmttoken2 after failed reload = %d, want 200 (previous set active)", rec.Code)
	}
	if rec := doAuthedReq(h, "GET", "/api/v1/gpu/status", "", "sk-vkllm-mgmttoken1"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status with mgmttoken1 after failed reload = %d, want 401 (previous set active)", rec.Code)
	}
	if n := strings.Count(buf.String(), "keys_reload_failed"); n != 1 {
		t.Fatalf("keys_reload_failed logged %d times, want 1", n)
	}
}

func TestAC11_StatusAndCostFullSnapshotMapping(t *testing.T) {
	clk := fakeclock.New(time.Unix(0, 0))
	keys := authKeys(t, "sk-vkllm-clientkey1", "sk-vkllm-mgmttoken1")
	fc := newFakeController()
	fc.statusSnap = controller.Snapshot{Public: "STOPPED"}
	h, _ := newFullServer(t, fc, keys, clk, 10000)

	rec := doAuthedReq(h, "GET", "/api/v1/gpu/status", "", "sk-vkllm-mgmttoken1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status STOPPED = %d, want 200", rec.Code)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode status body: %v", err)
	}
	for _, field := range []string{"instance", "idleDeadline", "secondsUntilIdleShutdown"} {
		if doc[field] != nil {
			t.Fatalf("STOPPED status %s = %v, want null", field, doc[field])
		}
	}
	if uptime, _ := doc["uptimeSeconds"].(float64); uptime != 0 {
		t.Fatalf("STOPPED status uptimeSeconds = %v, want 0", doc["uptimeSeconds"])
	}

	costRec := doAuthedReq(h, "GET", "/api/v1/gpu/cost", "", "sk-vkllm-mgmttoken1")
	if costRec.Code != http.StatusOK {
		t.Fatalf("cost = %d, want 200", costRec.Code)
	}
	var cost map[string]interface{}
	if err := json.Unmarshal(costRec.Body.Bytes(), &cost); err != nil {
		t.Fatalf("decode cost body: %v", err)
	}
	if estimates, _ := cost["estimates"].(bool); !estimates {
		t.Fatalf("cost estimates = %v, want true", cost["estimates"])
	}
	if limitations, _ := cost["limitations"].(string); limitations == "" {
		t.Fatal("cost limitations empty, want non-empty")
	}
}

func TestAC12_RedactionAcrossAllRoutes(t *testing.T) {
	clk := fakeclock.New(time.Unix(0, 0))
	keys := authKeys(t, "sk-vkllm-clientkey1", "sk-vkllm-mgmttoken1")
	fc := newFakeController()
	deadline := clk.Now().Add(600 * time.Second)
	fc.startSnap = controller.Snapshot{
		Public: "READY", IdleDeadline: &deadline, CreatedAt: clk.Now().Add(-time.Hour),
		MachineID: 777001, ProviderID: provider.InstanceID(424242), GPUName: "RTX4090",
	}
	fc.statusSnap = fc.startSnap
	fc.stopSnap = controller.Snapshot{Public: "DRAINING"}
	logs, log := captureLogs()
	reg := prometheus.NewRegistry()
	h := New(Deps{Controller: fc, Keys: keys, Limiter: auth.NewLimiter(10000, clk), Clock: clk, Reg: reg, Log: log})

	var bodies []string
	run := func(method, path, body, token string) {
		bodies = append(bodies, doAuthedReq(h, method, path, body, token).Body.String())
	}

	for _, rt := range []struct{ method, path, body string }{
		{"POST", "/api/v1/gpu/start", ""}, {"GET", "/api/v1/gpu/status", ""},
		{"POST", "/api/v1/gpu/stop", `{"force":false}`}, {"GET", "/api/v1/gpu/cost", ""},
	} {
		run(rt.method, rt.path, rt.body, "sk-vkllm-mgmttoken1")
		run(rt.method, rt.path, rt.body, "sk-vkllm-clientkey1") // wrong surface -> 403
		run(rt.method, rt.path, rt.body, "sk-vkllm-REPLACE_ME") // unknown -> 401
	}
	run("GET", "/api/v1/gpu/status", "", "") // missing Authorization -> 401

	rendered := false
	for _, body := range bodies {
		if strings.Contains(body, `"gpu":"RTX4090"`) {
			rendered = true
		}
	}
	if !rendered {
		t.Fatal("no response body rendered the instance block; the leak checks below would be vacuous")
	}

	text := logs.String()
	for _, secret := range []string{
		"sk-vkllm-mgmttoken1", "sk-vkllm-clientkey1", "sk-vkllm-REPLACE_ME", "Bearer ",
		"Estimates only", `{"force"`,
	} {
		if strings.Contains(text, secret) {
			t.Fatalf("log output contains a secret or body value %q", secret)
		}
	}
	for _, body := range bodies {
		for _, leak := range []string{"sk-vkllm-mgmttoken1", "sk-vkllm-clientkey1", "sshHost", "sshPort", "424242"} {
			if strings.Contains(body, leak) {
				t.Fatalf("response body contains %q: %s", leak, body)
			}
		}
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	allowedOps := map[string]bool{"start": true, "status": true, "stop": true, "cost": true, "other": true}
	allowedCodes := map[string]bool{
		"200": true, "202": true, "400": true, "401": true, "403": true, "404": true,
		"405": true, "413": true, "429": true, "500": true, "503": true,
	}
	for _, fam := range families {
		if fam.GetName() != "vkllm_mgmt_requests_total" && fam.GetName() != "vkllm_mgmt_request_seconds" {
			continue
		}
		for _, m := range fam.GetMetric() {
			for _, l := range m.GetLabel() {
				switch l.GetName() {
				case "op":
					if !allowedOps[l.GetValue()] {
						t.Fatalf("metric %s has unexpected op label %q", fam.GetName(), l.GetValue())
					}
				case "code":
					if !allowedCodes[l.GetValue()] {
						t.Fatalf("metric %s has unexpected code label %q", fam.GetName(), l.GetValue())
					}
				}
			}
		}
	}
}
