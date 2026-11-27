package version

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func script(t *testing.T) string {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "..", "..", "scripts", "version.sh")
}

// repo makes a throwaway git repository tagged v0.4.1 with one commit after it.
func repo(t *testing.T) string {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "a"},
		{"tag", "v0.4.1"},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "b"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func run(t *testing.T, dir string, args ...string) (string, error) {
	cmd := exec.Command(script(t), args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func TestMainVersionHasRunAndPrefixedSha(t *testing.T) {
	got, err := run(t, repo(t), "main", "0123456789abcdef0123456789abcdef01234567", "57")
	if err != nil {
		t.Fatal(err)
	}
	if want := "0.4.2-main.57.g0123456"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMainVersionNeedsRun(t *testing.T) {
	if _, err := run(t, repo(t), "main", "0123456789abcdef0123456789abcdef01234567"); err == nil {
		t.Fatal("want an error without a run number")
	}
}

func TestMainVersionRejectsNonNumericRun(t *testing.T) {
	if _, err := run(t, repo(t), "main", "0123456789abcdef0123456789abcdef01234567", "5x"); err == nil {
		t.Fatal("want an error for a non-numeric run number")
	}
}

func TestHelmAcceptsAllDigitSha(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not installed")
	}
	got, err := run(t, repo(t), "main", "0123456000000000000000000000000000000000", "57")
	if err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	chart := filepath.Join(wd, "..", "..", "deploy", "helm", "gpu-on-demand")
	cmd := exec.Command("helm", "package", chart, "--version", got, "--app-version", got, "-d", t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helm package %s: %v\n%s", got, err, out)
	}
}
