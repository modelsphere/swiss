package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/aceforeverd/swiss/internal/store"
)

func seedRuns(t *testing.T, s *Server, runs ...store.Run) {
	t.Helper()
	for _, r := range runs {
		if _, err := s.store.RecordRun(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunsAreFilteredAndNewestFirst(t *testing.T) {
	srv, s := deployServer(t, true)
	seedRuns(t, s,
		store.Run{Namespace: "modelforge", Release: "a", Action: "diff", StartedAt: "1", EndedAt: "2"},
		store.Run{Namespace: "modelforge", Release: "b", Action: "apply", StartedAt: "3", EndedAt: "4"},
		store.Run{Namespace: "modelforge", Release: "a", Action: "apply", StartedAt: "5", EndedAt: "6"},
	)

	code, body := get(t, srv, "/api/runs")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	all := body["runs"].([]any)
	if len(all) != 3 {
		t.Fatalf("want 3 runs, got %d", len(all))
	}
	if all[0].(map[string]any)["release"] != "a" || all[0].(map[string]any)["action"] != "apply" {
		t.Errorf("newest first: %v", all[0])
	}
	if body["hasStore"] != true {
		t.Error("a swissd with a database should say so")
	}

	_, byRelease := get(t, srv, "/api/runs?release=a")
	if n := len(byRelease["runs"].([]any)); n != 2 {
		t.Errorf("release filter: want 2, got %d", n)
	}

	_, byAction := get(t, srv, "/api/runs?release=a&action=diff")
	if n := len(byAction["runs"].([]any)); n != 1 {
		t.Errorf("release+action filter: want 1, got %d", n)
	}
}

// helmfile output runs to tens of kilobytes a row. The list omits it so the log
// view stays openable; one row carries it.
func TestOutputIsOmittedFromTheListAndServedPerRun(t *testing.T) {
	srv, s := deployServer(t, true)
	id, err := s.store.RecordRun(context.Background(), store.Run{
		Namespace: "modelforge", Release: "a", Action: "apply",
		Output: "helm upgrade output", StartedAt: "1", EndedAt: "2",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, list := get(t, srv, "/api/runs")
	row := list["runs"].([]any)[0].(map[string]any)
	if _, present := row["output"]; present {
		t.Error("the list must not carry output")
	}

	_, one := get(t, srv, "/api/runs/"+itoa(id))
	if one["output"] != "helm upgrade output" {
		t.Fatalf("a single run must carry its output: %v", one)
	}
}

func TestRunNotFoundAndBadID(t *testing.T) {
	srv, _ := deployServer(t, true)
	if code, _ := get(t, srv, "/api/runs/9999"); code != http.StatusNotFound {
		t.Errorf("missing run: want 404, got %d", code)
	}
	if code, _ := get(t, srv, "/api/runs/not-a-number"); code != http.StatusBadRequest {
		t.Errorf("bad id: want 400, got %d", code)
	}
}

// A swissd with no database serves an empty log rather than an error: losing
// the volume costs the history and nothing else.
func TestNoDatabaseServesAnEmptyLog(t *testing.T) {
	srv, _ := deployServer(t, false)
	code, body := get(t, srv, "/api/runs")
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	if len(body["runs"].([]any)) != 0 || body["hasStore"] != false {
		t.Fatalf("want an empty log flagged as storeless: %v", body)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
