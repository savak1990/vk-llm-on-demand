package configmap

import (
	"os"
	"path/filepath"
	"testing"
)

var selfSignedTestCA = []byte(`-----BEGIN CERTIFICATE-----
MIIBETCBxKADAgECAhRP3e1XI2/TP1/0HbywlUuchagc5jAFBgMrZXAwDzENMAsG
A1UEAwwEdGVzdDAeFw0yNjExMDIyMTAzMDZaFw0zNjEwMzAyMTAzMDZaMA8xDTAL
BgNVBAMMBHRlc3QwKjAFBgMrZXADIQAJRK9Ak3nIsRiWZotVF5ShKVVmFnDcJHeb
dfNQEaljvqMyMDAwHQYDVR0OBBYEFD4M4XYXj26zZgdYmN+bDbI7sxFrMA8GA1Ud
EwEB/wQFMAMBAf8wBQYDK2VwA0EAJGPsrm1jVX8JKwyTyUWYWMTIL1LU8rI9b0pO
5B8eG2bOiwtOjPvzUkQCmLDcLFnVtQ6Z3JIWpMKFpRAEt7jWBw==
-----END CERTIFICATE-----
`)

func writeFixtures(t *testing.T) (tokenPath, caPath string) {
	dir := t.TempDir()
	tokenPath = filepath.Join(dir, "token")
	caPath = filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(tokenPath, []byte("initial-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A minimal self-signed CA PEM good enough for AppendCertsFromPEM to accept;
	// New only checks the file parses, httptest TLS is skipped in these unit tests.
	if err := os.WriteFile(caPath, selfSignedTestCA, 0o600); err != nil {
		t.Fatal(err)
	}
	return tokenPath, caPath
}

func TestNewReadsTokenAndCA(t *testing.T) {
	tokenPath, caPath := writeFixtures(t)
	c, err := New(Config{
		APIServer: "https://127.0.0.1:6443", Namespace: "default", Name: "vkllm-state",
		TokenPath: tokenPath, CAPath: caPath, InstallationID: "4f2c1a9b0e77",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c == nil {
		t.Fatal("New returned nil client")
	}
}

func TestNewMissingToken(t *testing.T) {
	_, caPath := writeFixtures(t)
	_, err := New(Config{TokenPath: "/no/such/token", CAPath: caPath})
	if err == nil {
		t.Fatal("New: want error for missing token file")
	}
}
