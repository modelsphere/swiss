package autoscale

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const decisionsAPIVersion = "llmscaling.inference.x-k8s.io/v1alpha1"

// decisionsResponse is the part of the decision-server payload we read.
type decisionsResponse struct {
	APIVersion string     `json:"apiVersion"`
	Decisions  []decision `json:"decisions"`
}

type decision struct {
	Namespace string `json:"namespace"`
	ServiceID string `json:"serviceId"`
	Replicas  struct {
		// Active is a pointer so an omitted count stays distinguishable from 0.
		Active *int64 `json:"active"`
	} `json:"replicas"`
}

// Query asks decision-gen for one service. A non-200 or a transport error is
// returned, never turned into a replica count of 0. namespace is always sent
// and always used as a match filter.
func Query(ctx context.Context, client *http.Client, base string, headers map[string]string, namespace, serviceID string) (int, error) {
	if strings.TrimSpace(base) == "" {
		return 0, fmt.Errorf("serverAddress is empty")
	}
	if serviceID == "" {
		return 0, fmt.Errorf("serviceId is empty")
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
		return 0, fmt.Errorf("failed to query decision server: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to query decision server: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, fmt.Errorf("failed to query decision server: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := body
		if len(snippet) > 1024 {
			snippet = snippet[:1024]
		}
		return 0, fmt.Errorf("decision server returned status %d: %s", resp.StatusCode, string(snippet))
	}
	return ParseDecisions(body, namespace, serviceID)
}

// ParseDecisions reads replicas.active for the one decision addressed to
// serviceID in namespace. Anything other than exactly one match is an error.
func ParseDecisions(body []byte, namespace, serviceID string) (int, error) {
	var decoded decisionsResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return 0, fmt.Errorf("failed to decode decision response: %w", err)
	}
	if decoded.APIVersion != decisionsAPIVersion {
		return 0, fmt.Errorf("unsupported decision schema %q, this client only understands %q",
			decoded.APIVersion, decisionsAPIVersion)
	}
	var matches []decision
	for _, d := range decoded.Decisions {
		if d.ServiceID != serviceID || d.Namespace != namespace {
			continue
		}
		matches = append(matches, d)
	}
	switch len(matches) {
	case 1:
	case 0:
		return 0, fmt.Errorf("no decision for serviceId %q in namespace %q among the %d returned",
			serviceID, namespace, len(decoded.Decisions))
	default:
		return 0, fmt.Errorf("%d decisions match serviceId %q in namespace %q, expected 1",
			len(matches), serviceID, namespace)
	}
	active := matches[0].Replicas.Active
	if active == nil {
		return 0, fmt.Errorf("decision for serviceId %q carries no replicas.active", serviceID)
	}
	if *active < 0 || *active > math.MaxInt32 {
		return 0, fmt.Errorf("decision for serviceId %q recommends %d replicas, which is out of range", serviceID, *active)
	}
	return int(*active), nil
}
