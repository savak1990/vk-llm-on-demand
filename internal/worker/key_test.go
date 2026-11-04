package worker

import "testing"

// Recipe to derive the expected vector independently of WorkerKey:
//
//	printf 'worker:i:7' | openssl dgst -sha256 -hmac k -binary | base64 | tr '+/' '-_' | tr -d '='
//
// => "OxsPbGyzAYuFlELia6CQSuDIiUDoC-7tlm0-PYSiew8"
const wantKeyGen7 = "OxsPbGyzAYuFlELia6CQSuDIiUDoC-7tlm0-PYSiew8"

func TestWorkerKey(t *testing.T) {
	got := WorkerKey([]byte("k"), "i", 7)
	if got != wantKeyGen7 {
		t.Errorf("WorkerKey() = %q, want %q", got, wantKeyGen7)
	}
	if n := len(got); n != 43 {
		t.Errorf("WorkerKey() length = %d, want 43", n)
	}
	for _, c := range got {
		if c == ',' {
			t.Errorf("WorkerKey() contains a comma: %q", got)
		}
	}
}

func TestWorkerKeyDiffersByGeneration(t *testing.T) {
	k7 := WorkerKey([]byte("k"), "i", 7)
	k8 := WorkerKey([]byte("k"), "i", 8)
	if k7 == k8 {
		t.Errorf("WorkerKey() returned the same key for generation 7 and 8")
	}
}
