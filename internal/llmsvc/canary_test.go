package llmsvc

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCanaryOwnershipProtectsIncompleteMetadata(t *testing.T) {
	controller := true
	ref := metav1.OwnerReference{APIVersion: "canary.modelsphere.dev/v1alpha1", Kind: "LLMCanary", Name: "demo", UID: "old-uid", Controller: &controller}
	labels := map[string]string{CanaryPrefix + "canary-name": "demo", CanaryPrefix + "canary-namespace": "canary", CanaryPrefix + "canary-uid": "old-uid"}
	for _, tc := range []struct {
		name                string
		refs                []metav1.OwnerReference
		labels              map[string]string
		protected, verified bool
	}{
		{"ordinary", nil, nil, false, false},
		{"matching", []metav1.OwnerReference{ref}, labels, true, true},
		{"removed labels", []metav1.OwnerReference{ref}, nil, true, false},
		{"removed reference", nil, labels, true, false},
		{"different UID", []metav1.OwnerReference{ref}, map[string]string{CanaryPrefix + "canary-uid": "new-uid"}, true, false},
		{"empty marker", nil, map[string]string{CanaryPrefix + "canary-name": ""}, true, false},
		{"unrelated owner", []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "demo", UID: "old-uid"}}, nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &unstructured.Unstructured{}
			u.SetNamespace("canary")
			u.SetOwnerReferences(tc.refs)
			u.SetLabels(tc.labels)
			owner := CanaryOwnership(u)
			if (owner != nil) != tc.protected || (owner != nil && owner.Verified != tc.verified) {
				t.Fatalf("%+v", owner)
			}
		})
	}
}
