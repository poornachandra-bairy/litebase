package server

import (
	"net/http"
	"time"

	"github.com/litebase/litebase/internal/httpx"
	"github.com/litebase/litebase/internal/tables"
)

func (s *Server) handleGetSchema(w http.ResponseWriter, r *http.Request) {
	schema, err := s.tables.GetSchema(r.Context(), r.PathValue("db"))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, schema)
}

func (s *Server) handleGetTable(w http.ResponseWriter, r *http.Request) {
	table, err := s.tables.GetTable(r.Context(), r.PathValue("db"), r.PathValue("table"))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, table)
}

func (s *Server) handleCreateTable(w http.ResponseWriter, r *http.Request) {
	var in tables.CreateTableInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	db := r.PathValue("db")
	if err := s.tables.CreateTable(r.Context(), db, in); err != nil {
		fail(w, r, err)
		return
	}
	s.log.Info("table created", "database", db, "table", in.Name)

	table, err := s.tables.GetTable(r.Context(), db, in.Name)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, table)
}

func (s *Server) handleDropTable(w http.ResponseWriter, r *http.Request) {
	db, table := r.PathValue("db"), r.PathValue("table")
	// Dropping a table destroys its rows, so the name must be repeated.
	if r.URL.Query().Get("confirm") != table {
		httpx.Fail(w, r, httpx.BadRequest(
			"to drop this table, repeat its name in the 'confirm' query parameter"))
		return
	}
	if err := s.tables.DropTable(r.Context(), db, table); err != nil {
		fail(w, r, err)
		return
	}
	s.log.Warn("table dropped", "database", db, "table", table)
	httpx.NoContent(w)
}

func (s *Server) handleRenameTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := s.tables.RenameTable(r.Context(), r.PathValue("db"), r.PathValue("table"), req.Name); err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"status": "renamed", "name": req.Name})
}

func (s *Server) handleAddColumn(w http.ResponseWriter, r *http.Request) {
	var col tables.Column
	if err := httpx.DecodeJSON(w, r, &col); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	db, table := r.PathValue("db"), r.PathValue("table")
	if err := s.tables.AddColumn(r.Context(), db, table, col); err != nil {
		fail(w, r, err)
		return
	}
	s.respondWithTable(w, r, db, table, http.StatusCreated)
}

func (s *Server) handleModifyColumn(w http.ResponseWriter, r *http.Request) {
	var col tables.Column
	if err := httpx.DecodeJSON(w, r, &col); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	db, table, column := r.PathValue("db"), r.PathValue("table"), r.PathValue("column")

	// Modifying a column rebuilds the table, which can take a while on a large
	// one, so it gets its own deadline.
	ctx, cancel := contextWithTimeout(r, 10*time.Minute)
	defer cancel()

	if err := s.tables.ModifyColumn(ctx, db, table, column, col); err != nil {
		fail(w, r, err)
		return
	}
	s.log.Info("column modified", "database", db, "table", table, "column", column)
	s.respondWithTable(w, r, db, table, http.StatusOK)
}

func (s *Server) handleRenameColumn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	db, table := r.PathValue("db"), r.PathValue("table")
	if err := s.tables.RenameColumn(r.Context(), db, table, r.PathValue("column"), req.Name); err != nil {
		fail(w, r, err)
		return
	}
	s.respondWithTable(w, r, db, table, http.StatusOK)
}

func (s *Server) handleDropColumn(w http.ResponseWriter, r *http.Request) {
	db, table, column := r.PathValue("db"), r.PathValue("table"), r.PathValue("column")
	if r.URL.Query().Get("confirm") != column {
		httpx.Fail(w, r, httpx.BadRequest(
			"to drop this column, repeat its name in the 'confirm' query parameter"))
		return
	}
	if err := s.tables.DropColumn(r.Context(), db, table, column); err != nil {
		fail(w, r, err)
		return
	}
	s.log.Warn("column dropped", "database", db, "table", table, "column", column)
	s.respondWithTable(w, r, db, table, http.StatusOK)
}

func (s *Server) handleCreateIndex(w http.ResponseWriter, r *http.Request) {
	var in tables.CreateIndexInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := s.tables.CreateIndex(r.Context(), r.PathValue("db"), in); err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"status": "created", "name": in.Name})
}

func (s *Server) handleDropIndex(w http.ResponseWriter, r *http.Request) {
	if err := s.tables.DropIndex(r.Context(), r.PathValue("db"), r.PathValue("index")); err != nil {
		fail(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func (s *Server) handleDropView(w http.ResponseWriter, r *http.Request) {
	if err := s.tables.DropView(r.Context(), r.PathValue("db"), r.PathValue("view")); err != nil {
		fail(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func (s *Server) handleDropTrigger(w http.ResponseWriter, r *http.Request) {
	if err := s.tables.DropTrigger(r.Context(), r.PathValue("db"), r.PathValue("trigger")); err != nil {
		fail(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// respondWithTable returns the table's current definition after a change, so
// the dashboard can refresh without a second request.
func (s *Server) respondWithTable(w http.ResponseWriter, r *http.Request, db, table string, status int) {
	t, err := s.tables.GetTable(r.Context(), db, table)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, status, t)
}
