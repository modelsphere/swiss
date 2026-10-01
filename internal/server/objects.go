package server

import (
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/modelsphere/swiss/internal/cluster"
)

type releaseObjects struct {
	Objects []objectResult `json:"objects"`
}

// objectResult is one object the manifest names. Exactly one of Live, Missing
// and Error says what reading it found.
type objectResult struct {
	Ref     cluster.ObjectRef `json:"ref"`
	Live    *cluster.Object   `json:"live,omitempty"`
	Missing bool              `json:"missing,omitempty"`
	Error   string            `json:"error,omitempty"`
}

// handleObjects reports the ModelRoute, LLMScaler and LLMSLORequirement a
// release rendered, read live: what their controllers last observed is the
// half of "is this model serving" that helm and the pods cannot say.
//
// Separate from /status, which two pages poll and which must stay cheap when a
// CRD is not installed or not granted. A failure here is per object.
func (s *Server) handleObjects(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	rel, err := s.releaseRecord(ctx, r.PathValue("namespace"), r.PathValue("release"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	out := releaseObjects{Objects: []objectResult{}}
	if rel == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	for _, ref := range rel.Objects {
		res := objectResult{Ref: ref}
		o, err := s.probe.Object(ctx, ref)
		switch {
		case apierrors.IsForbidden(err):
			res.Error = "swissd may not read " + ref.Kind + " in " + ref.Namespace + ": upgrade the swiss chart, which grants it"
		case err != nil:
			res.Error = err.Error()
		case o == nil:
			res.Missing = true
		default:
			res.Live = o
		}
		out.Objects = append(out.Objects, res)
	}
	writeJSON(w, http.StatusOK, out)
}
