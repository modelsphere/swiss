package llmsvc

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clientsetfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestServed(t *testing.T) {
	ctx := t.Context()

	ok, err := New(newDyn(), emptyDisco(), nil).Served(ctx)
	if err != nil || ok {
		t.Fatalf("missing CRD: served %v err %v", ok, err)
	}

	d := emptyDisco()
	d.Resources = []*metav1.APIResourceList{{
		GroupVersion: "serving.modelsphere.dev/v1alpha1",
		APIResources: []metav1.APIResource{{Name: "others", Kind: "Other", Namespaced: true}},
	}}
	ok, err = New(newDyn(), d, nil).Served(ctx)
	if err != nil || ok {
		t.Fatalf("group without llmservices: served %v err %v", ok, err)
	}

	ok, err = New(newDyn(), servedDisco(), nil).Served(ctx)
	if err != nil || !ok {
		t.Fatalf("served %v err %v", ok, err)
	}

	d = emptyDisco()
	d.PrependReactor("*", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("discovery down")
	})
	ok, err = New(newDyn(), d, nil).Served(ctx)
	if err == nil || ok || apierrors.IsNotFound(err) {
		t.Fatalf("discovery error should surface: served %v err %v", ok, err)
	}
}

func TestCreateAlreadyExists(t *testing.T) {
	ctx := t.Context()
	c := New(newDyn(), nil, nil)
	if _, err := c.Create(ctx, obj("models", "qwen")); err != nil {
		t.Fatal(err)
	}
	_, err := c.Create(ctx, obj("models", "qwen"))
	if !apierrors.IsAlreadyExists(err) {
		t.Fatalf("got %v", err)
	}
	got, err := c.Get(ctx, "models", "qwen")
	if err != nil || got.GetName() != "qwen" {
		t.Fatalf("get %+v %v", got, err)
	}
}

func TestUpdateConflict(t *testing.T) {
	ctx := t.Context()
	dyn := newDyn()
	c := New(dyn, nil, nil)
	created, err := c.Create(ctx, obj("models", "qwen"))
	if err != nil {
		t.Fatal(err)
	}

	// The object tracker does not check resourceVersion. Reject anything but the
	// version the caller claims to have read, which is what the apiserver does.
	dyn.PrependReactor("update", "llmservices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		u := action.(k8stesting.UpdateAction).GetObject().(*unstructured.Unstructured)
		if u.GetResourceVersion() != "2" {
			return true, nil, apierrors.NewConflict(GVR.GroupResource(), u.GetName(), fmt.Errorf("stale"))
		}
		return false, nil, nil
	})

	stale := created.DeepCopy()
	stale.SetResourceVersion("1")
	if err := unstructured.SetNestedField(stale.Object, "0.9.0", "spec", "chart", "version"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Update(ctx, stale); !apierrors.IsConflict(err) {
		t.Fatalf("stale resourceVersion: %v", err)
	}

	fresh := created.DeepCopy()
	fresh.SetResourceVersion("2")
	if err := unstructured.SetNestedField(fresh.Object, "0.9.0", "spec", "chart", "version"); err != nil {
		t.Fatal(err)
	}
	got, err := c.Update(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	ver, _, _ := unstructured.NestedString(got.Object, "spec", "chart", "version")
	if ver != "0.9.0" {
		t.Fatalf("version %q", ver)
	}
}

func TestWaitApplied(t *testing.T) {
	for _, phase := range []string{"Applied", "Failed"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			c := New(newDyn(), nil, nil)
			c.Interval = 5 * time.Millisecond

			u := obj("models", "qwen")
			u.SetGeneration(4)
			if err := unstructured.SetNestedField(u.Object, "Applied", "status", "phase"); err != nil {
				t.Fatal(err)
			}
			if err := unstructured.SetNestedField(u.Object, int64(1), "status", "observedGeneration"); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Create(ctx, u); err != nil {
				t.Fatal(err)
			}

			errc := make(chan error, 1)
			go func() {
				time.Sleep(30 * time.Millisecond)
				got, err := c.Get(ctx, "models", "qwen")
				if err != nil {
					errc <- err
					return
				}
				if err := unstructured.SetNestedField(got.Object, phase, "status", "phase"); err != nil {
					errc <- err
					return
				}
				if err := unstructured.SetNestedField(got.Object, int64(4), "status", "observedGeneration"); err != nil {
					errc <- err
					return
				}
				if phase == "Failed" {
					if err := unstructured.SetNestedField(got.Object, "chart pull denied", "status", "message"); err != nil {
						errc <- err
						return
					}
				}
				_, err = c.Update(ctx, got)
				errc <- err
			}()

			final, err := c.WaitApplied(ctx, "models", "qwen", 4)
			if err != nil {
				t.Fatal(err)
			}
			if err := <-errc; err != nil {
				t.Fatal(err)
			}
			obj, err := FromUnstructured(final)
			if err != nil {
				t.Fatal(err)
			}
			if obj.Status == nil || obj.Status.ObservedGeneration != 4 || obj.Status.Phase != phase {
				t.Fatalf("status %+v", obj.Status)
			}
			if phase == "Failed" && obj.Status.Message != "chart pull denied" {
				t.Fatalf("message %q", obj.Status.Message)
			}
		})
	}
}

func TestWaitAppliedReadsJSONNumbers(t *testing.T) {
	// A real API server decodes numbers as float64. Polling for an hour would
	// only return if that shape still counts as Applied.
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	c := New(newDyn(), nil, nil)
	c.Interval = time.Hour
	u := obj("models", "qwen")
	u.Object["status"] = map[string]any{
		"observedGeneration": float64(3),
		"phase":              "Applied",
	}
	if _, err := c.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	final, err := c.WaitApplied(ctx, "models", "qwen", 3)
	if err != nil {
		t.Fatal(err)
	}
	got, err := FromUnstructured(final)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == nil || got.Status.ObservedGeneration != 3 || got.Status.Phase != "Applied" {
		t.Fatalf("status %+v", got.Status)
	}
}

func TestWaitAppliedTimesOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	c := New(newDyn(), nil, nil)
	c.Interval = 5 * time.Millisecond
	u := obj("models", "qwen")
	if err := unstructured.SetNestedField(u.Object, "Pending", "status", "phase"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	_, err := c.WaitApplied(ctx, "models", "qwen", 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestWaitGone(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	c := New(newDyn(), nil, nil)
	c.Interval = 5 * time.Millisecond
	if err := c.WaitGone(ctx, "models", "missing"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Create(ctx, obj("models", "qwen")); err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		errc <- c.Delete(ctx, "models", "qwen")
	}()
	if err := c.WaitGone(ctx, "models", "qwen"); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func TestListHonoursNamespaceScope(t *testing.T) {
	ctx := t.Context()
	dyn := newDyn()
	c := New(dyn, nil, nil)
	for _, ns := range []string{"models", "kimi", "other"} {
		if _, err := c.Create(ctx, obj(ns, ns+"-rel")); err != nil {
			t.Fatal(err)
		}
	}

	dyn.ClearActions()
	got, err := c.List(ctx, []string{"models", "kimi"})
	if err != nil {
		t.Fatal(err)
	}
	if names := objNames(got); !eq(names, []string{"kimi/kimi-rel", "models/models-rel"}) {
		t.Fatalf("scoped list %v", names)
	}
	if lists := listNamespaces(dyn); !eq(lists, []string{"models", "kimi"}) {
		t.Fatalf("list requests %v", lists)
	}

	dyn.ClearActions()
	got, err = c.List(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if names := objNames(got); !eq(names, []string{"kimi/kimi-rel", "models/models-rel", "other/other-rel"}) {
		t.Fatalf("cluster-wide %v", names)
	}
	if lists := listNamespaces(dyn); !eq(lists, []string{""}) {
		t.Fatalf("cluster-wide requests %v", lists)
	}

	dyn.PrependReactor("list", "llmservices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "kimi" {
			return true, nil, apierrors.NewForbidden(GVR.GroupResource(), "", fmt.Errorf("no"))
		}
		return false, nil, nil
	})
	if _, err := c.List(ctx, []string{"models", "kimi"}); !apierrors.IsForbidden(err) {
		t.Fatalf("scoped error: %v", err)
	}
}

func TestControllerRevisions(t *testing.T) {
	ctx := t.Context()
	raw := []byte(`{"spec":{"chart":{"name":"sglang"}}}`)
	kube := clientsetfake.NewSimpleClientset(
		&appsv1.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{Namespace: "models", Name: "qwen-7c9d"},
			Revision:   12,
			Data:       runtime.RawExtension{Raw: raw},
		},
		&appsv1.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{Namespace: "models", Name: "qwen-aaaa"},
			Revision:   11,
		},
	)
	c := New(nil, nil, kube)
	got, err := c.ControllerRevisions(ctx, "models", []string{"qwen-aaaa", "qwen-7c9d"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "qwen-aaaa" || got[0].Revision != 11 || got[1].Name != "qwen-7c9d" || got[1].Revision != 12 {
		t.Fatalf("%+v %+v", got[0], got[1])
	}
	if string(got[1].Data.Raw) != string(raw) {
		t.Fatalf("snapshot %s", got[1].Data.Raw)
	}
	if _, err := c.ControllerRevisions(ctx, "models", []string{"missing"}); !apierrors.IsNotFound(err) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := New(nil, nil, nil).ControllerRevisions(ctx, "models", []string{"qwen-7c9d"}); err == nil {
		t.Fatal("expected an error without a typed client")
	}
}

func newDyn(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{GVR: Kind + "List"}, objs...)
}

func emptyDisco() *fake.FakeDiscovery {
	return &fake.FakeDiscovery{Fake: &k8stesting.Fake{}}
}

func servedDisco() *fake.FakeDiscovery {
	d := emptyDisco()
	d.Resources = []*metav1.APIResourceList{{
		GroupVersion: "serving.modelsphere.dev/v1alpha1",
		APIResources: []metav1.APIResource{
			{Name: "llmservices", Kind: "LLMService", Namespaced: true},
			{Name: "llmservices/status", Kind: "LLMService", Namespaced: true},
		},
	}}
	return d
}

func obj(ns, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": APIVersion,
		"kind":       Kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": ns,
		},
		"spec": map[string]any{
			"chart": map[string]any{"name": "sglang", "repo": "oci://ghcr.io/modelsphere/charts", "version": "0.8.0"},
		},
	}}
}

func objNames(items []*unstructured.Unstructured) []string {
	out := make([]string, len(items))
	for i, u := range items {
		out[i] = u.GetNamespace() + "/" + u.GetName()
	}
	return out
}

func listNamespaces(dyn *dynamicfake.FakeDynamicClient) []string {
	var out []string
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "list" && a.GetResource() == GVR {
			out = append(out, a.GetNamespace())
		}
	}
	return out
}

func eq(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
