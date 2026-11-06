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
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Input       decisionInput    `json:"input"`
	Expected    decisionExpected `json:"expected"`
}

type decisionInput struct {
	Namespace string            `json:"namespace"`
	ServiceID string            `json:"serviceId"`
	Status    int               `json:"status,omitempty"`
	Body      string            `json:"body,omitempty"`
	Transport bool              `json:"transport,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

type decisionExpected struct {
	Replicas      *int     `json:"replicas,omitempty"`
	PD            *Engines `json:"pd,omitempty"`
	ErrorContains string   `json:"errorContains,omitempty"`
	NoDecision    bool     `json:"noDecision,omitempty"`
	Header        string   `json:"header,omitempty"`
}

func TestDecisions(t *testing.T) {
	cases.Run(t, "testdata/decisions", func(t *testing.T, tc decisionCase) {
		in, exp := tc.Input, tc.Expected
		base := "http://127.0.0.1:1"
		var client *http.Client
		if !in.Transport {
			var gotHeader, gotNS, gotID string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotHeader = r.Header.Get("X-Test")
				gotNS = r.URL.Query().Get("namespace")
				gotID = r.URL.Query().Get("serviceId")
				if in.Status != 0 && in.Status != http.StatusOK {
					http.Error(w, in.Body, in.Status)
					return
				}
				_, _ = io.WriteString(w, in.Body)
			}))
			t.Cleanup(srv.Close)
			base = srv.URL
			client = srv.Client()
			t.Cleanup(func() {
				if gotNS != in.Namespace || gotID != in.ServiceID {
					t.Errorf("query namespace=%q serviceId=%q, want %q %q", gotNS, gotID, in.Namespace, in.ServiceID)
				}
				if exp.Header != "" && gotHeader != exp.Header {
					t.Errorf("X-Test %q, want %q", gotHeader, exp.Header)
				}
			})
		}
		got, err := Query(t.Context(), client, base, in.Headers, in.Namespace, in.ServiceID)
		if exp.ErrorContains != "" {
			if err == nil || !strings.Contains(err.Error(), exp.ErrorContains) {
				t.Fatalf("error %v, want substring %q", err, exp.ErrorContains)
			}
			if exp.NoDecision && !errors.Is(err, ErrNoDecision) {
				t.Fatalf("error %v, want ErrNoDecision", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if exp.Replicas == nil {
			t.Fatal("success case needs expected.replicas")
		}
		if !got.OK {
			t.Fatal("decision not OK")
		}
		if got.Replicas != *exp.Replicas {
			t.Fatalf("replicas %d, want %d", got.Replicas, *exp.Replicas)
		}
		switch {
		case exp.PD == nil && got.PD != nil:
			t.Fatalf("pd %+v, want none", *got.PD)
		case exp.PD != nil && got.PD == nil:
			t.Fatalf("pd none, want %+v", *exp.PD)
		case exp.PD != nil && *got.PD != *exp.PD:
			t.Fatalf("pd %+v, want %+v", *got.PD, *exp.PD)
		}
	})
}
