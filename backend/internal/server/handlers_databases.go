package server

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/litebase/litebase/internal/database"
	"github.com/litebase/litebase/internal/httpx"
)

func (s *Server) handleListDatabases(w http.ResponseWriter, r *http.Request) {
	list, err := s.mgr.List(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"databases": list})
}

type createDatabaseRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *Server) handleCreateDatabase(w http.ResponseWriter, r *http.Request) {
	var req createDatabaseRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	meta, err := s.mgr.Create(r.Context(), req.Name, req.Description)
	if err != nil {
		fail(w, r, err)
		return
	}
	s.log.Info("database created", "database", meta.Name)
	httpx.JSON(w, http.StatusCreated, meta)
}

func (s *Server) handleGetDatabase(w http.ResponseWriter, r *http.Request) {
	meta, err := s.mgr.Find(r.Context(), r.PathValue("db"))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, meta)
}

func (s *Server) handleUpdateDatabase(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Description string `json:"description"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := s.mgr.SetDescription(r.Context(), r.PathValue("db"), req.Description); err != nil {
		fail(w, r, err)
		return
	}
	meta, err := s.mgr.Find(r.Context(), r.PathValue("db"))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, meta)
}

func (s *Server) handleRenameDatabase(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	oldName := r.PathValue("db")
	if err := s.mgr.Rename(r.Context(), oldName, req.Name); err != nil {
		fail(w, r, err)
		return
	}
	s.log.Info("database renamed", "from", oldName, "to", req.Name)
	meta, err := s.mgr.Find(r.Context(), req.Name)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, meta)
}

func (s *Server) handleDeleteDatabase(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("db")
	// Deleting a database destroys data irreversibly, so the caller must name
	// it again in the request. That turns a mis-click into a no-op.
	if r.URL.Query().Get("confirm") != name {
		httpx.Fail(w, r, httpx.BadRequest(
			"to delete this database, repeat its name in the 'confirm' query parameter"))
		return
	}
	if err := s.mgr.Delete(r.Context(), name); err != nil {
		fail(w, r, err)
		return
	}
	s.log.Warn("database deleted", "database", name)
	httpx.NoContent(w)
}

// handleImportDatabase accepts a SQLite file upload and registers it.
func (s *Server) handleImportDatabase(w http.ResponseWriter, r *http.Request) {
	// The multipart parser keeps small parts in memory and spills the rest to
	// disk; the overall size is already capped by the body limit middleware.
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		httpx.Fail(w, r, httpx.BadRequest("expected a multipart upload with a 'file' field"))
		return
	}
	defer r.MultipartForm.RemoveAll()

	name := r.FormValue("name")
	if name == "" {
		httpx.Fail(w, r, httpx.BadRequest("a database name is required"))
		return
	}
	// Validate before touching the filesystem so a bad name never creates a
	// temporary file.
	if err := database.ValidateIdentifier(name); err != nil {
		fail(w, r, err)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		httpx.Fail(w, r, httpx.BadRequest("a 'file' upload is required"))
		return
	}
	defer file.Close()

	if header.Size > s.cfg.MaxUploadBytes {
		httpx.Fail(w, r, httpx.TooLarge("the uploaded file is larger than the configured limit"))
		return
	}

	// The upload is staged in our own temp directory. The client's filename is
	// never used as a path, so a crafted name cannot escape anywhere.
	staged := filepath.Join(s.cfg.TempDir(), "import-"+database.NewID()+".db")
	out, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		httpx.Fail(w, r, httpx.Internal(err))
		return
	}
	_, copyErr := io.Copy(out, io.LimitReader(file, s.cfg.MaxUploadBytes))
	closeErr := out.Close()
	defer os.Remove(staged)

	if copyErr != nil || closeErr != nil {
		httpx.Fail(w, r, httpx.Internal(fmt.Errorf("stage upload: %v %v", copyErr, closeErr)))
		return
	}

	meta, err := s.mgr.ImportFile(r.Context(), name, r.FormValue("description"), staged)
	if err != nil {
		fail(w, r, err)
		return
	}
	s.log.Info("database imported", "database", meta.Name, "size", header.Size)
	httpx.JSON(w, http.StatusCreated, meta)
}

// handleExportDatabase streams a consistent snapshot of a database.
func (s *Server) handleExportDatabase(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("db")
	if _, err := s.mgr.Find(r.Context(), name); err != nil {
		fail(w, r, err)
		return
	}

	// A snapshot is exported rather than the live file, so the download is a
	// consistent database even while writes continue.
	path, cleanup, err := s.backups.Snapshot(r.Context(), name)
	if err != nil {
		fail(w, r, err)
		return
	}
	defer cleanup()

	f, err := os.Open(path)
	if err != nil {
		httpx.Fail(w, r, httpx.Internal(err))
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		httpx.Fail(w, r, httpx.Internal(err))
		return
	}

	filename := fmt.Sprintf("%s-%s.db", name, time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	// The filename is built from a validated database name, so it cannot carry
	// a quote or newline into the header.
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if _, err := io.Copy(w, f); err != nil {
		s.log.Warn("export stream interrupted", "database", name, "error", err)
	}
}

func (s *Server) handleDatabaseStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.tables.GetStats(r.Context(), r.PathValue("db"))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, stats)
}

func (s *Server) handleIntegrityCheck(w http.ResponseWriter, r *http.Request) {
	// An integrity check reads every page, so it gets a generous deadline of
	// its own rather than the default handler timeout.
	ctx, cancel := contextWithTimeout(r, 5*time.Minute)
	defer cancel()

	report, err := s.tables.CheckIntegrity(ctx, r.PathValue("db"))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, report)
}

// handleVacuum rebuilds a database to reclaim free pages.
func (s *Server) handleVacuum(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 10*time.Minute)
	defer cancel()

	h, err := s.mgr.Get(ctx, r.PathValue("db"))
	if err != nil {
		fail(w, r, err)
		return
	}
	defer h.Release()

	start := time.Now()
	if _, err := h.Writer().ExecContext(ctx, "VACUUM"); err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"status":      "vacuumed",
		"duration_ms": time.Since(start).Milliseconds(),
	})
}
