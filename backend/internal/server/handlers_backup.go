package server

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/litebase/litebase/internal/backup"
	"github.com/litebase/litebase/internal/httpx"
	"github.com/litebase/litebase/internal/middleware"
)

func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	list, err := s.backups.List(r.Context(), backup.ListOptions{
		Database: r.URL.Query().Get("database"),
		Limit:    queryInt(r, "limit", 100),
		Offset:   queryInt(r, "offset", 0),
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"backups":              list,
		"providers":            s.backups.Providers(),
		"encryption_available": s.backups.EncryptionAvailable(),
	})
}

func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Database string `json:"database"`
		Provider string `json:"provider"`
		Encrypt  bool   `json:"encrypt"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Database == "" {
		httpx.Fail(w, r, httpx.BadRequest("a database name is required"))
		return
	}

	// A backup of a large database can take a while; give it a deadline of its
	// own rather than the default handler timeout.
	ctx, cancel := contextWithTimeout(r, 30*time.Minute)
	defer cancel()

	rec, err := s.backups.Create(ctx, backup.CreateOptions{
		Database: req.Database,
		Provider: req.Provider,
		Encrypt:  req.Encrypt,
		Trigger:  backup.TriggerManual,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, rec)
}

// handleDownloadBackup streams a backup to the operator.
func (s *Server) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	// Encrypted backups are decrypted on the way out by default so the operator
	// receives a usable database file; ?raw=true keeps the encrypted form for
	// archival.
	decrypt := !queryBool(r, "raw", false)

	reader, rec, err := s.backups.Open(r.Context(), r.PathValue("id"), decrypt)
	if err != nil {
		fail(w, r, err)
		return
	}
	defer reader.Close()

	filename := rec.Filename
	if rec.Encrypted && decrypt {
		// The download is plaintext, so the ".enc" suffix would be misleading.
		filename = trimSuffix(filename, ".enc")
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+sanitizeFilename(filename)+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// The length is unknown when decrypting on the fly, so it is only sent for
	// a raw download.
	if !decrypt || !rec.Encrypted {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", rec.SizeBytes))
	}

	if _, err := io.Copy(w, reader); err != nil {
		s.log.Warn("backup download interrupted", "backup_id", rec.ID, "error", err)
	}
}

func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec, err := s.backups.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	// Restoring overwrites a live database, so the operator must name the
	// target explicitly rather than clicking one button.
	if r.URL.Query().Get("confirm") != rec.DatabaseName {
		httpx.Fail(w, r, httpx.BadRequest(
			"to restore, repeat the database name in the 'confirm' query parameter"))
		return
	}

	ctx, cancel := contextWithTimeout(r, 30*time.Minute)
	defer cancel()

	if err := s.backups.Restore(ctx, id); err != nil {
		fail(w, r, err)
		return
	}
	s.log.Warn("database restored from backup",
		"database", rec.DatabaseName, "backup_id", id,
		"by", userID(middleware.UserFrom(r.Context())))
	httpx.JSON(w, http.StatusOK, map[string]any{
		"status":   "restored",
		"database": rec.DatabaseName,
	})
}

func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	if err := s.backups.Delete(r.Context(), r.PathValue("id")); err != nil {
		fail(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	list, err := s.backups.ListSchedules(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"schedules": list})
}

func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	var sch backup.Schedule
	if err := httpx.DecodeJSON(w, r, &sch); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	sch.ID = ""
	if err := s.backups.CreateSchedule(r.Context(), &sch); err != nil {
		httpx.Fail(w, r, httpx.BadRequest("%s", err.Error()))
		return
	}
	httpx.JSON(w, http.StatusCreated, sch)
}

func (s *Server) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	var sch backup.Schedule
	if err := httpx.DecodeJSON(w, r, &sch); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	sch.ID = r.PathValue("id")
	if err := s.backups.UpdateSchedule(r.Context(), &sch); err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, sch)
}

func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	if err := s.backups.DeleteSchedule(r.Context(), r.PathValue("id")); err != nil {
		fail(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// handleRunSchedule triggers a schedule immediately.
func (s *Server) handleRunSchedule(w http.ResponseWriter, r *http.Request) {
	sch, err := s.backups.GetSchedule(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, r, err)
		return
	}

	ctx, cancel := contextWithTimeout(r, 30*time.Minute)
	defer cancel()

	if err := s.backups.RunSchedule(ctx, sch); err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"status": "completed"})
}

func trimSuffix(s, suffix string) string {
	if len(s) > len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)]
	}
	return s
}

// sanitizeFilename strips characters that would let a filename break out of the
// Content-Disposition header.
func sanitizeFilename(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch r {
		case '"', '\\', '\n', '\r', 0:
			continue
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return "backup.db"
	}
	return string(out)
}
