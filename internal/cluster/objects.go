package cluster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"gopkg.in/yaml.v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ObjectRef names one object a release rendered, as its manifest spells it.
type ObjectRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
}

// Object is a custom resource read live: the spec as applied, and the status
// its controller last reported.
type Object struct {
	Generation int64          `json:"generation,omitempty"`
	Created    time.Time      `json:"created,omitzero"`
	Spec       map[string]any `json:"spec,omitempty"`
	Status     map[string]any `json:"status,omitempty"`
}

// WatchedKinds are the custom resources a release page reports on, with their
// plural resource names. Read from the manifest by kind, so the group is
// whichever one the chart version rendered.
var WatchedKinds = map[string]string{
	"ModelRoute":        "modelroutes",
	"LLMScaler":         "llmscalers",
	"LLMSLORequirement": "llmslorequirements",
}

// manifestObjects lists the watched objects in a rendered helm manifest. A
// namespace the template left out is the release's.
func manifestObjects(manifest, namespace string) []ObjectRef {
	var out []ObjectRef
	dec := yaml.NewDecoder(bytes.NewReader([]byte(manifest)))
	for {
		var doc struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
			Metadata   struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
		}
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// helm already parsed this manifest; a document yaml.v3 cannot read
			// ends the scan rather than guessing at the rest.
			break
		}
		if _, ok := WatchedKinds[doc.Kind]; !ok || doc.Metadata.Name == "" {
			continue
		}
		ns := doc.Metadata.Namespace
		if ns == "" {
			ns = namespace
		}
		out = append(out, ObjectRef{APIVersion: doc.APIVersion, Kind: doc.Kind, Namespace: ns, Name: doc.Metadata.Name})
	}
	return out
}

func (r ObjectRef) gvr() (schema.GroupVersionResource, error) {
	gv, err := schema.ParseGroupVersion(r.APIVersion)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	res, ok := WatchedKinds[r.Kind]
	if !ok {
		return schema.GroupVersionResource{}, fmt.Errorf("%s is not a kind swiss reads", r.Kind)
	}
	return gv.WithResource(res), nil
}

// Object reads one custom resource. Nil, nil when it is gone: the manifest
// names what helm applied, not what is still there.
func (k *Kube) Object(ctx context.Context, ref ObjectRef) (*Object, error) {
	if k.dyn == nil {
		return nil, fmt.Errorf("no dynamic client: Object needs one")
	}
	gvr, err := ref.gvr()
	if err != nil {
		return nil, err
	}
	u, err := k.dyn.Resource(gvr).Namespace(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get %s %s/%s: %w", ref.Kind, ref.Namespace, ref.Name, err)
	}
	o := &Object{Generation: u.GetGeneration(), Created: u.GetCreationTimestamp().Time}
	o.Spec, _ = u.Object["spec"].(map[string]any)
	o.Status, _ = u.Object["status"].(map[string]any)
	return o, nil
}
