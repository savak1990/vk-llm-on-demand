package store

import (
	"encoding/json"
	"fmt"
)

const maxDocumentBytes = 256 << 10

func Encode(d Document) ([]byte, error) {
	if len(d.Ops) > 20 {
		d.Ops = append([]Op{}, d.Ops[len(d.Ops)-20:]...)
	}
	b, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("store: marshal document: %w", err)
	}
	if len(b) > maxDocumentBytes {
		return nil, ErrTooLarge
	}
	return b, nil
}

func Decode(b []byte) (Document, error) {
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return Document{}, fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	sv, ok := raw["schemaVersion"].(float64)
	if !ok || sv < 1 {
		return Document{}, ErrCorrupt
	}
	version := int(sv)
	if version > SchemaVersion {
		return Document{}, ErrSchemaTooNew
	}
	for v := version; v < SchemaVersion; v++ {
		upgrade, ok := upgrades[v]
		if !ok {
			return Document{}, ErrCorrupt
		}
		if err := upgrade(raw); err != nil {
			return Document{}, err
		}
	}
	b2, err := json.Marshal(raw)
	if err != nil {
		return Document{}, fmt.Errorf("store: remarshal document: %w", err)
	}
	var d Document
	if err := json.Unmarshal(b2, &d); err != nil {
		return Document{}, fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	return d, nil
}

var upgrades = map[int]func(map[string]any) error{
	1: upgradeV1,
}

// upgradeV1 maps the schema-1 shape (0/1 desired, leases, activity) onto
// schema 2: idle tracking replaces leases entirely.
func upgradeV1(raw map[string]any) error {
	delete(raw, "leases")
	delete(raw, "activity")
	desired, ok := raw["desired"].(float64)
	if !ok {
		return ErrCorrupt
	}
	switch desired {
	case 1:
		raw["desired"] = DesiredRunning
	case 0:
		raw["desired"] = DesiredStopped
	default:
		return ErrCorrupt
	}
	if inst, ok := raw["instance"].(map[string]any); ok && inst != nil {
		inst["lastInferenceFinishedAt"] = nil
		inst["idleDeadline"] = nil
	}
	raw["cache"] = nil
	raw["schemaVersion"] = float64(2)
	return nil
}
