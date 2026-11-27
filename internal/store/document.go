package store

import (
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
	SchemaVersion    int         `json:"schemaVersion"`
	InstallationID   string      `json:"installationId"`
	Generation       int64       `json:"generation"`
	Desired          string      `json:"desired"`
	Instance         *Instance   `json:"instance"`
	Ops              []Op        `json:"ops"`
	AttemptsThisHour Attempts    `json:"attemptsThisHour"`
	Ledger           Ledger      `json:"ledger"`
	MachineDenylist  []DenyEntry `json:"machineDenylist"`
	Cache            *CacheState `json:"cache"`
	BudgetMonthUSD   *float64    `json:"budgetMonthUsd,omitempty"`
	StartOrigin      string      `json:"startOrigin,omitempty"`
	Rentals          []Rental    `json:"rentals,omitempty"`
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
	Cache                   string     `json:"cache,omitempty"`
}

// Rental is the record of one start. MarkAt is the controller-clock time
// of the last stage boundary; it is cleared once the start is ready or ends.
type Rental struct {
	Gen                int64            `json:"gen"`
	Origin             string           `json:"origin,omitempty"`
	StartedAt          time.Time        `json:"startedAt"`
	ReadyAt            *time.Time       `json:"readyAt,omitempty"`
	EndedAt            *time.Time       `json:"endedAt,omitempty"`
	MarkAt             *time.Time       `json:"markAt,omitempty"`
	Outcome            string           `json:"outcome,omitempty"`
	MachineID          int64            `json:"machineId"`
	Geo                string           `json:"geo,omitempty"`
	GPU                string           `json:"gpu,omitempty"`
	DPH                float64          `json:"dph"`
	InetDownMbps       float64          `json:"inetDownMbps"`
	EstDownloadSeconds int64            `json:"estDownloadSeconds,omitempty"`
	ModelBytes         int64            `json:"modelBytes,omitempty"`
	Stages             map[string]int64 `json:"stages,omitempty"`
	DownloadMBps       float64          `json:"downloadMBps,omitempty"`
	CostUSD            float64          `json:"costUSD,omitempty"`
	Cache              string           `json:"cache,omitempty"`
}

type Offer struct {
	MachineID int64          `json:"machineId"`
	GPU       string         `json:"gpu"`
	DPH       float64        `json:"dph"`
	InetDown  float64        `json:"inetDown"`
	Geo       string         `json:"geo,omitempty"`
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
	Month           string        `json:"month"`
	TotalUSD        float64       `json:"totalUsd"`
	StorageTotalUSD float64       `json:"storageTotalUsd,omitempty"`
	Entries         []LedgerEntry `json:"entries"`
}

type LedgerEntry struct {
	Gen      int64      `json:"gen"`
	Volume   string     `json:"volume,omitempty"`
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

const (
	CacheSeeding  = "seeding"
	CacheReady    = "ready"
	CacheDeleting = "deleting"
)

type CacheState struct {
	Volumes []CacheVolume `json:"volumes"`
}

// CacheVolume is keyed by Label: the provider assigns VolumeID only after
// the create, and the sweep fills it in from the volume list.
type CacheVolume struct {
	Label                string     `json:"label"`
	VolumeID             int64      `json:"volumeId,omitempty"`
	VolumeOfferID        int64      `json:"volumeOfferId,omitempty"`
	MachineID            int64      `json:"machineId"`
	ModelURL             string     `json:"modelURL"`
	SHA256               string     `json:"sha256"`
	SizeGB               int        `json:"sizeGB"`
	StorageUSDPerGBMonth float64    `json:"storageUSDPerGBMonth"`
	State                string     `json:"state"`
	CreatedGen           int64      `json:"createdGen"`
	InUseGen             int64      `json:"inUseGen"`
	CreatedAt            time.Time  `json:"createdAt"`
	ExpiresAt            *time.Time `json:"expiresAt"`
	DeleteRequestedAt    *time.Time `json:"deleteRequestedAt"`
	DeleteReason         string     `json:"deleteReason,omitempty"`
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
	}, nil
}
