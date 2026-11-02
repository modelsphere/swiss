package autoscale

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/testutil/cases"
)

type decisionCase struct {
	Name           string            `json:"name"`
	Description    string            `json:"description,omitempty"`
	Namespace      string            `json:"namespace"`
	ServiceID      string            `json:"serviceId"`
	Status         int               `json:"status,omitempty"`
	Body           string            `json:"body,omitempty"`
	Transport      bool              `json:"transport,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Want           *int              `json:"want,omitempty"`
	ErrorContains  string            `json:"errorContains,omitempty"`
	WantNoDecision bool              `json:"wantNoDecision,omitempty"`
	WantHeader     string            `json:"wantHeader,omitempty"`
}

func TestDecisions(t *testing.T) {
	cases.Run(t, "testdata/decisions", func(t *testing.T, tc decisionCase) {
		base := "http://127.0.0.1:1"
		var client *http.Client
		if !tc.Transport {
			var gotHeader, gotNS, gotID string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotHeader = r.Header.Get("X-Test")
				gotNS = r.URL.Query().Get("namespace")
				gotID = r.URL.Query().Get("serviceId")
				if tc.Status != 0 && tc.Status != http.StatusOK {
					http.Error(w, tc.Body, tc.Status)
					return
				}
				_, _ = io.WriteString(w, tc.Body)
			}))
			t.Cleanup(srv.Close)
			base = srv.URL
			client = srv.Client()
			t.Cleanup(func() {
				if gotNS != tc.Namespace || gotID != tc.ServiceID {
					t.Errorf("query namespace=%q serviceId=%q, want %q %q", gotNS, gotID, tc.Namespace, tc.ServiceID)
				}
				if tc.WantHeader != "" && gotHeader != tc.WantHeader {
					t.Errorf("X-Test %q, want %q", gotHeader, tc.WantHeader)
				}
			})
		}
		got, err := Query(t.Context(), client, base, tc.Headers, tc.Namespace, tc.ServiceID)
		if tc.ErrorContains != "" {
			if err == nil || !strings.Contains(err.Error(), tc.ErrorContains) {
				t.Fatalf("error %v, want substring %q", err, tc.ErrorContains)
			}
			if tc.WantNoDecision && !errors.Is(err, ErrNoDecision) {
				t.Fatalf("error %v, want ErrNoDecision", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if tc.Want == nil {
			t.Fatal("success case needs want")
		}
		if got != *tc.Want {
			t.Fatalf("replicas %d, want %d", got, *tc.Want)
		}
	})
}
