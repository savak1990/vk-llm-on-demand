package worker

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"os"
	"strings"
	"testing"
)

func TestRenderGolden(t *testing.T) {
	want, err := os.ReadFile("testdata/onstart.golden.sh")
	if err != nil {
		t.Fatal(err)
	}
	got := Render()
	if got != string(want) {
		t.Errorf("Render() does not match testdata/onstart.golden.sh byte for byte\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderSize(t *testing.T) {
	if n := len(Render()); n >= 4048 {
		t.Errorf("Render() is %d bytes, want < 4048", n)
	}
}

func TestWrapRoundTrip(t *testing.T) {
	plain := strings.Repeat("echo hello world\n", 400) // force the wrapper path
	wrapped := strings.TrimRight(wrap(plain), "\n")

	const prefix = "echo "
	if !strings.HasPrefix(wrapped, prefix) {
		t.Fatalf("wrap() output does not start with %q: %s", prefix, wrapped)
	}
	const suffix = " | base64 -d | gunzip | bash"
	if !strings.HasSuffix(wrapped, suffix) {
		t.Fatalf("wrap() output does not end with %q: %s", suffix, wrapped)
	}

	b64 := strings.TrimSuffix(strings.TrimPrefix(wrapped, prefix), suffix)
	gz, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("wrap() output is not valid base64: %v", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("wrap() output is not valid gzip: %v", err)
	}
	roundTripped, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(roundTripped) != plain {
		t.Errorf("wrap() round trip does not reproduce the plain script")
	}
}
