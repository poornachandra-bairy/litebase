package server

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/litebase/litebase/internal/httpx"
	"github.com/litebase/litebase/internal/query"
)

// The compiled React bundle is served by the Go binary in production, which is
// what makes single-binary deployment possible: there is no separate web server
// to configure.

// staticHandler serves the frontend, falling back to index.html so that client
// side routes such as /databases/app work on a page reload.
func (s *Server) staticHandler() http.Handler {
	if s.staticFS == nil {
		return http.HandlerFunc(s.handleNoFrontend)
	}

	fileServer := http.FileServer(http.FS(s.staticFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			httpx.Fail(w, r, httpx.NotFound("not found"))
			return
		}

		// Clean the path before touching the filesystem. http.FS also rejects
		// traversal, but normalising here keeps the fallback logic honest.
		upath := path.Clean("/" + r.URL.Path)
		name := strings.TrimPrefix(upath, "/")
		if name == "" {
			name = "index.html"
		}

		f, err := s.staticFS.Open(name)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				httpx.Fail(w, r, httpx.Internal(err))
				return
			}
			// An unknown path that is not an asset request belongs to the
			// single-page app's router.
			s.serveIndex(w, r)
			return
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil || info.IsDir() {
			s.serveIndex(w, r)
			return
		}

		// Vite emits content-hashed asset filenames, so those can be cached
		// indefinitely. index.html must never be cached, or a deploy would not
		// reach browsers.
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	f, err := s.staticFS.Open("index.html")
	if err != nil {
		s.handleNoFrontend(w, r)
		return
	}
	defer f.Close()

	content, err := io.ReadAll(f)
	if err != nil {
		httpx.Fail(w, r, httpx.Internal(err))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", time.Time{}, strings.NewReader(string(content)))
}

// handleNoFrontend explains what to do when the binary was built without the
// dashboard bundle, which is the normal state during backend development.
func (s *Server) handleNoFrontend(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		httpx.Fail(w, r, httpx.NotFound("no endpoint is defined for this path"))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Litebase</title>
<style>
 body{font:15px/1.6 ui-sans-serif,system-ui,-apple-system,sans-serif;margin:0;
      display:grid;place-items:center;min-height:100vh;background:#0b0e14;color:#c9d1d9}
 main{max-width:34rem;padding:2rem}
 h1{font-size:1.3rem;margin:0 0 .5rem}
 code{background:#161b22;padding:.15rem .4rem;border-radius:4px;font-size:.9em}
 a{color:#58a6ff}
</style></head>
<body><main>
<h1>Litebase is running</h1>
<p>The API is available, but the dashboard bundle was not compiled into this binary.</p>
<p>To build it:</p>
<p><code>cd frontend &amp;&amp; npm install &amp;&amp; npm run build</code>, then rebuild the server.</p>
<p>During development run <code>npm run dev</code> and use the Vite server, which proxies the API.</p>
<p>Health check: <a href="/api/health">/api/health</a></p>
</main></body></html>`)
}

// osFS adapts a directory on disk to fs.FS, used when the frontend is served
// from LITEBASE_STATIC_DIR rather than the embedded bundle.
func osFS(dir string) (fs.FS, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("static dir is not a directory")
	}
	return os.DirFS(dir), nil
}

// queryOptions builds the SQL editor's execution options.
func queryOptions(maxRows int, readOnly bool) query.ExecOptions {
	return query.ExecOptions{MaxRows: maxRows, ReadOnly: readOnly}
}
