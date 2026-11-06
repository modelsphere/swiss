package autoscale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"k8s.io/kube-openapi/pkg/validation/spec"
	"k8s.io/kube-openapi/pkg/validation/strfmt"
	"k8s.io/kube-openapi/pkg/validation/validate"
)

// ErrNoDecision is a 200 whose decisions contain no row for this service.
// The SLO may have no maximumDeployment; that is decision-gen's business.
var ErrNoDecision = errors.New("no decision")

// The second is the first renamed off inference.x-k8s.io; decision-gen switches
// to it once its consumers read both.
var decisionsAPIVersions = []string{
	"llmscaling.inference.x-k8s.io/v1alpha1",
	"llmscaling.inference.modelsphere.dev/v1alpha1",
}

// decisionsSchema is the part of decision-gen's GET /decisions this client
// reads, after its docs/openapi.yaml. A response is checked against it before
// it is decoded, so a missing count is an error, never a 0.
var decisionsSchema = object(props{
	"apiVersion": enum(decisionsAPIVersions...),
	"decisions": array(object(props{
		"namespace": str,
		"serviceId": str,
		"replicas":  object(props{"active": count}, "active"),
		// shape.pd is a pdengine row. Another shape is ignored.
		"shape": object(props{
			"pd": object(props{
				"slices":  count,
				"prefill": object(props{"replicas": count}, "replicas"),
				"decode":  object(props{"replicas": count}, "replicas"),
			}, "slices", "prefill", "decode"),
		}),
	}, "namespace", "serviceId", "replicas")),
}, "apiVersion", "decisions")

// decisionsResponse is a response that matched decisionsSchema.
type decisionsResponse struct {
	Decisions []decision `json:"decisions"`
}

type decision struct {
	Namespace string `json:"namespace"`
	ServiceID string `json:"serviceId"`
	Replicas  struct {
		Active int `json:"active"`
	} `json:"replicas"`
	Shape struct {
		PD *struct {
			Slices  int `json:"slices"`
			Prefill struct {
				Replicas int `json:"replicas"`
			} `json:"prefill"`
			Decode struct {
				Replicas int `json:"replicas"`
			} `json:"decode"`
		} `json:"pd"`
	} `json:"shape"`
}

// toDecision is the row as the loop applies it: replicas.active, or on a
// pdengine row shape.pd.slices with its per-slice prefill and decode. An off
// pdengine row reports active 0 while slices stays the count to run.
// liveReplicas is not read: the loop reads the DisaggregatedSet itself.
func (d decision) toDecision() *Decision {
	out := &Decision{OK: true, Replicas: d.Replicas.Active}
	if pd := d.Shape.PD; pd != nil {
		out.Replicas = pd.Slices
		out.PD = &Engines{Prefill: pd.Prefill.Replicas, Decode: pd.Decode.Replicas}
	}
	return out
}

// Query asks decision-gen for one service. A non-200 or a transport error is
// returned, never turned into a replica count of 0. namespace is always sent
// and always used as a match filter.
func Query(ctx context.Context, client *http.Client, base string, headers map[string]string, namespace, serviceID string) (*Decision, error) {
	if strings.TrimSpace(base) == "" {
		return nil, fmt.Errorf("serverAddress is empty")
	}
	if serviceID == "" {
		return nil, fmt.Errorf("serviceId is empty")
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	q := url.Values{}
	q.Set("namespace", namespace)
	q.Set("serviceId", serviceID)
	endpoint := strings.TrimRight(base, "/") + "/decisions?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query decision server: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query decision server: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to query decision server: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := body
		if len(snippet) > 1024 {
			snippet = snippet[:1024]
		}
		return nil, fmt.Errorf("decision server returned status %d: %s", resp.StatusCode, string(snippet))
	}
	return ParseDecisions(body, namespace, serviceID)
}

// ParseDecisions checks body against decisionsSchema and returns the one
// decision addressed to serviceID in namespace. Anything other than exactly
// one match is an error. Query filters the response to this service, so the
// schema check sees only its rows.
func ParseDecisions(body []byte, namespace, serviceID string) (*Decision, error) {
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode decision response: %w", err)
	}
	if res := validate.NewSchemaValidator(&decisionsSchema, nil, "", strfmt.Default).Validate(raw); res.HasErrors() {
		msgs := make([]string, len(res.Errors))
		for i, err := range res.Errors {
			msgs[i] = err.Error()
		}
		return nil, fmt.Errorf("decision response does not match decision-gen's schema: %s", strings.Join(msgs, "; "))
	}
	var resp decisionsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to decode decision response: %w", err)
	}
	var matches []decision
	for _, d := range resp.Decisions {
		if d.Namespace == namespace && d.ServiceID == serviceID {
			matches = append(matches, d)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0].toDecision(), nil
	case 0:
		return nil, fmt.Errorf("%w for serviceId %q in namespace %q among the %d returned",
			ErrNoDecision, serviceID, namespace, len(resp.Decisions))
	default:
		return nil, fmt.Errorf("%d decisions match serviceId %q in namespace %q, expected 1",
			len(matches), serviceID, namespace)
	}
}

// The schema builders for decisionsSchema.
type props = map[string]spec.Schema

var (
	str   = schema("string")
	count = func() spec.Schema {
		s := schema("integer")
		s.Minimum, s.Maximum = new(float64(0)), new(float64(math.MaxInt32))
		return s
	}()
)

func schema(typ string) spec.Schema {
	return spec.Schema{SchemaProps: spec.SchemaProps{Type: spec.StringOrArray{typ}}}
}

func object(p props, required ...string) spec.Schema {
	s := schema("object")
	s.Properties, s.Required = p, required
	return s
}

func array(items spec.Schema) spec.Schema {
	s := schema("array")
	s.Items = &spec.SchemaOrArray{Schema: &items}
	return s
}

func enum(values ...string) spec.Schema {
	s := str
	for _, v := range values {
		s.Enum = append(s.Enum, v)
	}
	return s
}
