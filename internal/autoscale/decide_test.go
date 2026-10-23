package autoscale

import (
	"testing"
	"time"

	"github.com/modelsphere/swiss/internal/testutil/cases"
)

type decideCase struct {
	Name               string   `json:"name"`
	Description        string   `json:"description,omitempty"`
	Now                string   `json:"now,omitempty"`
	Config             Config   `json:"config"`
	Status             Status   `json:"status,omitempty"`
	Live               Live     `json:"live"`
	Decision           Decision `json:"decision"`
	Apply              string   `json:"apply,omitempty"`
	WantHold           string   `json:"wantHold"`
	WantScale          bool     `json:"wantScale"`
	WantCurrent        int      `json:"wantCurrent"`
	Want               int      `json:"want"`
	WantFailed         bool     `json:"wantFailed"`
	HasDecision        bool     `json:"hasDecision"`
	WantFailures       int      `json:"wantFailures,omitempty"`
	WantRetryAfter     string   `json:"wantRetryAfter,omitempty"`
	WantLastFailedAt   string   `json:"wantLastFailedAt,omitempty"`
	WantLastFailedWant *int     `json:"wantLastFailedWant,omitempty"`
}

func TestDecide(t *testing.T) {
	cases.Run(t, "testdata/decide", func(t *testing.T, tc decideCase) {
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		if tc.Now != "" {
			parsed, err := time.Parse(time.RFC3339, tc.Now)
			if err != nil {
				t.Fatalf("now: %v", err)
			}
			now = parsed
		}
		got := Decide(now, tc.Config, tc.Status, tc.Live, tc.Decision)
		switch tc.Apply {
		case "", "none":
		case "ok":
			got = NoteApply(now, got, ApplyOK)
		case "error":
			got = NoteApply(now, got, ApplyFailed)
		case "conflict":
			got = NoteApply(now, got, ApplyConflict)
		case "busy":
			got = NoteApply(now, got, ApplyBusy)
		default:
			t.Fatalf("apply %q", tc.Apply)
		}
		if got.HoldReason != tc.WantHold {
			t.Errorf("hold %q, want %q", got.HoldReason, tc.WantHold)
		}
		if got.Scale != tc.WantScale {
			t.Errorf("scale %v, want %v", got.Scale, tc.WantScale)
		}
		if got.Current != tc.WantCurrent {
			t.Errorf("current %d, want %d", got.Current, tc.WantCurrent)
		}
		if got.Want != tc.Want {
			t.Errorf("want %d, want %d", got.Want, tc.Want)
		}
		if got.Failed != tc.WantFailed {
			t.Errorf("failed %v, want %v", got.Failed, tc.WantFailed)
		}
		if tc.HasDecision != (got.Decision != nil) {
			t.Errorf("decision present %v, want %v", got.Decision != nil, tc.HasDecision)
		}
		if got.Decision != nil && tc.Decision.OK && *got.Decision != tc.Decision.Replicas {
			t.Errorf("decision %d, want %d", *got.Decision, tc.Decision.Replicas)
		}
		if got.Failures != tc.WantFailures {
			t.Errorf("failures %d, want %d", got.Failures, tc.WantFailures)
		}
		if got.RetryAfter != tc.WantRetryAfter {
			t.Errorf("retryAfter %q, want %q", got.RetryAfter, tc.WantRetryAfter)
		}
		if got.LastFailedAt != tc.WantLastFailedAt {
			t.Errorf("lastFailedAt %q, want %q", got.LastFailedAt, tc.WantLastFailedAt)
		}
		if !samePtr(got.LastFailedWant, tc.WantLastFailedWant) {
			t.Errorf("lastFailedWant %v, want %v", pint(got.LastFailedWant), pint(tc.WantLastFailedWant))
		}
	})
}

func samePtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func pint(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
