package catalog

import "testing"

// variants[].values is the chart schema, not ours. The catalog repo dropped the
// closed schema over it for that reason, and an entry is no longer rejected for
// setting a key the ownership table assigns to another layer -- progressDeadlineSeconds
// is a real one: the sglang chart takes it, and a model that loads for ninety
// minutes has to raise it or the rollout is marked failed while it is still coming up.
//
// Precedence, not validation, is what keeps this safe: the catalog merges first,
// so a site or a deploy still overrides anything an entry sets.
func TestEntryMaySetAnyChartKey(t *testing.T) {
	for _, y := range []string{
		"progressDeadlineSeconds: 7200",
		"replicaCount: 2",
		"schedulerName: volcano",
	} {
		raw := []byte(`apiVersion: catalog.swiss/v1
name: m
version: 1.0.0
variants:
  - id: sglang-tp2
    engine: sglang
    chart: {name: sglang, version: 0.7.1}
    requires: {gpus: 2}
    values:
      ` + y + "\n")
		e, err := ParseEntry(raw)
		if err != nil {
			t.Fatalf("%s: %v", y, err)
		}
		e.Source = Source{HF: "org/m"}
		if err := e.Validate(); err != nil {
			t.Errorf("%s: entry refused: %v", y, err)
		}
	}
}

// The keys that are genuinely wrong in a public repo are still wrong -- they are
// just not swiss-side validation any more. Nothing here rejects them, so the
// catalog repo review is what catches a namespace or a registry URL in an entry.
func TestEntryWithSiteKeyIsNoLongerRefused(t *testing.T) {
	raw := []byte(`apiVersion: catalog.swiss/v1
name: m
version: 1.0.0
variants:
  - id: sglang-tp2
    engine: sglang
    chart: {name: sglang, version: 0.7.1}
    requires: {gpus: 2}
    values:
      cache:
        hostPath: /mnt/cache
`)
	e, err := ParseEntry(raw)
	if err != nil {
		t.Fatal(err)
	}
	e.Source = Source{HF: "org/m"}
	if err := e.Validate(); err != nil {
		t.Errorf("entry refused: %v", err)
	}
}
