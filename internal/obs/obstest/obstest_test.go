package obstest

import "testing"

func TestAssertNoSecretCatches(t *testing.T) {
	rec := &recordingT{}
	assertNoSecretOn(rec, []byte("level=INFO msg=\"auth ok\" token=sk-vkllm-abc123\n"), "sk-vkllm-abc123")
	if !rec.failed {
		t.Fatal("AssertNoSecret did not fail on a buffer containing the literal secret")
	}
}

func TestAssertNoSecretClean(t *testing.T) {
	rec := &recordingT{}
	assertNoSecretOn(rec, []byte("level=INFO msg=\"auth ok\" key_id=abc123\n"), "sk-vkllm-abc123")
	if rec.failed {
		t.Fatal("AssertNoSecret failed on a buffer that only contains the key id, not the full secret")
	}
}

type recordingT struct{ failed bool }

func (r *recordingT) Helper()                   {}
func (r *recordingT) Errorf(_ string, _ ...any) { r.failed = true }
