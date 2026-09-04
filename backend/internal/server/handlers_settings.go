package server

import (
	"net/http"
	"time"

	"github.com/litebase/litebase/internal/httpx"
	"github.com/litebase/litebase/internal/storage"
	"github.com/litebase/litebase/internal/tables"
)

func nowUTC() time.Time { return time.Now().UTC() }

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	var gdrive storage.GoogleDriveConfig
	configured, err := s.settings.getJSON(r.Context(), gdriveSettingKey, &gdrive)
	if err != nil {
		// A setting that cannot be decrypted must not block the whole page.
		s.log.Warn("read google drive settings", "error", err)
	}

	httpx.JSON(w, http.StatusOK, map[string]any{
		"version":              Version,
		"data_dir":             s.cfg.DataDir,
		"providers":            s.backups.Providers(),
		"encryption_available": s.backups.EncryptionAvailable(),
		"supported_types":      tables.SupportedTypes(),
		"limits": map[string]any{
			"max_query_rows":   s.cfg.MaxQueryRows,
			"max_body_bytes":   s.cfg.MaxBodyBytes,
			"max_upload_bytes": s.cfg.MaxUploadBytes,
			"query_timeout":    s.cfg.QueryTimeout.String(),
		},
		"google_drive": map[string]any{
			"configured": configured,
			// The client id identifies the connection without being a secret;
			// the client secret and refresh token are never returned.
			"client_id": gdrive.ClientID,
			"folder_id": gdrive.FolderID,
		},
	})
}

func (s *Server) handleSetGoogleDrive(w http.ResponseWriter, r *http.Request) {
	var cfg storage.GoogleDriveConfig
	if err := httpx.DecodeJSON(w, r, &cfg); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := cfg.Validate(); err != nil {
		httpx.Fail(w, r, httpx.BadRequest("%s", err.Error()))
		return
	}

	// The credentials are verified before being stored, so a typo is caught
	// here rather than at the next scheduled backup.
	provider, err := storage.NewGoogleDrive(cfg)
	if err != nil {
		httpx.Fail(w, r, httpx.BadRequest("%s", err.Error()))
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	if err := provider.TestConnection(ctx); err != nil {
		httpx.Fail(w, r, httpx.BadRequest("%s", err.Error()))
		return
	}

	if err := s.settings.setJSON(r.Context(), gdriveSettingKey, cfg, true); err != nil {
		httpx.Fail(w, r, httpx.BadRequest("%s", err.Error()))
		return
	}
	// Register it immediately so backups can use it without a restart.
	s.storageRegistry.Register(provider)
	s.log.Info("google drive storage configured")

	httpx.JSON(w, http.StatusOK, map[string]any{"status": "connected"})
}

func (s *Server) handleTestGoogleDrive(w http.ResponseWriter, r *http.Request) {
	provider, err := s.storageRegistry.Get("gdrive")
	if err != nil {
		httpx.Fail(w, r, httpx.BadRequest("Google Drive is not configured"))
		return
	}
	drive, ok := provider.(*storage.GoogleDrive)
	if !ok {
		httpx.Fail(w, r, httpx.Internal(nil))
		return
	}

	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	if err := drive.TestConnection(ctx); err != nil {
		httpx.Fail(w, r, httpx.BadRequest("%s", err.Error()))
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleDeleteGoogleDrive(w http.ResponseWriter, r *http.Request) {
	if err := s.settings.delete(r.Context(), gdriveSettingKey); err != nil {
		fail(w, r, err)
		return
	}
	s.log.Info("google drive storage disconnected")
	httpx.JSON(w, http.StatusOK, map[string]any{
		"status": "disconnected",
		"note":   "Existing backups stored in Google Drive are not deleted. Restart the server to fully unload the provider.",
	})
}
