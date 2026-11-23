package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Pinned to a non-UTC offset so the goldens only pass if renderStatus
// actually converts to local time, not because local happens to equal UTC.
func TestMain(m *testing.M) {
	time.Local = time.FixedZone("TEST", 3600)
	os.Exit(m.Run())
}

func parseStatusDoc(t *testing.T, jsonStr string) statusDoc {
	var doc statusDoc
	if err := json.Unmarshal([]byte(jsonStr), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestStatusGolden(t *testing.T) {
	cases := []struct {
		name string
		doc  statusDoc
	}{
		{
			"stopped",
			parseStatusDoc(t, `{
				"provider": "vast.ai",
				"state": "STOPPED",
				"instance": null,
				"model": {"alias": "qwen-35b", "contextSize": 32768},
				"ready": false,
				"uptimeSeconds": 0,
				"lastInferenceFinishedAt": null,
				"secondsUntilIdleShutdown": null,
				"cost": {"estimatedInstanceUSD": 0, "estimatedMonthUSD": 0, "budgetMonthUSD": 30},
				"unavailable": ""
			}`),
		},
		{
			"allocating",
			parseStatusDoc(t, `{
				"provider": "vast.ai",
				"state": "ALLOCATING",
				"instance": {
					"gpu": "RTX 4090",
					"machineId": 12345,
					"priceUSDPerHour": 0.71,
					"createdAt": "2026-11-11T10:30:00Z",
					"readyAt": null
				},
				"model": {"alias": "qwen-35b", "contextSize": 32768},
				"ready": false,
				"uptimeSeconds": 0,
				"lastInferenceFinishedAt": null,
				"secondsUntilIdleShutdown": null,
				"cost": {"estimatedInstanceUSD": 0, "estimatedMonthUSD": 0, "budgetMonthUSD": 30},
				"unavailable": ""
			}`),
		},
		{
			"ready",
			parseStatusDoc(t, `{
				"provider": "vast.ai",
				"state": "READY",
				"instance": {
					"gpu": "RTX 4090",
					"machineId": 12345,
					"priceUSDPerHour": 0.71,
					"createdAt": "2026-11-11T10:30:00Z",
					"readyAt": "2026-11-11T10:30:00Z"
				},
				"model": {"alias": "qwen-35b", "contextSize": 32768},
				"ready": true,
				"uptimeSeconds": 1830,
				"lastInferenceFinishedAt": "2026-11-11T10:25:00Z",
				"secondsUntilIdleShutdown": 1800,
				"cost": {"estimatedInstanceUSD": 0.5, "estimatedMonthUSD": 10, "budgetMonthUSD": 30},
				"unavailable": ""
			}`),
		},
		{
			"draining",
			parseStatusDoc(t, `{
				"provider": "vast.ai",
				"state": "DRAINING",
				"instance": {
					"gpu": "RTX 4090",
					"machineId": 12345,
					"priceUSDPerHour": 0.71,
					"createdAt": "2026-11-11T10:30:00Z",
					"readyAt": "2026-11-11T10:30:00Z"
				},
				"model": {"alias": "qwen-35b", "contextSize": 32768},
				"ready": true,
				"uptimeSeconds": 1830,
				"lastInferenceFinishedAt": "2026-11-11T10:25:00Z",
				"secondsUntilIdleShutdown": 0,
				"cost": {"estimatedInstanceUSD": 0.5, "estimatedMonthUSD": 10, "budgetMonthUSD": 30},
				"unavailable": ""
			}`),
		},
		{
			"unavailable",
			parseStatusDoc(t, `{
				"provider": "vast.ai",
				"state": "STOPPED",
				"instance": null,
				"model": {"alias": "qwen-35b", "contextSize": 32768},
				"ready": false,
				"uptimeSeconds": 0,
				"lastInferenceFinishedAt": null,
				"secondsUntilIdleShutdown": null,
				"cost": {"estimatedInstanceUSD": 0, "estimatedMonthUSD": 0, "budgetMonthUSD": 30},
				"unavailable": "no_offer"
			}`),
		},
		{
			"desired_match",
			parseStatusDoc(t, `{
				"provider": "vast.ai",
				"state": "READY",
				"desired": "RUNNING",
				"instance": {
					"gpu": "RTX 4090",
					"machineId": 12345,
					"priceUSDPerHour": 0.71,
					"createdAt": "2026-11-11T10:30:00Z",
					"readyAt": "2026-11-11T10:30:00Z"
				},
				"model": {"alias": "qwen-35b", "contextSize": 32768},
				"ready": true,
				"uptimeSeconds": 1830,
				"lastInferenceFinishedAt": "2026-11-11T10:25:00Z",
				"secondsUntilIdleShutdown": 1800,
				"cost": {"estimatedInstanceUSD": 0.5, "estimatedMonthUSD": 10, "budgetMonthUSD": 30},
				"unavailable": ""
			}`),
		},
		{
			"ready_cost",
			parseStatusDoc(t, `{
				"provider": "vast.ai",
				"state": "READY",
				"instance": {
					"gpu": "RTX 6000Ada", "machineId": 40508, "priceUSDPerHour": 0.6156,
					"createdAt": "2026-11-22T10:30:00Z", "readyAt": "2026-11-22T10:40:00Z",
					"prices": {"gpuUSDPerHour": 0.56, "storageUSDPerGBMonth": 0.667, "downloadUSDPerGB": 0.039, "uploadUSDPerGB": 0.039},
					"inetDownMbps": 889.4
				},
				"model": {"alias": "qwen-35b", "contextSize": 32768},
				"ready": true,
				"uptimeSeconds": 1530,
				"lastInferenceFinishedAt": null,
				"secondsUntilIdleShutdown": 1800,
				"cost": {
					"estimatedInstanceUSD": 1.41, "estimatedMonthUSD": 4.2, "budgetMonthUSD": 30,
					"breakdown": {"gpuUSD": 0.24, "storageUSD": 0.03, "downloadUSD": 1.14},
					"selection": {"modelBytes": 29200000000, "horizonSeconds": 1800, "estimatedDownloadSeconds": 263, "estimatedSessionUSD": 1.49}
				},
				"unavailable": ""
			}`),
		},
		{
			"desired_mismatch",
			parseStatusDoc(t, `{
				"provider": "vast.ai",
				"state": "ALLOCATING",
				"desired": "STOPPED",
				"instance": {
					"gpu": "RTX 4090",
					"machineId": 12345,
					"priceUSDPerHour": 0.71,
					"createdAt": "2026-11-11T10:30:00Z",
					"readyAt": null
				},
				"model": {"alias": "qwen-35b", "contextSize": 32768},
				"ready": false,
				"uptimeSeconds": 0,
				"lastInferenceFinishedAt": null,
				"secondsUntilIdleShutdown": null,
				"cost": {"estimatedInstanceUSD": 0, "estimatedMonthUSD": 0, "budgetMonthUSD": 30},
				"unavailable": ""
			}`),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			renderStatus(&buf, c.doc)
			want, err := os.ReadFile(filepath.Join("testdata", "status_"+c.name+".golden"))
			if err != nil {
				t.Fatal(err)
			}
			if buf.String() != string(want) {
				t.Errorf("render mismatch:\ngot:\n%s\nwant:\n%s", buf.String(), string(want))
			}
		})
	}
}

func TestJSONRender(t *testing.T) {
	raw := []byte(`{"state":"READY","ready":true}`)
	var buf bytes.Buffer
	renderJSON(&buf, raw)
	if buf.String() != string(raw) {
		t.Errorf("JSON render mismatch:\ngot:\n%s\nwant:\n%s", buf.String(), string(raw))
	}
}
