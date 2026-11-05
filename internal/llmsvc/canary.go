package llmsvc

import (
	"encoding/json"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	CanaryPrefix  = "canary.modelsphere.dev/"
	AssignmentKey = CanaryPrefix + "assignment"
)

type CanaryOwner struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	Verified   bool   `json:"verified"`
}

type CanaryAssignment struct {
	APIVersion    string `json:"apiVersion"`
	Kind          string `json:"kind"`
	Namespace     string `json:"namespace"`
	Name          string `json:"name"`
	UID           string `json:"uid"`
	Role          string `json:"role,omitempty"`
	Arm           string `json:"arm,omitempty"`
	ConfigDigest  string `json:"configDigest,omitempty"`
	ConfigVersion string `json:"configVersion,omitempty"`
	Verified      bool   `json:"verified"`
}

// CanaryOwnership reports incomplete ownership metadata with Verified=false.
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
			ns := labels[CanaryPrefix+"canary-namespace"]
			if ns == "" {
				ns = u.GetNamespace()
			}
			return &CanaryOwner{APIVersion: CanaryPrefix + "v1alpha1", Kind: "LLMCanary", Namespace: ns, Name: labels[CanaryPrefix+"canary-name"], UID: labels[CanaryPrefix+"canary-uid"]}
		}
	}
	return nil
}

// CanaryAssigned reports malformed or incomplete assignments with Verified=false.
func CanaryAssigned(u *unstructured.Unstructured) *CanaryAssignment {
	if u == nil {
		return nil
	}
	raw, ok := u.GetAnnotations()[AssignmentKey]
	if !ok {
		return nil
	}
	var a CanaryAssignment
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &a) != nil {
		return &CanaryAssignment{
			APIVersion: CanaryPrefix + "v1alpha1",
			Kind:       "LLMCanary",
			Namespace:  u.GetNamespace(),
			Verified:   false,
		}
	}
	a.APIVersion = strings.TrimSpace(a.APIVersion)
	a.Kind = strings.TrimSpace(a.Kind)
	a.Namespace = strings.TrimSpace(a.Namespace)
	a.Name = strings.TrimSpace(a.Name)
	a.UID = strings.TrimSpace(a.UID)
	a.Role = strings.TrimSpace(a.Role)
	a.Arm = strings.TrimSpace(a.Arm)
	a.ConfigDigest = strings.TrimSpace(a.ConfigDigest)
	a.ConfigVersion = strings.TrimSpace(a.ConfigVersion)

	validRole := a.Role == "production" || a.Role == "arm"
	validArm := (a.Role == "arm" && a.Arm != "") || (a.Role == "production" && a.Arm == "")
	sameNS := a.Role == "production" || u.GetNamespace() == "" || a.Namespace == u.GetNamespace()
	a.Verified = a.APIVersion == CanaryPrefix+"v1alpha1" &&
		a.Kind == "LLMCanary" &&
		a.Namespace != "" &&
		a.Name != "" &&
		a.UID != "" &&
		validRole &&
		validArm &&
		sameNS
	if a.APIVersion == "" {
		a.APIVersion = CanaryPrefix + "v1alpha1"
	}
	if a.Kind == "" {
		a.Kind = "LLMCanary"
	}
	if a.Namespace == "" {
		a.Namespace = u.GetNamespace()
	}
	return &a
}
