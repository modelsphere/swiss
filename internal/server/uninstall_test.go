package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
)

// stubHelm writes a helm that reports whatever the test needs. The real binary
// is absent here, and an uninstall that shells out is otherwise untestable.
func stubHelm(t *testing.T, exitCode int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "helm")
	script := "#!/bin/sh\necho \"helm $*\"\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func del(t *testing.T, srv *httptest.Server, path string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func TestUninstallRemovesTheReleaseThenThePlan(t *testing.T) {
	srv, s := deployServerWith(t, liveProbe(), true)
	s.cfg.Server.HelmBin = stubHelm(t, 0)
	w := s.writer.(*fakeWriter)

	code, out := del(t, srv, "/api/releases/modelforge/glm-53")
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, out)
	}
	if len(w.deleted) != 1 || w.deleted[0] != "modelforge/"+cluster.PlanConfigMapPrefix+"glm-53" {
		t.Fatalf("the plan beside the release must be removed, deleted=%v", w.deleted)
	}

	runs, err := s.store.Runs(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 || runs[0].Action != "uninstall" {
		t.Fatalf("an uninstall must be audited: %+v", runs)
	}
	if runs[0].Error != "" {
		t.Errorf("a successful uninstall should record no error: %q", runs[0].Error)
	}
	if runs[0].Release != "glm-53" || runs[0].Namespace != "modelforge" {
		t.Errorf("the audit row must name the release: %+v", runs[0])
	}
}

// The write-ahead in reverse. An apply records the plan before touching the
// cluster so a failure cannot leave a live release with nothing beside it; an
// uninstall removes the release first for the same reason. Dropping the plan up
// front and then failing would turn a swiss-deployed release into an untracked
// one -- the row that is supposed to mean somebody installed by hand.
func TestFailedUninstallLeavesThePlanInPlace(t *testing.T) {
	srv, s := deployServerWith(t, liveProbe(), true)
	s.cfg.Server.HelmBin = stubHelm(t, 1)
	w := s.writer.(*fakeWriter)

	code, _ := del(t, srv, "/api/releases/modelforge/glm-53")
	if code != http.StatusInternalServerError {
		t.Fatalf("a failing helm must fail the request, got %d", code)
	}
	if len(w.deleted) != 0 {
		t.Fatalf("the plan must survive a failed uninstall, deleted=%v", w.deleted)
	}

	runs, _ := s.store.Runs(context.Background(), 10)
	if len(runs) == 0 || runs[0].Error == "" {
		t.Fatalf("a failed uninstall must still be audited with its error: %+v", runs)
	}
}

func TestUninstallRefusesAReleaseThatIsNotThere(t *testing.T) {
	srv, s := deployServerWith(t, liveProbe(), true)
	s.cfg.Server.HelmBin = stubHelm(t, 0)

	code, out := del(t, srv, "/api/releases/modelforge/never-deployed")
	if code != http.StatusNotFound {
		t.Fatalf("want 404, got %d %v", code, out)
	}
}

// A release swiss did not deploy has no plan beside it, and is exactly the row
// most likely to need removing. Uninstall names a release rather than a plan so
// that it stays possible.
func TestUninstallWorksOnAnUntrackedRelease(t *testing.T) {
	srv, s := deployServerWith(t, liveProbe(), true)
	s.cfg.Server.HelmBin = stubHelm(t, 0)

	code, out := del(t, srv, "/api/releases/modelforge/by-hand")
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, out)
	}
	runs, _ := s.store.Runs(context.Background(), 10)
	if len(runs) == 0 || runs[0].PlanHash != "" {
		t.Errorf("no plan means no hash, not a fabricated one: %+v", runs)
	}
}

func TestReadOnlyServerRefusesUninstall(t *testing.T) {
	srv, _ := deployServer(t, false)
	code, body := del(t, srv, "/api/releases/modelforge/glm-53")
	if code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", code)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "allowDeploy") {
		t.Errorf("the refusal should name the switch: %q", msg)
	}
}
