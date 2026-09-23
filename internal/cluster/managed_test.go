package cluster

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
)

// planMeta is a plan ConfigMap as ManagedRefs sees it: name, namespace and the
// label, with none of the contents. That is the whole point of the metadata
// client -- the real object carries every values document the release was
// applied with.
func planMeta(namespace, name string) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "swiss"},
		},
	}
}

func metaClient(objs ...*metav1.PartialObjectMetadata) *metadatafake.FakeMetadataClient {
	scheme := metadatafake.NewTestScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		panic(err)
	}
	out := make([]runtime.Object, len(objs))
	for i, o := range objs {
		out[i] = o
	}
	return metadatafake.NewSimpleMetadataClient(scheme, out...)
}

// The managed set is the plan ConfigMaps. swiss stamps its managed-by label on
// everything it writes, so the site profile carries it too -- the name prefix is
// what separates a plan from it, and the release name is what is left.
func TestManagedRefsListsPlansAndNotTheProfile(t *testing.T) {
	md := metaClient(
		planMeta("modelforge", PlanConfigMapPrefix+"kimi-k3"),
		planMeta("modelforge", PlanConfigMapPrefix+"glm-53"),
		planMeta("modelforge", "swiss-profile"), // same label, not a plan
	)
	k := NewKubeWithClients(fake.NewSimpleClientset(), md)

	refs, err := k.ManagedRefs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("want the two plans and not the profile, got %+v", refs)
	}
	// Sorted, and the release name is the ConfigMap name without the prefix.
	if refs[0].Name != "glm-53" || refs[0].Namespace != "modelforge" {
		t.Errorf("got %+v, want glm-53 first", refs[0])
	}
	if refs[1].Name != "kimi-k3" {
		t.Errorf("got %+v, want kimi-k3 second", refs[1])
	}
}

func TestManagedRefsNeedsAMetadataClient(t *testing.T) {
	k := NewKubeWithClient(fake.NewSimpleClientset())
	if _, err := k.ManagedRefs(context.Background()); err == nil {
		t.Error("without a metadata client this must say so rather than silently listing everything")
	}
}

func planCM(ns, release string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: PlanConfigMapPrefix + release},
		Data:       map[string]string{planKey: "source:\n  model: glm5.3\n"},
	}
}

// helm keeps every revision side by side. Only the newest is live, and only it
// is worth gunzipping -- the older ones are whole rendered manifests.
func TestReleaseTakesTheHighestRevision(t *testing.T) {
	cs := fake.NewSimpleClientset(
		planCM("modelforge", "glm-53"),
		helmSecret("modelforge", "glm-53", 1, "superseded", "sglang", "0.7.0"),
		helmSecret("modelforge", "glm-53", 4, "deployed", "sglang", "0.7.1"),
		// A different release in the same namespace must not be picked up: the
		// lookup is by helm's own name label, not a scan.
		helmSecret("modelforge", "kimi-k3", 9, "deployed", "sglang", "0.7.1"),
	)

	rel, err := NewKubeWithClient(cs).Release(context.Background(), "modelforge", "glm-53")
	if err != nil {
		t.Fatal(err)
	}
	if rel == nil {
		t.Fatal("the plan and the release both exist")
	}
	if rel.Revision != 4 || rel.Status != "deployed" || rel.Chart != "sglang-0.7.1" {
		t.Errorf("want the newest revision, got %d %q %q", rel.Revision, rel.Status, rel.Chart)
	}
	if rel.SwissFiles[planKey] == "" {
		t.Error("the plan must come back with it")
	}
}

// A plan whose release is gone is an uninstall that did not finish cleaning up.
// The row still belongs in the view -- that is how it gets noticed -- so this is
// not an error.
func TestReleaseWithNoLiveRelease(t *testing.T) {
	cs := fake.NewSimpleClientset(planCM("modelforge", "glm-53"))

	rel, err := NewKubeWithClient(cs).Release(context.Background(), "modelforge", "glm-53")
	if err != nil {
		t.Fatal(err)
	}
	if rel == nil || rel.Revision != 0 || rel.SwissFiles[planKey] == "" {
		t.Errorf("want the plan with no live state, got %+v", rel)
	}
}
