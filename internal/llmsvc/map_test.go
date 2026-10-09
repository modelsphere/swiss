package llmsvc

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelsphere/swiss/internal/catalog"
	"github.com/modelsphere/swiss/internal/compose"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/site"
	"github.com/modelsphere/swiss/internal/values"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestWireContract(t *testing.T) {
	if CRDName != "llmservices.serving.modelsphere.dev" ||
		APIVersion != "serving.modelsphere.dev/v1alpha1" ||
		Kind != "LLMService" || Resource != "llmservices" {
		t.Fatalf("identity crd=%s api=%s kind=%s resource=%s", CRDName, APIVersion, Kind, Resource)
	}
	if GVR != (schema.GroupVersionResource{Group: "serving.modelsphere.dev", Version: "v1alpha1", Resource: "llmservices"}) {
		t.Fatalf("GVR %v", GVR)
	}
	if PhasePending != "Pending" || PhaseApplying != "Applying" || PhaseApplied != "Applied" || PhaseFailed != "Failed" {
		t.Fatal("phases")
	}
	if ConditionApplied != "Applied" || ConditionAdopted != "Adopted" {
		t.Fatal("conditions")
	}
	if AnnDeletionPolicy != "serving.modelsphere.dev/deletion-policy" || AnnForceConflicts != "serving.modelsphere.dev/force-conflicts" {
		t.Fatalf("operator annotations %s %s", AnnDeletionPolicy, AnnForceConflicts)
	}
}

func TestRoundTripKeepsThePlanHash(t *testing.T) {
	p := realistic(t)
	p.CreateNamespace = true
	doc, err := p.RenderHelmfile("")
	if err != nil {
		t.Fatal(err)
	}
	p.Helmfile = doc
	if !strings.Contains(p.Helmfile, "createNamespace: true") {
		t.Fatalf("fixture did not ask for a namespace:\n%s", p.Helmfile)
	}

	u := assertRoundTrip(t, p, "upgrade", "bump to 1.1.0")
	if u.GetAPIVersion() != "serving.modelsphere.dev/v1alpha1" || u.GetKind() != "LLMService" {
		t.Fatalf("gvk %s %s", u.GetAPIVersion(), u.GetKind())
	}
	if u.GetNamespace() != "models" || u.GetName() != "qwen" {
		t.Fatalf("meta %s/%s", u.GetNamespace(), u.GetName())
	}
	if got := layerNames(t, u); !reflect.DeepEqual(got, []string{"catalog", "site", "derived", "form", "edit"}) {
		t.Fatalf("layers %v", got)
	}
	back, err := ToPlan(u)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(back.Helmfile, "createNamespace: true") {
		t.Fatalf("createNamespace survived in the helmfile:\n%s", back.Helmfile)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(u.Object, "spec", "suspend"); found {
		t.Fatal("suspend false was written")
	}
	if _, found, _ := unstructured.NestedMap(u.Object, "spec", "chart", "credentialsRef"); found {
		t.Fatal("credentialsRef is not on the plan")
	}
}

func TestEmptyLayersAreLeftOut(t *testing.T) {
	p := &plan.Plan{
		APIVersion: plan.APIVersion,
		Release:    plan.Release{Name: "qwen", Namespace: "models"},
		Source: plan.SourceRef{
			Catalog: "https://example.invalid/swiss-catalog/index.json",
			Model:   "qwen3.6-35b-a3b", Version: "1.1.0", Variant: "sglang-tp2-h100",
		},
		Chart:   plan.ChartRef{Name: "sglang", Version: "0.8.6", Repo: "oci://ghcr.io/modelsphere/charts"},
		Engine:  "sglang",
		Profile: "prod",
		Layers: map[string]values.Tree{
			values.LayerCatalog: nil,
			values.LayerSite:    {"image": map[string]any{"repository": "ghcr.io/modelsphere/sglang"}},
			values.LayerDerived: {},
			values.LayerForm:    {"replicaCount": 2, "extraArgs": []any{"--tp-size=2"}},
		},
	}
	if err := p.ComputeHash(); err != nil {
		t.Fatal(err)
	}
	u := assertRoundTrip(t, p, "", "")
	if got := layerNames(t, u); !reflect.DeepEqual(got, []string{"site", "form"}) {
		t.Fatalf("layers %v", got)
	}
}

func TestChartPathPlanIsRefused(t *testing.T) {
	p := &plan.Plan{
		Release: plan.Release{Name: "qwen", Namespace: "models"},
		Chart:   plan.ChartRef{Name: "sglang", Version: "0.8.6", Path: "../charts"},
	}
	_, err := FromPlan(p, "install", "")
	if err == nil || err.Error() != "a chartPath plan cannot use llmsvc: the operator has no path" {
		t.Fatalf("got %v", err)
	}
	if !errors.Is(err, ErrChartPath) {
		t.Fatalf("chartPath error is not ErrChartPath: %v", err)
	}

	p.Chart.Path = ""
	_, err = FromPlan(p, "install", "")
	if err == nil || err.Error() == "a chartPath plan cannot use llmsvc: the operator has no path" {
		t.Fatalf("a plan with no chart source: %v", err)
	}
	if !errors.Is(err, ErrChartRepo) {
		t.Fatalf("missing repo is not ErrChartRepo: %v", err)
	}
}

func TestAnnotationsAreExact(t *testing.T) {
	p := realistic(t)
	u, err := FromPlan(p, "upgrade", "bump to 1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"swiss.modelsphere.dev/plan-hash":    p.Hash,
		"swiss.modelsphere.dev/catalog":      p.Source.Catalog,
		"swiss.modelsphere.dev/catalog-name": p.Source.CatalogName,
		"swiss.modelsphere.dev/catalog-ref":  p.Source.Ref,
		"swiss.modelsphere.dev/entry-digest": p.Source.Digest,
		"swiss.modelsphere.dev/profile":      p.Profile,
		"swiss.modelsphere.dev/action":       "upgrade",
		"swiss.modelsphere.dev/note":         "bump to 1.1.0",
	}
	if !reflect.DeepEqual(u.GetAnnotations(), want) {
		t.Fatalf("annotations\n got %#v\nwant %#v", u.GetAnnotations(), want)
	}

	u, err = FromPlan(p, "", "")
	if err != nil {
		t.Fatal(err)
	}
	ann := u.GetAnnotations()
	if _, ok := ann["swiss.modelsphere.dev/action"]; ok {
		t.Fatal("empty action was written")
	}
	if _, ok := ann["swiss.modelsphere.dev/note"]; ok {
		t.Fatal("empty note was written")
	}
	if ann["swiss.modelsphere.dev/plan-hash"] != p.Hash || ann["swiss.modelsphere.dev/profile"] != "prod" {
		t.Fatalf("remaining annotations %#v", ann)
	}
	if IsExternal(u) {
		t.Fatal("a swiss-written object is not external")
	}
}

func TestTamperedPlanHashIsRefused(t *testing.T) {
	p := realistic(t)
	u, err := FromPlan(p, "upgrade", "")
	if err != nil {
		t.Fatal(err)
	}
	ann := u.GetAnnotations()
	ann["swiss.modelsphere.dev/plan-hash"] = "sha256:tampered"
	u.SetAnnotations(ann)
	if _, err := ToPlan(u); err == nil {
		t.Fatal("a plan-hash that does not match the spec must be refused")
	}
}

func TestExternalLLMServiceMaps(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.modelsphere.dev/v1alpha1",
		"kind":       "LLMService",
		"metadata": map[string]any{
			"name":      "qwen",
			"namespace": "models",
			"annotations": map[string]any{
				"serving.modelsphere.dev/deletion-policy": "Orphan",
			},
		},
		"spec": map[string]any{
			"chart": map[string]any{
				"name": "sglang", "repo": "oci://ghcr.io/modelsphere/charts", "version": "0.8.6",
			},
			"layers": []any{
				map[string]any{"name": "edit", "values": map[string]any{"replicaCount": int64(1)}},
			},
			"model": map[string]any{"name": "qwen3.6-35b-a3b", "engine": "sglang", "hf": "Qwen/Qwen3.6-35B-A3B"},
		},
	}}
	if !IsExternal(u) {
		t.Fatal("operator annotations are not swiss provenance")
	}
	p, err := ToPlan(u)
	if err != nil {
		t.Fatal(err)
	}
	if p.Hash == "" {
		t.Fatal("external object has no computed hash")
	}
	if err := p.VerifyHash(); err != nil {
		t.Fatal(err)
	}
	if p.APIVersion != plan.APIVersion || p.Release.Name != "qwen" || p.Release.Namespace != "models" {
		t.Fatalf("identity %+v", p.Release)
	}
	if p.Chart.Repo != "oci://ghcr.io/modelsphere/charts" || p.Chart.Name != "sglang" || p.Chart.Version != "0.8.6" || p.Chart.Path != "" {
		t.Fatalf("chart %+v", p.Chart)
	}
	if p.Source.Model != "qwen3.6-35b-a3b" || p.Source.HF != "Qwen/Qwen3.6-35B-A3B" || p.Engine != "sglang" {
		t.Fatalf("model %+v engine %s", p.Source, p.Engine)
	}
	if p.Source.Catalog != "" || p.Profile != "" {
		t.Fatalf("provenance leaked: %+v profile %q", p.Source, p.Profile)
	}
	if _, ok := p.Layers["edit"]; !ok || len(p.Layers) != 1 {
		t.Fatalf("layers %#v", p.Layers)
	}
	if p.Helmfile == "" || !strings.Contains(p.Helmfile, "namespace: models") {
		t.Fatalf("helmfile was not regenerated:\n%s", p.Helmfile)
	}
	if p.CreateNamespace {
		t.Fatal("createNamespace was invented")
	}

	// A swiss annotation, even without a plan hash, is not external.
	u.SetAnnotations(map[string]string{"swiss.modelsphere.dev/profile": "prod"})
	if IsExternal(u) {
		t.Fatal("profile annotation is swiss provenance")
	}
}

func TestStatusRoundTripsThroughUnstructured(t *testing.T) {
	ts := metav1.NewTime(time.Date(2026, 10, 9, 3, 4, 5, 0, time.UTC))
	in := &LLMService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "qwen", Namespace: "models",
			Annotations: map[string]string{AnnAction: "upgrade"},
		},
		Spec: Spec{
			Chart: ChartSpec{
				Name: "sglang", Repo: "oci://ghcr.io/modelsphere/charts", Version: ">=0.8.0",
				CredentialsRef: &CredentialsRef{Name: "chart-pull"},
			},
			Layers: []Layer{{Name: "catalog", Values: values.Tree{"replicaCount": 1}}},
			Model: &ModelSpec{
				Name: "qwen3.6-35b-a3b", Version: "1.1.0", Variant: "sglang-tp2-h100",
				HF: "Qwen/Qwen3.6-35B-A3B", Engine: "sglang",
			},
			Suspend: true,
		},
		Status: &Status{
			ObservedGeneration: 7,
			Phase:              PhaseApplied,
			Conditions: []metav1.Condition{{
				Type: ConditionAdopted, Status: metav1.ConditionTrue, Reason: "Adopted",
				Message: "equal", LastTransitionTime: ts,
			}},
			Chart:       &ChartStatus{Name: "sglang", Version: "0.8.6"},
			AppliedHash: "sha256:abc",
			Helm:        &HelmStatus{Revision: 12, Status: "deployed"},
			History: []HistoryEntry{{
				Revision: 12, Hash: "sha256:abc", AppliedAt: ts,
				ControllerRevision: "qwen-7c9d",
				Annotations:        map[string]string{AnnAction: "upgrade", AnnNote: "bump to 1.1.0"},
				ForceConflicts:     true, Action: "upgrade",
			}},
		},
	}
	in.APIVersion = APIVersion
	in.Kind = Kind
	u, err := ToUnstructured(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(u.Object)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, needle := range []string{
		`"forceConflicts":true`,
		`"controllerRevision":"qwen-7c9d"`,
		`"observedGeneration":7`,
		`"credentialsRef":{"name":"chart-pull"}`,
		`"appliedHash":"sha256:abc"`,
		`"suspend":true`,
		`"action":"upgrade"`,
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("missing %s in %s", needle, s)
		}
	}

	got, err := FromUnstructured(u)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "qwen" || got.Namespace != "models" || got.APIVersion != APIVersion || got.Kind != Kind {
		t.Fatalf("meta %+v", got.ObjectMeta)
	}
	if !got.Spec.Suspend || got.Spec.Chart.CredentialsRef == nil || got.Spec.Chart.CredentialsRef.Name != "chart-pull" {
		t.Fatalf("spec %+v", got.Spec)
	}
	if got.Spec.Chart.Version != ">=0.8.0" || got.Spec.Model == nil || got.Spec.Model.HF != "Qwen/Qwen3.6-35B-A3B" || got.Spec.Model.Engine != "sglang" {
		t.Fatalf("chart/model %+v %+v", got.Spec.Chart, got.Spec.Model)
	}
	if got.Status == nil || got.Status.ObservedGeneration != 7 || got.Status.Phase != PhaseApplied || got.Status.AppliedHash != "sha256:abc" {
		t.Fatalf("status %+v", got.Status)
	}
	if got.Status.Chart == nil || got.Status.Chart.Version != "0.8.6" || got.Status.Helm == nil || got.Status.Helm.Revision != 12 || got.Status.Helm.Status != "deployed" {
		t.Fatalf("status chart/helm %+v %+v", got.Status.Chart, got.Status.Helm)
	}
	if len(got.Status.Conditions) != 1 || got.Status.Conditions[0].Type != ConditionAdopted || got.Status.Conditions[0].Status != metav1.ConditionTrue {
		t.Fatalf("conditions %+v", got.Status.Conditions)
	}
	if len(got.Status.History) != 1 {
		t.Fatalf("history %+v", got.Status.History)
	}
	h := got.Status.History[0]
	if h.Revision != 12 || h.Hash != "sha256:abc" || h.ControllerRevision != "qwen-7c9d" || !h.ForceConflicts || h.Action != "upgrade" {
		t.Fatalf("history entry %+v", h)
	}
	if h.Annotations[AnnAction] != "upgrade" || h.Annotations[AnnNote] != "bump to 1.1.0" {
		t.Fatalf("history annotations %#v", h.Annotations)
	}
	if !h.AppliedAt.Time.Equal(ts.Time) {
		t.Fatalf("appliedAt %s", h.AppliedAt)
	}
}

func realistic(t *testing.T) *plan.Plan {
	t.Helper()
	p, err := compose.Compose(compose.Input{
		Catalog:     "https://example.invalid/swiss-catalog/index.json",
		CatalogName: "public",
		Ref:         "0123456789abcdef0123456789abcdef01234567",
		Entry: catalog.Entry{
			APIVersion: catalog.APIVersion,
			Name:       "qwen3.6-35b-a3b",
			Version:    "1.1.0",
			Digest:     "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Source:     catalog.Source{HF: "Qwen/Qwen3.6-35B-A3B"},
		},
		Variant: catalog.Variant{
			ID:       "sglang-tp2-h100",
			Engine:   "sglang",
			Chart:    catalog.Chart{Name: "sglang", Version: "0.8.6"},
			Image:    &catalog.Image{Repository: "lmsysorg/sglang", Tag: "v0.5.9"},
			Requires: catalog.Requires{GPUs: 2},
			Values: values.Tree{
				"extraArgs": []any{"--tp-size=2"},
				"model":     map[string]any{"mountPath": "/model"},
			},
		},
		Profile: site.Profile{
			Name:      "prod",
			Namespace: "models",
			ChartRepo: "oci://ghcr.io/modelsphere/charts",
			Model:     site.ModelPaths{PathTemplate: "/mnt/disk0/models/{{name}}"},
			Registry:  site.Registry{Mirror: "ghcr.io/modelsphere"},
			Cache:     site.Cache{Enabled: true, HostPath: "/mnt/disk0/sglang-cache"},
			Nodes:     site.Nodes{GPUsPerNode: 8},
		},
		Release:   "qwen",
		Namespace: "models",
		Overrides: values.Tree{"replicaCount": 2},
		Edits:     values.Tree{"extraArgs": []any{"--tp-size=2", "--mem-fraction-static=0.85"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func assertRoundTrip(t *testing.T, p *plan.Plan, action, note string) *unstructured.Unstructured {
	t.Helper()
	u, err := FromPlan(p, action, note)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ToPlan(u)
	if err != nil {
		t.Fatal(err)
	}
	if back.Hash != p.Hash {
		t.Fatalf("hash\n got %s\nwant %s\nvalues %s\nvs    %s", back.Hash, p.Hash, canonical(t, back.Values()), canonical(t, p.Values()))
	}
	if err := back.VerifyHash(); err != nil {
		t.Fatal(err)
	}
	if back.APIVersion != plan.APIVersion {
		t.Fatalf("apiVersion %s", back.APIVersion)
	}
	if back.Release != p.Release || back.Source != p.Source || back.Chart != p.Chart || back.Engine != p.Engine || back.Profile != p.Profile {
		t.Fatalf("got release=%+v source=%+v chart=%+v engine=%s profile=%s\nwant release=%+v source=%+v chart=%+v engine=%s profile=%s",
			back.Release, back.Source, back.Chart, back.Engine, back.Profile,
			p.Release, p.Source, p.Chart, p.Engine, p.Profile)
	}
	sameLayers(t, p.Layers, back.Layers)
	doc, err := back.RenderHelmfile("")
	if err != nil {
		t.Fatal(err)
	}
	if back.Helmfile != doc {
		t.Fatalf("helmfile not regenerated:\n%s\nvs\n%s", back.Helmfile, doc)
	}
	if back.CreateNamespace {
		t.Fatal("createNamespace was carried")
	}
	return u
}

func sameLayers(t *testing.T, want, got map[string]values.Tree) {
	t.Helper()
	want, got = nonEmpty(want), nonEmpty(got)
	if len(want) != len(got) {
		t.Fatalf("layer count %d vs %d\nwant %#v\ngot %#v", len(want), len(got), want, got)
	}
	for name, tree := range want {
		if canonical(t, tree) != canonical(t, got[name]) {
			t.Errorf("layer %s\n got %s\nwant %s", name, canonical(t, got[name]), canonical(t, tree))
		}
	}
}

func nonEmpty(layers map[string]values.Tree) map[string]values.Tree {
	out := map[string]values.Tree{}
	for k, v := range layers {
		if len(v) > 0 {
			out[k] = v
		}
	}
	return out
}

func canonical(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func layerNames(t *testing.T, u *unstructured.Unstructured) []string {
	t.Helper()
	raw, found, err := unstructured.NestedSlice(u.Object, "spec", "layers")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		return nil
	}
	var names []string
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("layer %T", item)
		}
		name, _ := m["name"].(string)
		names = append(names, name)
	}
	return names
}
