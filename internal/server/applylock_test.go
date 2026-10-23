package server

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestApplyLockBusyIs409(t *testing.T) {
	err := &applyBusyError{ns: "models", release: "glm-53"}
	if !errors.Is(err, errApplyBusy) {
		t.Fatal("applyBusyError must unwrap to errApplyBusy")
	}
	var hf *httpFailure
	if !errors.As(err, &hf) || hf.code != http.StatusConflict {
		t.Fatalf("As(httpFailure) = %+v", hf)
	}

	srv, s := deployServerWith(t, liveProbe(), true)
	code, body := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "release": "glm-53",
		"overrides": map[string]any{"replicaCount": 2},
	})
	if code != http.StatusOK {
		t.Fatalf("plan status %d: %v", code, body)
	}
	if !s.tryApplyLock("models", "glm-53") {
		t.Fatal("could not take the apply lock")
	}
	t.Cleanup(func() { s.unlockApply("models", "glm-53") })

	code, out := post(t, srv, "/api/apply", map[string]any{"planHash": body["hash"]})
	if code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %v", code, out)
	}
	if !strings.Contains(errText(out), "apply in progress") {
		t.Fatalf("error %q", errText(out))
	}
}
