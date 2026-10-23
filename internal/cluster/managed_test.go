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
		planMeta("models", PlanConfigMapPrefix+"kimi-k3"),
		planMeta("models", PlanConfigMapPrefix+"glm-53"),
		planMeta("models", "swiss-profile"), // same label, not a plan
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
	if refs[0].Name != "glm-53" || refs[0].Namespace != "models" {
		t.Errorf("got %+v, want glm-53 first", refs[0])
	}
	if refs[1].Name != "kimi-k3" {
		t.Errorf("got %+v, want kimi-k3 second", refs[1])
	}
}

func TestManagedRefsReturnsThePhaseLabel(t *testing.T) {
	md := metaClient(planMetaLabeled("models", PlanConfigMapPrefix+"glm-53", map[string]string{
		LabelPhase: "uninstalling",
	}))
	k := NewKubeWithClients(fake.NewSimpleClientset(), md)

	refs, err := k.ManagedRefs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Namespace != "models" || refs[0].Name != "glm-53" {
		t.Fatalf("got %+v", refs)
	}
	if refs[0].Labels[LabelPhase] != "uninstalling" {
		t.Fatalf("the phase label is what a restart reads, got %+v", refs[0].Labels)
	}
}

// PutConfigMap replaces the label set. A later write with no phase label has
// to drop the uninstalling marker, or a finished release stays in that set.
func TestPutConfigMapReplacesPhaseLabel(t *testing.T) {
	cs := fake.NewSimpleClientset()
	k := NewKubeWithClient(cs)
	ctx := context.Background()
	ref := "models/" + PlanConfigMapPrefix + "glm-53"
	data := map[string]string{planKey: "source:\n  model: glm\n"}

	if err := k.PutConfigMap(ctx, ref, data, map[string]string{LabelPhase: "uninstalling"}); err != nil {
		t.Fatal(err)
	}
	cm, err := cs.CoreV1().ConfigMaps("models").Get(ctx, PlanConfigMapPrefix+"glm-53", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cm.Labels["app.kubernetes.io/managed-by"] != "swiss" || cm.Labels[LabelPhase] != "uninstalling" {
		t.Fatalf("labels %+v", cm.Labels)
	}

	if err := k.PutConfigMap(ctx, ref, data, nil); err != nil {
		t.Fatal(err)
	}
	cm, err = cs.CoreV1().ConfigMaps("models").Get(ctx, PlanConfigMapPrefix+"glm-53", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cm.Labels["app.kubernetes.io/managed-by"] != "swiss" {
		t.Fatalf("managed-by must stay, labels %+v", cm.Labels)
	}
	if _, ok := cm.Labels[LabelPhase]; ok {
		t.Fatalf("nil labels must clear the phase marker, labels %+v", cm.Labels)
	}
}

func planMetaLabeled(namespace, name string, extra map[string]string) *metav1.PartialObjectMetadata {
	o := planMeta(namespace, name)
	for k, v := range extra {
		o.Labels[k] = v
	}
	return o
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
		planCM("models", "glm-53"),
		helmSecret("models", "glm-53", 1, "superseded", "sglang", "0.7.0"),
		helmSecret("models", "glm-53", 4, "deployed", "sglang", "0.7.1"),
		// A different release in the same namespace must not be picked up: the
		// lookup is by helm's own name label, not a scan.
		helmSecret("models", "kimi-k3", 9, "deployed", "sglang", "0.7.1"),
	)

	rel, err := NewKubeWithClient(cs).Release(context.Background(), "models", "glm-53")
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
	cs := fake.NewSimpleClientset(planCM("models", "glm-53"))

	rel, err := NewKubeWithClient(cs).Release(context.Background(), "models", "glm-53")
	if err != nil {
		t.Fatal(err)
	}
	if rel == nil || rel.Revision != 0 || rel.SwissFiles[planKey] == "" {
		t.Errorf("want the plan with no live state, got %+v", rel)
	}
}
