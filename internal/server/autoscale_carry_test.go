package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/testutil/cases"
	"github.com/modelsphere/swiss/internal/values"
)

type carryCase struct {
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Input       carryInput    `json:"input"`
	Expected    carryExpected `json:"expected"`
}

type carryInput struct {
	Kind              string `json:"kind"`
	On                bool   `json:"rule,omitempty"`
	Overrides         bool   `json:"overrides,omitempty"`
	SLOOff            bool   `json:"sloOff,omitempty"`
	Edits             string `json:"edits,omitempty"`
	ArchivedRule      bool   `json:"archivedRule,omitempty"`
	LiveAutoscale     *int   `json:"liveAutoscale,omitempty"`
	ArchivedAutoscale *int   `json:"archivedAutoscale,omitempty"`
	ArchivedForm      *int   `json:"archivedForm,omitempty"`
}

type carryExpected struct {
	Layer        bool `json:"layer"`
	ReplicaCount *int `json:"replicaCount,omitempty"`
}

func TestAutoscaleCarry(t *testing.T) {
	cases.Run(t, "testdata/autoscale-carry", func(t *testing.T, tc carryCase) {
		switch tc.Input.Kind {
		case "upgrade":
			runUpgradeCarry(t, tc)
		case "rollback":
			runRollbackCarry(t, tc)
		default:
			t.Fatalf("kind %q", tc.Input.Kind)
		}
	})
}

func runUpgradeCarry(t *testing.T, tc carryCase) {
	t.Helper()
	p, _ := livePlan(t, planRequest{
		Model: "qwen3.6-35b-a3b", Release: "r", ServiceID: "r",
		Overrides: values.Tree{"replicaCount": 3},
	})
	if tc.Input.On {
		setAutoscaleRule(p)
	}
	if tc.Input.LiveAutoscale != nil {
		if p.Layers == nil {
			p.Layers = map[string]values.Tree{}
		}
		p.Layers[values.LayerAutoscale] = values.Tree{"replicaCount": *tc.Input.LiveAutoscale}
		if err := p.ComputeHash(); err != nil {
			t.Fatal(err)
		}
	}
	files, err := p.Files("")
	if err != nil {
		t.Fatal(err)
	}
	probe := liveProbe()
	probe.Rel = append(probe.Rel, cluster.Release{
		Name: "r", Namespace: "models", Status: "deployed", Revision: 3, SwissFiles: files,
	})
	srv, _ := deployServerWith(t, probe, true)

	body := map[string]any{"fromRelease": "r"}
	overrides := map[string]any{}
	if tc.Input.Overrides {
		overrides["replicaCount"] = 5
	}
	if tc.Input.SLOOff {
		overrides["sloRequirement"] = map[string]any{"enabled": false}
	}
	if len(overrides) > 0 {
		body["overrides"] = overrides
	}
	if tc.Input.Edits != "" {
		body["editsYAML"] = tc.Input.Edits
	}
	code, up := post(t, srv, "/api/plans", body)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, up)
	}
	layer := layerDoc(up, values.LayerAutoscale)
	if tc.Expected.Layer {
		if jsonInt(layer["replicaCount"]) != *tc.Expected.ReplicaCount {
			t.Fatalf("autoscale replicaCount %v, want %d (layer %v)", layer["replicaCount"], *tc.Expected.ReplicaCount, layer)
		}
		if jsonInt(planValues(up)["replicaCount"]) != *tc.Expected.ReplicaCount {
			t.Fatalf("merged replicaCount %v, want the autoscale value %d", planValues(up)["replicaCount"], *tc.Expected.ReplicaCount)
		}
		return
	}
	if len(layer) != 0 {
		t.Fatalf("autoscale layer should be absent, got %v", layer)
	}
	wantForm := 3
	if tc.Input.Overrides {
		wantForm = 5
	}
	if jsonInt(planValues(up)["replicaCount"]) != wantForm {
		t.Fatalf("merged replicaCount %v, want form %d", planValues(up)["replicaCount"], wantForm)
	}
}

func runRollbackCarry(t *testing.T, tc carryCase) {
	t.Helper()
	form := 1
	if tc.Input.ArchivedForm != nil {
		form = *tc.Input.ArchivedForm
	}
	probe, baseFiles := seedRelease(t, 6, values.Tree{"replicaCount": form})
	base, err := plan.FromFiles(baseFiles)
	if err != nil {
		t.Fatal(err)
	}
	arch := clonePlan(base)
	if tc.Input.ArchivedRule {
		setAutoscaleRule(arch)
	}
	if tc.Input.ArchivedAutoscale != nil {
		if arch.Layers == nil {
			arch.Layers = map[string]values.Tree{}
		}
		arch.Layers[values.LayerAutoscale] = values.Tree{"replicaCount": *tc.Input.ArchivedAutoscale}
	}
	if err := arch.ComputeHash(); err != nil {
		t.Fatal(err)
	}
	archFiles, err := arch.Files("")
	if err != nil {
		t.Fatal(err)
	}
	if err := mustVerify(archFiles); err != nil {
		t.Fatal(err)
	}

	live := clonePlan(base)
	if tc.Input.On {
		setAutoscaleRule(live)
	}
	if tc.Input.LiveAutoscale != nil {
		if live.Layers == nil {
			live.Layers = map[string]values.Tree{}
		}
		live.Layers[values.LayerAutoscale] = values.Tree{"replicaCount": *tc.Input.LiveAutoscale}
	}
	if err := live.ComputeHash(); err != nil {
		t.Fatal(err)
	}
	liveFiles, err := live.Files("")
	if err != nil {
		t.Fatal(err)
	}
	for i := range probe.Rel {
		if probe.Rel[i].Name == "glm-53" {
			probe.Rel[i].SwissFiles = liveFiles
		}
	}
	probe.Maps[planRef("models", "glm-53")] = liveFiles
	ref := archiveRef("models", "glm-53", 4)
	probe.Secrets = map[string]map[string]string{ref: archFiles}
	probe.SecretLabels = map[string]map[string]string{ref: archiveLabels("glm-53", 4)}

	srv, s := deployServerWith(t, probe, true)
	s.cfg.Server.HelmBin, s.cfg.Server.HelmfileBin = stubHelm(t, 0), stubHelm(t, 0)
	code, out := post(t, srv, "/api/releases/models/glm-53/rollback", map[string]any{
		"toRevision": 4, "expectRevision": 6,
	})
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, out)
	}
	if err := mustVerify(probe.Secrets[ref]); err != nil {
		t.Fatalf("archived plan no longer verifies: %v", err)
	}
	hash, _ := out["planHash"].(string)
	archived, err := plan.FromFiles(probe.Secrets[ref])
	if err != nil {
		t.Fatal(err)
	}
	overlay := tc.Input.On && tc.Input.ArchivedRule && tc.Input.LiveAutoscale != nil
	if overlay {
		stored, err := s.store.Plan(context.Background(), hash)
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		if layer := stored.Layers[values.LayerAutoscale]; layer != nil {
			got = jsonInt(layer["replicaCount"])
		}
		if !tc.Expected.Layer || got != *tc.Expected.ReplicaCount {
			t.Fatalf("applied autoscale replicaCount %d, want %d", got, *tc.Expected.ReplicaCount)
		}
		if hash == archived.Hash {
			t.Fatal("overlay did not recompute the hash")
		}
		return
	}
	if hash != archived.Hash {
		t.Fatalf("applied %s, want the archived plan %s", hash, archived.Hash)
	}
}

func setAutoscaleRule(p *plan.Plan) {
	if p.Layers == nil {
		p.Layers = map[string]values.Tree{}
	}
	form := p.Layers[values.LayerForm]
	if form == nil {
		form = values.Tree{}
		p.Layers[values.LayerForm] = form
	}
	form["pd"] = values.Tree{"enabled": true, "colocate": false}
	form["sloRequirement"] = values.Tree{"enabled": true}
}

func mustVerify(files map[string]string) error {
	p, err := plan.FromFiles(files)
	if err != nil {
		return err
	}
	return p.VerifyHash()
}
