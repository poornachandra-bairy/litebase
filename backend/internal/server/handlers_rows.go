package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/litebase/litebase/internal/auth"
	"github.com/litebase/litebase/internal/database"
	"github.com/litebase/litebase/internal/httpx"
	"github.com/litebase/litebase/internal/middleware"
	"github.com/litebase/litebase/internal/rows"
)

// Row browsing uses POST with a JSON body rather than GET with query
// parameters, because filters are structured and can be numerous; encoding them
// into a URL would be lossy and would risk exceeding URL length limits.

func (s *Server) handleListRows(w http.ResponseWriter, r *http.Request) {
	var opts rows.ListOptions
	if err := httpx.DecodeJSON(w, r, &opts); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	page, err := s.rows.List(r.Context(), r.PathValue("db"), r.PathValue("table"), opts)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

type rowKeyRequest struct {
	Key map[string]any `json:"key"`
}

type rowWriteRequest struct {
	Key    map[string]any `json:"key"`
	Values map[string]any `json:"values"`
}

func (s *Server) handleGetRow(w http.ResponseWriter, r *http.Request) {
	var req rowKeyRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	row, err := s.rows.Get(r.Context(), r.PathValue("db"), r.PathValue("table"), req.Key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"row": row})
}

func (s *Server) handleCreateRow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Values map[string]any `json:"values"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	row, err := s.rows.Insert(r.Context(), r.PathValue("db"), r.PathValue("table"), req.Values)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"row": row})
}

func (s *Server) handleUpdateRow(w http.ResponseWriter, r *http.Request) {
	var req rowWriteRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	row, err := s.rows.Update(r.Context(), r.PathValue("db"), r.PathValue("table"), req.Key, req.Values)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"row": row})
}

func (s *Server) handleDeleteRows(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(req.Keys) == 0 {
		httpx.Fail(w, r, httpx.BadRequest("no rows were selected"))
		return
	}
	deleted, err := s.rows.DeleteMany(r.Context(), r.PathValue("db"), r.PathValue("table"), req.Keys)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

// handleExecuteSQL runs ad-hoc SQL from the editor.
func (s *Server) handleExecuteSQL(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFrom(r.Context())
	if user == nil {
		httpx.Fail(w, r, httpx.Unauthorized(""))
		return
	}

	var req struct {
		SQL     string `json:"sql"`
		MaxRows int    `json:"max_rows"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	// A user without sql:write runs on a read-only connection, so SQLite itself
	// refuses any write. That is stronger than inspecting the statement text,
	// which could be fooled by a trigger or an unusual construction.
	readOnly := !user.Can(auth.PermSQLWrite)

	resp, err := s.query.Execute(r.Context(), r.PathValue("db"), req.SQL, queryOptions(req.MaxRows, readOnly))
	if err != nil {
		fail(w, r, err)
		return
	}
	if !readOnly {
		s.log.Info("sql executed", "user_id", user.ID, "database", r.PathValue("db"),
			"statements", len(resp.Results), "failed", resp.Failed)
	}
	httpx.JSON(w, http.StatusOK, resp)
}

// handleExportRows streams a table as CSV or JSON.
func (s *Server) handleExportRows(w http.ResponseWriter, r *http.Request) {
	db, table := r.PathValue("db"), r.PathValue("table")
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "json" {
		httpx.Fail(w, r, httpx.BadRequest("format must be csv or json"))
		return
	}

	limit := queryInt(r, "limit", 100000)
	if limit <= 0 || limit > 1000000 {
		limit = 100000
	}

	// Export reads in pages so a large table does not have to fit in memory at
	// once, and the response streams to the client as it goes.
	ctx, cancel := contextWithTimeout(r, 10*time.Minute)
	defer cancel()

	filename := fmt.Sprintf("%s-%s.%s", table, time.Now().UTC().Format("20060102-150405"), format)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		s.streamCSV(ctx, w, r, db, table, limit)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	s.streamJSON(ctx, w, r, db, table, limit)
}

const exportPageSize = 1000

func (s *Server) streamCSV(ctx context.Context, w http.ResponseWriter, r *http.Request, db, table string, limit int) {
	writer := csv.NewWriter(w)
	defer writer.Flush()

	var headerWritten bool
	var columns []string
	exported := 0

	for offset := 0; exported < limit; offset += exportPageSize {
		pageSize := min(exportPageSize, limit-exported)
		page, err := s.rows.List(ctx, db, table, rows.ListOptions{Limit: pageSize, Offset: offset})
		if err != nil {
			if !headerWritten {
				fail(w, r, err)
			} else {
				s.log.Error("export failed mid-stream", "table", table, "error", err)
			}
			return
		}
		if len(page.Rows) == 0 {
			return
		}
		if !headerWritten {
			columns = page.Columns
			if err := writer.Write(columns); err != nil {
				return
			}
			headerWritten = true
		}

		for _, row := range page.Rows {
			record := make([]string, len(columns))
			for i, col := range columns {
				record[i] = csvValue(row[col])
			}
			if err := writer.Write(record); err != nil {
				return
			}
		}
		writer.Flush()
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

		exported += len(page.Rows)
		if !page.HasMore {
			return
		}
	}
}

func (s *Server) streamJSON(ctx context.Context, w http.ResponseWriter, r *http.Request, db, table string, limit int) {
	enc := json.NewEncoder(w)
	fmt.Fprint(w, "[")
	first := true
	exported := 0

	for offset := 0; exported < limit; offset += exportPageSize {
		pageSize := min(exportPageSize, limit-exported)
		page, err := s.rows.List(ctx, db, table, rows.ListOptions{Limit: pageSize, Offset: offset})
		if err != nil {
			s.log.Error("json export failed", "table", table, "error", err)
			break
		}
		if len(page.Rows) == 0 {
			break
		}
		for _, row := range page.Rows {
			if !first {
				fmt.Fprint(w, ",")
			}
			first = false
			if err := enc.Encode(row); err != nil {
				break
			}
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		exported += len(page.Rows)
		if !page.HasMore {
			break
		}
	}
	fmt.Fprint(w, "]")
}

// csvValue renders a value for CSV output.
func csvValue(v any) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return val
	case int64:
		return strconv.FormatInt(val, 10)
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(val)
	case database.BlobValue:
		// Binary has no faithful CSV representation; the base64 form keeps the
		// export lossless.
		return val.Base64
	default:
		return fmt.Sprintf("%v", val)
	}
}
