package mgmt

import (
	"encoding/json"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

const limitationsText = "Estimates only; provider billing is authoritative."

type instanceDoc struct {
	GPU             string     `json:"gpu"`
	MachineID       int64      `json:"machineId"`
	PriceUSDPerHour float64    `json:"priceUSDPerHour"`
	CreatedAt       time.Time  `json:"createdAt"`
	ReadyAt         *time.Time `json:"readyAt"`
}

type modelDoc struct {
	Alias       string `json:"alias"`
	ContextSize int    `json:"contextSize"`
}

type costSummary struct {
	EstimatedInstanceUSD float64 `json:"estimatedInstanceUSD"`
	EstimatedMonthUSD    float64 `json:"estimatedMonthUSD"`
	BudgetMonthUSD       float64 `json:"budgetMonthUSD"`
}

type statusDocument struct {
	Provider                 string       `json:"provider"`
	State                    string       `json:"state"`
	Desired                  string       `json:"desired"`
	Generation               int64        `json:"generation"`
	Instance                 *instanceDoc `json:"instance"`
	Model                    modelDoc     `json:"model"`
	Ready                    bool         `json:"ready"`
	UptimeSeconds            int64        `json:"uptimeSeconds"`
	LastInferenceFinishedAt  *time.Time   `json:"lastInferenceFinishedAt"`
	IdleDeadline             *time.Time   `json:"idleDeadline"`
	SecondsUntilIdleShutdown *int64       `json:"secondsUntilIdleShutdown"`
	Cost                     costSummary  `json:"cost"`
	Unavailable              string       `json:"unavailable"`
	NextCreateAt             *time.Time   `json:"nextCreateAt"`
	InstallationID           string       `json:"installationId"`
	LabelPrefix              string       `json:"labelPrefix"`
	Result                   string       `json:"result,omitempty"`
}

type costDocument struct {
	Estimates            bool     `json:"estimates"`
	PriceUSDPerHour      float64  `json:"priceUSDPerHour"`
	UptimeSeconds        int64    `json:"uptimeSeconds"`
	EstimatedInstanceUSD float64  `json:"estimatedInstanceUSD"`
	EstimatedMonthUSD    float64  `json:"estimatedMonthUSD"`
	BudgetMonthUSD       float64  `json:"budgetMonthUSD"`
	ProviderBalanceUSD   *float64 `json:"providerBalanceUSD"`
	Limitations          string   `json:"limitations"`
}

func buildStatusDocument(snap controller.Snapshot, cfg config.Mgmt, now time.Time) statusDocument {
	doc := statusDocument{
		Provider: snap.Provider, State: snap.Public, Desired: snap.Desired,
		Generation: snap.Generation, Model: modelDoc{Alias: cfg.ModelAlias, ContextSize: cfg.ModelContextSize},
		Ready: snap.Public == "READY", Cost: costSummary{
			EstimatedInstanceUSD: snap.SpendInstanceUSD, EstimatedMonthUSD: snap.SpendMTDUSD, BudgetMonthUSD: cfg.BudgetMonthUSD,
		},
		Unavailable: snap.Unavailable, InstallationID: snap.InstallationID, LabelPrefix: snap.LabelPrefix,
	}
	if !snap.CreatedAt.IsZero() {
		doc.Instance = &instanceDoc{
			GPU: snap.GPUName, MachineID: snap.MachineID, PriceUSDPerHour: snap.Hourly.USD(),
			CreatedAt: snap.CreatedAt, ReadyAt: snap.ReadyAt,
		}
		doc.UptimeSeconds = int64(now.Sub(snap.CreatedAt).Seconds())
	}
	doc.LastInferenceFinishedAt = snap.LastInferenceFinishedAt
	if snap.Public == "READY" {
		doc.IdleDeadline = snap.IdleDeadline
	}
	if doc.IdleDeadline != nil {
		secs := int64(doc.IdleDeadline.Sub(now).Seconds())
		if secs < 0 {
			secs = 0
		}
		doc.SecondsUntilIdleShutdown = &secs
	}
	if !snap.NextCreateAt.IsZero() && snap.NextCreateAt.After(now) {
		t := snap.NextCreateAt
		doc.NextCreateAt = &t
	}
	return doc
}

func buildCostDocument(snap controller.Snapshot, cfg config.Mgmt, now time.Time) costDocument {
	doc := costDocument{
		Estimates: true, EstimatedMonthUSD: snap.SpendMTDUSD, BudgetMonthUSD: cfg.BudgetMonthUSD,
		ProviderBalanceUSD: snap.ProviderBalanceUSD, Limitations: limitationsText,
	}
	if !snap.CreatedAt.IsZero() {
		doc.PriceUSDPerHour = snap.Hourly.USD()
		doc.UptimeSeconds = int64(now.Sub(snap.CreatedAt).Seconds())
		doc.EstimatedInstanceUSD = snap.SpendInstanceUSD
	}
	return doc
}

// resultFor classifies an op against the snapshot the caller already
// holds, so start/stop never need a second controller call just to
// tell "already there" apart from "just requested".
func resultFor(op string, snap controller.Snapshot) string {
	switch {
	case op == "start" && snap.Public == "READY":
		return "already_running"
	case op == "stop" && snap.Public == "STOPPED":
		return "already_stopped"
	default:
		return "accepted"
	}
}

// errorBody marshals the management API's fixed error shape; reason
// is omitted when empty.
func errorBody(code, message, reason string) []byte {
	doc := struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Reason  string `json:"reason,omitempty"`
		} `json:"error"`
	}{}
	doc.Error.Code = code
	doc.Error.Message = message
	doc.Error.Reason = reason
	b, _ := json.Marshal(doc)
	return b
}
