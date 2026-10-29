package llmsvc

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const CanaryPrefix = "canary.modelsphere.dev/"

type CanaryOwner struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	Verified   bool   `json:"verified"`
}

// CanaryOwnership keeps orphaned or inconsistent ownership protected too.
func CanaryOwnership(u *unstructured.Unstructured) *CanaryOwner {
	if u == nil {
		return nil
	}
	labels := u.GetLabels()
	for _, ref := range u.GetOwnerReferences() {
		if strings.HasPrefix(ref.APIVersion, CanaryPrefix) && ref.Kind == "LLMCanary" {
			return &CanaryOwner{
				APIVersion: ref.APIVersion, Kind: ref.Kind, Namespace: u.GetNamespace(), Name: ref.Name, UID: string(ref.UID),
				Verified: ref.Controller != nil && *ref.Controller && ref.UID != "" && ref.Name != "" &&
					labels[CanaryPrefix+"canary-name"] == ref.Name && labels[CanaryPrefix+"canary-namespace"] == u.GetNamespace() && labels[CanaryPrefix+"canary-uid"] == string(ref.UID),
			}
		}
	}
	for _, key := range []string{"canary-name", "canary-namespace", "canary-uid"} {
		if _, ok := labels[CanaryPrefix+key]; ok {
			return &CanaryOwner{APIVersion: CanaryPrefix + "v1alpha1", Kind: "LLMCanary", Namespace: labels[CanaryPrefix+"canary-namespace"], Name: labels[CanaryPrefix+"canary-name"], UID: labels[CanaryPrefix+"canary-uid"]}
		}
	}
	return nil
}
