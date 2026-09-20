package values

import "testing"

func TestMergeReplacesListsRatherThanAppending(t *testing.T) {
	dst := Tree{"extraArgs": []any{"--tp-size=8"}}
	Merge(dst, Tree{"extraArgs": []any{"--tp-size=2"}}, LayerForm, nil)
	got := dst["extraArgs"].([]any)
	if len(got) != 1 || got[0] != "--tp-size=2" {
		t.Fatalf("lists must replace, not append: %v", got)
	}
}

func TestMergeRecursesIntoMapsAndRecordsProvenance(t *testing.T) {
	dst := Tree{"model": map[string]any{"gpus": "8", "mountPath": "/model"}}
	prov := Provenance{}
	Merge(dst, Tree{"model": map[string]any{"localPath": "/mnt/disk0/x"}}, LayerSite, prov)
	m := dst["model"].(map[string]any)
	if m["gpus"] != "8" || m["localPath"] != "/mnt/disk0/x" {
		t.Fatalf("sibling keys must survive a nested merge: %v", m)
	}
	if prov["model.localPath"] != LayerSite {
		t.Fatalf("provenance not recorded: %v", prov)
	}
}

func TestSetRefusesToTunnelThroughScalar(t *testing.T) {
	if err := Set(Tree{"a": "scalar"}, "a.b", 1); err == nil {
		t.Fatal("expected an error rather than silently dropping a.b")
	}
}

func TestOwnerLongestPrefixWins(t *testing.T) {
	for path, want := range map[string]string{
		"cache.hostPath": LayerSite,
		// Derived is a provenance label, not an owner: the site owns cache.*
		// and can therefore override what the derived rule computes.
		"cache.maxSlotsPerNode": LayerSite,
		"image.tag":             LayerCatalog,
		"image.repository":      LayerSite,
		"model.gpus":            LayerCatalog,
		"model.localPath":       LayerSite,
		"scaler.maxReplicas":    LayerForm, // unmatched -> form
		"somethingNewInChart":   LayerForm,
	} {
		if got := Owner(path); got != want {
			t.Errorf("Owner(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestCheckOwnershipRejectsSiteKeyInCatalogLayer(t *testing.T) {
	err := CheckOwnership(Tree{"model": map[string]any{"localPath": "/mnt/x"}}, LayerCatalog)
	if err == nil {
		t.Fatal("a public catalog must not be able to set model.localPath")
	}
}

func TestParseSetInfersTypes(t *testing.T) {
	tr, err := ParseSet("scaler.maxReplicas=8")
	if err != nil {
		t.Fatal(err)
	}
	v, _ := Get(tr, "scaler.maxReplicas")
	if v != int64(8) {
		t.Fatalf("got %#v, want int64(8)", v)
	}
}
