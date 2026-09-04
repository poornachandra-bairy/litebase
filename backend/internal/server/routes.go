package server

import (
	"net/http"

	"github.com/litebase/litebase/internal/auth"
	"github.com/litebase/litebase/internal/middleware"
)

// AdminPrefix is where the dashboard's own API lives. Generated endpoints may
// not claim a path underneath it, which is enforced in api.ValidatePath.
const AdminPrefix = "/api/admin"

// routes builds the complete route table.
//
// Three families of routes are served:
//
//   - /api/admin/...  the dashboard API, authenticated by session cookie
//   - /api/...        endpoints defined in the API Builder, authenticated by
//     API key
//   - /...            the compiled frontend
//
// Ordering matters: the admin prefix is registered explicitly so it always wins
// over the catch-all that dispatches to generated endpoints.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// Health is unauthenticated so a load balancer or "docker compose ps" can
	// reach it, and it deliberately reveals nothing beyond liveness.
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/ready", s.handleReady)

	// Session endpoints are rate limited far more tightly than the rest of the
	// dashboard, since they are the credential-guessing surface.
	authLimited := func(h http.HandlerFunc) http.Handler {
		return middleware.Chain(h,
			middleware.BodyLimit(s.cfg.MaxBodyBytes),
			middleware.RateLimit(s.authLimiter, middleware.IPIdentity(s.cfg.TrustProxy)),
		)
	}
	mux.Handle("POST /api/admin/auth/login", authLimited(s.handleLogin))
	mux.Handle("POST /api/admin/auth/logout", authLimited(s.handleLogout))

	// Everything else under the admin prefix requires a session.
	admin := http.NewServeMux()
	s.registerAdminRoutes(admin)

	adminHandler := middleware.Chain(admin,
		middleware.BodyLimit(s.cfg.MaxUploadBytes),
		middleware.RateLimit(s.adminLimiter, middleware.IPIdentity(s.cfg.TrustProxy)),
		middleware.RequireSession(s.auth),
		middleware.CSRF(),
	)
	mux.Handle("/api/admin/", adminHandler)

	// Generated endpoints. The API key middleware runs inside the handler so
	// that a public endpoint can skip authentication.
	dataHandler := middleware.Chain(
		http.HandlerFunc(s.handleGeneratedAPI),
		middleware.BodyLimit(s.cfg.MaxBodyBytes),
	)
	mux.Handle("/api/", dataHandler)

	// The frontend, served from the embedded bundle in production.
	mux.Handle("/", s.staticHandler())
	return mux
}

// registerAdminRoutes wires the dashboard API.
//
// Each route declares the permission it needs, so authorisation is visible
// alongside the route rather than buried in the handler.
func (s *Server) registerAdminRoutes(mux *http.ServeMux) {
	type route struct {
		pattern string
		perm    auth.Permission
		handler http.HandlerFunc
	}

	routes := []route{
		// Session and profile.
		{"GET /api/admin/auth/me", "", s.handleMe},
		{"POST /api/admin/auth/change-password", "", s.handleChangePassword},

		// Databases.
		{"GET /api/admin/databases", auth.PermDatabaseRead, s.handleListDatabases},
		{"POST /api/admin/databases", auth.PermDatabaseWrite, s.handleCreateDatabase},
		{"GET /api/admin/databases/{db}", auth.PermDatabaseRead, s.handleGetDatabase},
		{"PATCH /api/admin/databases/{db}", auth.PermDatabaseWrite, s.handleUpdateDatabase},
		{"DELETE /api/admin/databases/{db}", auth.PermDatabaseWrite, s.handleDeleteDatabase},
		{"POST /api/admin/databases/{db}/rename", auth.PermDatabaseWrite, s.handleRenameDatabase},
		{"POST /api/admin/databases/import", auth.PermDatabaseWrite, s.handleImportDatabase},
		{"GET /api/admin/databases/{db}/export", auth.PermDatabaseRead, s.handleExportDatabase},
		{"GET /api/admin/databases/{db}/stats", auth.PermDatabaseRead, s.handleDatabaseStats},
		{"POST /api/admin/databases/{db}/integrity-check", auth.PermDatabaseRead, s.handleIntegrityCheck},
		{"POST /api/admin/databases/{db}/vacuum", auth.PermSchemaWrite, s.handleVacuum},

		// Schema.
		{"GET /api/admin/databases/{db}/schema", auth.PermDatabaseRead, s.handleGetSchema},
		{"GET /api/admin/databases/{db}/tables/{table}", auth.PermDatabaseRead, s.handleGetTable},
		{"POST /api/admin/databases/{db}/tables", auth.PermSchemaWrite, s.handleCreateTable},
		{"DELETE /api/admin/databases/{db}/tables/{table}", auth.PermSchemaWrite, s.handleDropTable},
		{"POST /api/admin/databases/{db}/tables/{table}/rename", auth.PermSchemaWrite, s.handleRenameTable},
		{"POST /api/admin/databases/{db}/tables/{table}/columns", auth.PermSchemaWrite, s.handleAddColumn},
		{"PATCH /api/admin/databases/{db}/tables/{table}/columns/{column}", auth.PermSchemaWrite, s.handleModifyColumn},
		{"POST /api/admin/databases/{db}/tables/{table}/columns/{column}/rename", auth.PermSchemaWrite, s.handleRenameColumn},
		{"DELETE /api/admin/databases/{db}/tables/{table}/columns/{column}", auth.PermSchemaWrite, s.handleDropColumn},
		{"POST /api/admin/databases/{db}/indexes", auth.PermSchemaWrite, s.handleCreateIndex},
		{"DELETE /api/admin/databases/{db}/indexes/{index}", auth.PermSchemaWrite, s.handleDropIndex},
		{"DELETE /api/admin/databases/{db}/views/{view}", auth.PermSchemaWrite, s.handleDropView},
		{"DELETE /api/admin/databases/{db}/triggers/{trigger}", auth.PermSchemaWrite, s.handleDropTrigger},

		// Rows.
		{"POST /api/admin/databases/{db}/tables/{table}/rows/query", auth.PermRowsRead, s.handleListRows},
		{"POST /api/admin/databases/{db}/tables/{table}/rows", auth.PermRowsWrite, s.handleCreateRow},
		{"POST /api/admin/databases/{db}/tables/{table}/rows/get", auth.PermRowsRead, s.handleGetRow},
		{"POST /api/admin/databases/{db}/tables/{table}/rows/update", auth.PermRowsWrite, s.handleUpdateRow},
		{"POST /api/admin/databases/{db}/tables/{table}/rows/delete", auth.PermRowsWrite, s.handleDeleteRows},
		{"GET /api/admin/databases/{db}/tables/{table}/export", auth.PermRowsRead, s.handleExportRows},
		{"POST /api/admin/databases/{db}/tables/{table}/import", auth.PermRowsWrite, s.handleImportRows},

		// SQL editor.
		{"POST /api/admin/databases/{db}/query", auth.PermSQLRead, s.handleExecuteSQL},

		// API builder.
		{"GET /api/admin/endpoints", auth.PermAPIRead, s.handleListEndpoints},
		{"POST /api/admin/endpoints", auth.PermAPIWrite, s.handleCreateEndpoint},
		{"GET /api/admin/endpoints/{id}", auth.PermAPIRead, s.handleGetEndpoint},
		{"PUT /api/admin/endpoints/{id}", auth.PermAPIWrite, s.handleUpdateEndpoint},
		{"DELETE /api/admin/endpoints/{id}", auth.PermAPIWrite, s.handleDeleteEndpoint},
		{"POST /api/admin/endpoints/{id}/enabled", auth.PermAPIWrite, s.handleToggleEndpoint},
		{"POST /api/admin/endpoints/test", auth.PermAPIWrite, s.handleTestEndpoint},
		{"GET /api/admin/openapi.json", auth.PermAPIRead, s.handleOpenAPI},

		// API keys.
		{"GET /api/admin/keys", auth.PermKeysRead, s.handleListKeys},
		{"POST /api/admin/keys", auth.PermKeysWrite, s.handleCreateKey},
		{"PUT /api/admin/keys/{id}", auth.PermKeysWrite, s.handleUpdateKey},
		{"DELETE /api/admin/keys/{id}", auth.PermKeysWrite, s.handleDeleteKey},

		// Backups.
		{"GET /api/admin/backups", auth.PermBackupRead, s.handleListBackups},
		{"POST /api/admin/backups", auth.PermBackupWrite, s.handleCreateBackup},
		{"GET /api/admin/backups/{id}/download", auth.PermBackupWrite, s.handleDownloadBackup},
		{"POST /api/admin/backups/{id}/restore", auth.PermBackupWrite, s.handleRestoreBackup},
		{"DELETE /api/admin/backups/{id}", auth.PermBackupWrite, s.handleDeleteBackup},
		{"GET /api/admin/backup-schedules", auth.PermBackupRead, s.handleListSchedules},
		{"POST /api/admin/backup-schedules", auth.PermBackupWrite, s.handleCreateSchedule},
		{"PUT /api/admin/backup-schedules/{id}", auth.PermBackupWrite, s.handleUpdateSchedule},
		{"DELETE /api/admin/backup-schedules/{id}", auth.PermBackupWrite, s.handleDeleteSchedule},
		{"POST /api/admin/backup-schedules/{id}/run", auth.PermBackupWrite, s.handleRunSchedule},

		// Settings and users.
		{"GET /api/admin/settings", auth.PermSettingsRead, s.handleGetSettings},
		{"PUT /api/admin/settings/storage/gdrive", auth.PermSettingsWrite, s.handleSetGoogleDrive},
		{"POST /api/admin/settings/storage/gdrive/test", auth.PermSettingsWrite, s.handleTestGoogleDrive},
		{"DELETE /api/admin/settings/storage/gdrive", auth.PermSettingsWrite, s.handleDeleteGoogleDrive},
		{"GET /api/admin/users", auth.PermUsersRead, s.handleListUsers},
		{"POST /api/admin/users", auth.PermUsersWrite, s.handleCreateUser},
		{"PUT /api/admin/users/{id}", auth.PermUsersWrite, s.handleUpdateUser},
		{"DELETE /api/admin/users/{id}", auth.PermUsersWrite, s.handleDeleteUser},
	}

	for _, r := range routes {
		handler := http.Handler(r.handler)
		if r.perm != "" {
			handler = middleware.RequirePermission(r.perm)(handler)
		}
		mux.Handle(r.pattern, handler)
	}
}
