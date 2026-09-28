package exec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
	"gopkg.in/yaml.v3"
)

func testPlan() *plan.Plan {
	return &plan.Plan{
		APIVersion: plan.APIVersion,
		Release:    plan.Release{Name: "glm-53", Namespace: "modelforge"},
		Chart:      plan.ChartRef{Name: "sglang", Version: "0.8.0", Repo: "oci://harbor.4pd.io/hardcore-tech"},
		Engine:     "sglang",
		Layers: map[string]values.Tree{
			"form":    {"replicaCount": 2},
			"catalog": {"extraArgs": []any{"--tp-size=8"}},
		},
	}
}

// The flag goes to helm upgrade alone. helmfile's apply runs helm-diff first,
// in the same invocation, and helm-diff does not know --force-conflicts: sent
// through --args it would fail the apply before anything was upgraded.
func TestForceConflictsGoesToTheUpgradeOnly(t *testing.T) {
	if got := (ApplyOptions{}).args(); got != nil {
		t.Errorf("an unforced apply must add no arguments, got %q", got)
	}
	got := ApplyOptions{ForceConflicts: true}.args()
	want := []string{"--sync-args", "--force-conflicts"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("args = %q, want %q", got, want)
	}
}

// End to end through a fake helmfile, so the flag is asserted where it lands
// rather than where it is assembled.
func TestApplyPassesForceConflictsToHelmfile(t *testing.T) {
	dir := t.TempDir()
	bin, log := filepath.Join(dir, "helmfile"), filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r := Runner{HelmfileBin: bin}

	if _, err := r.Apply(t.Context(), testPlan(), ApplyOptions{ForceConflicts: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Apply(t.Context(), testPlan(), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(calls) != 2 {
		t.Fatalf("want two applies, got %q", calls)
	}
	if !strings.Contains(calls[0], "--sync-args --force-conflicts") {
		t.Errorf("forced apply did not carry the flag: %q", calls[0])
	}
	if strings.Contains(calls[1], "force") {
		t.Errorf("an unforced apply must carry nothing of the sort: %q", calls[1])
	}
}

func TestMaterializeWritesAValuesFileAndAOneReleaseHelmfile(t *testing.T) {
	ws, err := Runner{}.Materialize(testPlan())
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	// One file per layer, not one flattened document: the workspace shows the
	// layering, and helm merging them back lands on what compose produced.
	for _, name := range []string{"form.yaml", "catalog.yaml"} {
		if _, err := os.Stat(filepath.Join(ws.Dir, name)); err != nil {
			t.Fatalf("%s not written: %v", name, err)
		}
	}
	vals, err := os.ReadFile(filepath.Join(ws.Dir, "form.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var got values.Tree
	if err := yaml.Unmarshal(vals, &got); err != nil {
		t.Fatal(err)
	}
	if v, _ := values.Get(got, "replicaCount"); v != 2 {
		t.Errorf("values not written: %v", got)
	}

	doc, err := os.ReadFile(ws.Helmfile)
	if err != nil {
		t.Fatal(err)
	}
	var hf plan.HelmfileDoc
	if err := yaml.Unmarshal(doc, &hf); err != nil {
		t.Fatal(err)
	}
	if len(hf.Releases) != 1 {
		t.Fatalf("want one release, got %d", len(hf.Releases))
	}
	r := hf.Releases[0]
	if r.Name != "glm-53" || r.Namespace != "modelforge" || r.Version != "0.8.0" {
		t.Errorf("release wrong: %+v", r)
	}
	if r.Chart != "oci://harbor.4pd.io/hardcore-tech/sglang" {
		t.Errorf("chart ref wrong: %q", r.Chart)
	}
}

// These are load-bearing for a 20-40 minute cold load, and swissd must behave
// the same as `make apply` rather than approximately the same.
func TestHelmDefaultsMatchTheRepo(t *testing.T) {
	ws, err := Runner{}.Materialize(testPlan())
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	doc, _ := os.ReadFile(ws.Helmfile)
	var hf plan.HelmfileDoc
	if err := yaml.Unmarshal(doc, &hf); err != nil {
		t.Fatal(err)
	}
	d := hf.HelmDefaults
	if d.Wait || d.Atomic || d.CleanupOnFail {
		t.Errorf("wait/atomic/cleanupOnFail must all be false: %+v", d)
	}
	if d.HistoryMax != 20 {
		t.Errorf("historyMax = %d, want 20", d.HistoryMax)
	}
	if len(d.DiffArgs) != 1 || d.DiffArgs[0] != "--three-way-merge" {
		t.Errorf("diffArgs = %v", d.DiffArgs)
	}
}

func TestWorkspaceIsRemovedUnlessKept(t *testing.T) {
	ws, err := Runner{}.Materialize(testPlan())
	if err != nil {
		t.Fatal(err)
	}
	dir := ws.Dir
	ws.Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("workspace should be removed")
	}

	kept, err := Runner{KeepWorkspace: true}.Materialize(testPlan())
	if err != nil {
		t.Fatal(err)
	}
	kept.Close()
	if _, err := os.Stat(kept.Dir); err != nil {
		t.Error("--keep-workspace should leave it on disk")
	}
	os.RemoveAll(kept.Dir)
}

// A classic HTTP chart repo is not a chart URL: helm must be told it is a repo
// before it can resolve name+version into an archive. Passing the repo URL as
// the chart 404s, because the archive is <url>/<name>-<version>.tgz.
func TestClassicChartRepoBecomesARepositoriesEntry(t *testing.T) {
	p := testPlan()
	p.Chart.Repo = "https://harbor.4pd.io/chartrepo/hardcore-tech/"
	ws, err := Runner{}.Materialize(p)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	doc, _ := os.ReadFile(ws.Helmfile)
	var hf plan.HelmfileDoc
	if err := yaml.Unmarshal(doc, &hf); err != nil {
		t.Fatal(err)
	}
	if len(hf.Repositories) != 1 {
		t.Fatalf("want a repositories entry, got %+v", hf.Repositories)
	}
	if hf.Repositories[0].URL != "https://harbor.4pd.io/chartrepo/hardcore-tech" {
		t.Errorf("trailing slash should be trimmed: %q", hf.Repositories[0].URL)
	}
	if hf.Releases[0].Chart != "charts/sglang" || hf.Releases[0].Version != "0.8.0" {
		t.Errorf("release must refer to the alias: %+v", hf.Releases[0])
	}
}

// OCI needs no repositories entry: the reference is the chart.
func TestOCIRegistryIsAddressedDirectly(t *testing.T) {
	ws, err := Runner{}.Materialize(testPlan())
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	doc, _ := os.ReadFile(ws.Helmfile)
	var hf plan.HelmfileDoc
	if err := yaml.Unmarshal(doc, &hf); err != nil {
		t.Fatal(err)
	}
	if len(hf.Repositories) != 0 {
		t.Errorf("oci needs no repositories entry: %+v", hf.Repositories)
	}
	if hf.Releases[0].Chart != "oci://harbor.4pd.io/hardcore-tech/sglang" {
		t.Errorf("chart = %q", hf.Releases[0].Chart)
	}
}

func TestLocalChartPathIsAbsolute(t *testing.T) {
	p := testPlan()
	p.Chart.Repo, p.Chart.Path = "", "../charts"
	ws, err := Runner{}.Materialize(p)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	doc, _ := os.ReadFile(ws.Helmfile)
	var hf plan.HelmfileDoc
	_ = yaml.Unmarshal(doc, &hf)
	// helmfile runs with cwd inside the temp dir, so a relative chart path would
	// resolve against the wrong place.
	if !filepath.IsAbs(hf.Releases[0].Chart) {
		t.Errorf("chart path must be absolute, got %q", hf.Releases[0].Chart)
	}
	if hf.Releases[0].Version != "" {
		t.Error("a local chart path carries no version for helm to resolve")
	}
}

func TestNoChartSourceIsAnError(t *testing.T) {
	p := testPlan()
	p.Chart.Repo, p.Chart.Path = "", ""
	if _, err := (Runner{}).Materialize(p); err == nil || !strings.Contains(err.Error(), "no chart source") {
		t.Fatalf("got %v", err)
	}
}
