package store

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

const SchemaVersion = 2

const (
	DesiredRunning = "RUNNING"
	DesiredStopped = "STOPPED"
)

type Document struct {
	SchemaVersion    int             `json:"schemaVersion"`
	InstallationID   string          `json:"installationId"`
	Generation       int64           `json:"generation"`
	Desired          string          `json:"desired"`
	Instance         *Instance       `json:"instance"`
	Ops              []Op            `json:"ops"`
	AttemptsThisHour Attempts        `json:"attemptsThisHour"`
	Ledger           Ledger          `json:"ledger"`
	MachineDenylist  []DenyEntry     `json:"machineDenylist"`
	Cache            json.RawMessage `json:"cache"`
	BudgetMonthUSD   *float64        `json:"budgetMonthUsd,omitempty"`
}

type Instance struct {
	ProviderID              string     `json:"providerId"`
	Label                   string     `json:"label"`
	State                   string     `json:"state"`
	Offer                   Offer      `json:"offer"`
	SSHHost                 string     `json:"sshHost"`
	SSHPort                 int        `json:"sshPort"`
	HostKeyFingerprint      string     `json:"hostKeyFingerprint"`
	CreatedAt               time.Time  `json:"createdAt"`
	ReadyAt                 *time.Time `json:"readyAt"`
	LastInferenceFinishedAt *time.Time `json:"lastInferenceFinishedAt"`
	IdleDeadline            *time.Time `json:"idleDeadline"`
	LastError               string     `json:"lastError"`
}

type Offer struct {
	MachineID int64          `json:"machineId"`
	GPU       string         `json:"gpu"`
	DPH       float64        `json:"dph"`
	InetDown  float64        `json:"inetDown"`
	Prices    *OfferPrices   `json:"prices,omitempty"`
	Estimate  *OfferEstimate `json:"estimate,omitempty"`
}

type OfferPrices struct {
	GPUUSDPerHour        float64 `json:"gpuUSDPerHour"`
	StorageUSDPerGBMonth float64 `json:"storageUSDPerGBMonth"`
	DownloadUSDPerGB     float64 `json:"downloadUSDPerGB"`
	UploadUSDPerGB       float64 `json:"uploadUSDPerGB"`
}

type OfferEstimate struct {
	ModelBytes      int64   `json:"modelBytes"`
	DiskGB          int     `json:"diskGB"`
	HorizonSeconds  int64   `json:"horizonSeconds"`
	DownloadSeconds int64   `json:"downloadSeconds"`
	DownloadUSD     float64 `json:"downloadUSD"`
	SessionUSD      float64 `json:"sessionUSD"`
}

type Op struct {
	Key        string     `json:"key"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	Outcome    string     `json:"outcome"`
}

type Attempts struct {
	Hour  string `json:"hour"`
	Count int    `json:"count"`
}

type Ledger struct {
	Month    string        `json:"month"`
	TotalUSD float64       `json:"totalUsd"`
	Entries  []LedgerEntry `json:"entries"`
}

type LedgerEntry struct {
	Gen      int64      `json:"gen"`
	Start    time.Time  `json:"start"`
	End      *time.Time `json:"end"`
	DPH      float64    `json:"dph"`
	FixedUSD float64    `json:"fixedUsd,omitempty"`
	EstUSD   float64    `json:"estUsd"`
}

type DenyEntry struct {
	MachineID int64     `json:"machineId"`
	Until     time.Time `json:"until"`
}

func (d Document) IsFresh() bool { return d.Generation == 0 && d.Instance == nil }

var installationIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

func NewDocument(installationID string) (Document, error) {
	if !installationIDPattern.MatchString(installationID) {
		return Document{}, fmt.Errorf("store: invalid installationId %q", installationID)
	}
	return Document{
		SchemaVersion:  SchemaVersion,
		InstallationID: installationID,
		Desired:        DesiredStopped,
		Cache:          []byte("null"),
	}, nil
}
