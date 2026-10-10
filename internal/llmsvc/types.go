// Package llmsvc is swiss's view of an LLMService: its shape, the mapping to a
// plan, and the client that reads and writes it. The operator is a different
// module; this one speaks unstructured to the Kubernetes API.
package llmsvc

import (
	"fmt"

	"github.com/modelsphere/swiss/internal/values"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	Group    = "serving.modelsphere.dev"
	Version  = "v1alpha1"
	Resource = "llmservices"
	Kind     = "LLMService"

	CRDName    = "llmservices.serving.modelsphere.dev"
	APIVersion = Group + "/" + Version

	PhasePending  = "Pending"
	PhaseApplying = "Applying"
	PhaseApplied  = "Applied"
	PhaseFailed   = "Failed"

	ConditionApplied = "Applied"
	ConditionAdopted = "Adopted"

	SwissAnnotationPrefix = "swiss.modelsphere.dev/"

	AnnPlanHash    = SwissAnnotationPrefix + "plan-hash"
	AnnCatalog     = SwissAnnotationPrefix + "catalog"
	AnnCatalogName = SwissAnnotationPrefix + "catalog-name"
	AnnCatalogRef  = SwissAnnotationPrefix + "catalog-ref"
	AnnEntryDigest = SwissAnnotationPrefix + "entry-digest"
	AnnProfile     = SwissAnnotationPrefix + "profile"
	AnnAction      = SwissAnnotationPrefix + "action"
	AnnNote        = SwissAnnotationPrefix + "note"

	AnnDeletionPolicy = Group + "/deletion-policy"
	AnnForceConflicts = Group + "/force-conflicts"
)

var GVR = schema.GroupVersionResource{Group: Group, Version: Version, Resource: Resource}

// LLMService mirrors serving.modelsphere.dev/v1alpha1 LLMService. It exists so
// callers can build one without a generated client; the wire form is unstructured.
type LLMService struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   Spec    `json:"spec,omitempty"`
	Status *Status `json:"status,omitempty"`
}

type Spec struct {
	Chart   ChartSpec  `json:"chart"`
	Layers  []Layer    `json:"layers,omitempty"`
	Model   *ModelSpec `json:"model,omitempty"`
	Suspend bool       `json:"suspend,omitempty"`
}

type ChartSpec struct {
	Name           string          `json:"name,omitempty"`
	Repo           string          `json:"repo,omitempty"`
	Version        string          `json:"version,omitempty"`
	CredentialsRef *CredentialsRef `json:"credentialsRef,omitempty"`
}

type CredentialsRef struct {
	Name string `json:"name"`
}

type Layer struct {
	Name   string      `json:"name"`
	Values values.Tree `json:"values,omitempty"`
}

type ModelSpec struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	Variant string `json:"variant,omitempty"`
	HF      string `json:"hf,omitempty"`
	Engine  string `json:"engine,omitempty"`
}

type Status struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Phase              string             `json:"phase,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	Message            string             `json:"message,omitempty"`
	Chart              *ChartStatus       `json:"chart,omitempty"`
	AppliedHash        string             `json:"appliedHash,omitempty"`
	Helm               *HelmStatus        `json:"helm,omitempty"`
	History            []HistoryEntry     `json:"history,omitempty"`
}

type ChartStatus struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

type HelmStatus struct {
	Revision int64  `json:"revision,omitempty"`
	Status   string `json:"status,omitempty"`
}

// HistoryEntry is one applied revision, newest first on the object. Annotations
// are the swiss.modelsphere.dev keys the operator copied at apply time.
type HistoryEntry struct {
	Revision           int64             `json:"revision"`
	Hash               string            `json:"hash,omitempty"`
	AppliedAt          metav1.Time       `json:"appliedAt,omitempty,omitzero"`
	ControllerRevision string            `json:"controllerRevision,omitempty"`
	Annotations        map[string]string `json:"annotations,omitempty"`
	ForceConflicts     bool              `json:"forceConflicts,omitempty"`
	Action             string            `json:"action,omitempty"`
}

func ToUnstructured(s *LLMService) (*unstructured.Unstructured, error) {
	if s == nil {
		return nil, fmt.Errorf("nil LLMService")
	}
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(s)
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{Object: m}
	if u.GetAPIVersion() == "" {
		u.SetAPIVersion(APIVersion)
	}
	if u.GetKind() == "" {
		u.SetKind(Kind)
	}
	return u, nil
}

func FromUnstructured(u *unstructured.Unstructured) (*LLMService, error) {
	if u == nil {
		return nil, fmt.Errorf("nil object")
	}
	var s LLMService
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
