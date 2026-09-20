package server

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// SetWeb installs the SPA. Nil disables it and leaves swissd API-only, which is
// what the CLI-only and test paths use.
func (s *Server) SetWeb(f fs.FS) { s.web = f }

// WebFromDir serves the UI from disk instead of the embedded copy, so the
// frontend can be iterated on without rebuilding the binary.
func WebFromDir(dir string) (fs.FS, error) {
	f := os.DirFS(dir)
	if _, err := fs.Stat(f, "index.html"); err != nil {
		return nil, err
	}
	return f, nil
}

// spa serves built assets, and index.html for every other path so that client
// routing works on a hard refresh.
//
// Anything under /api/ is deliberately excluded from that fallback and must 404
// as JSON: handing an HTML page to a fetch() produces a parse error at the call
// site and hides which request actually failed.
//
// What counts as "an asset" is decided by location, not by file extension.
// Extensions look like the obvious test and are wrong here -- model names carry
// dots (glm-5.3, kimi-k2.5, qwen3.6-35b-a3b), so path.Ext("/catalog/glm-5.3")
// is ".3" and every such route would 404 instead of loading the app. Vite emits
// hashed files under assets/, so that prefix is the honest signal, and a miss
// there is a stale reference rather than a client route.
func (s *Server) spa(w http.ResponseWriter, r *http.Request) {
	if s.web == nil {
		writeError(w, http.StatusNotFound, "no web UI in this build -- run `npm run build` in web/, or start swissd with -web-dir")
		return
	}
	upath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")

	if strings.HasPrefix(upath, "api/") {
		writeError(w, http.StatusNotFound, "no such endpoint")
		return
	}
	if upath == "" {
		s.serveIndex(w, r)
		return
	}

	if strings.HasPrefix(upath, "assets/") {
		if !exists(s.web, upath) {
			writeError(w, http.StatusNotFound, "no such file")
			return
		}
		// Vite fingerprints these names, so they are safe to cache hard; the
		// entry point below never is.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeFileFS(w, r, s.web, upath)
		return
	}

	// A real file at the root (favicon.ico, robots.txt) wins; everything else
	// is a client route.
	if exists(s.web, upath) {
		http.ServeFileFS(w, r, s.web, upath)
		return
	}
	s.serveIndex(w, r)
}

func exists(f fs.FS, name string) bool {
	st, err := fs.Stat(f, name)
	return err == nil && !st.IsDir()
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	// index.html names the fingerprinted bundles, so a cached copy after an
	// upgrade points at assets that no longer exist.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, s.web, "index.html")
}
