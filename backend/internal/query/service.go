package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/litebase/litebase/internal/database"
)

// Service runs ad-hoc SQL for the SQL editor.
type Service struct {
	mgr     *database.Manager
	maxRows int
	timeout time.Duration
}

// NewService builds a query service.
func NewService(mgr *database.Manager, maxRows int, timeout time.Duration) *Service {
	if maxRows <= 0 {
		maxRows = 1000
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Service{mgr: mgr, maxRows: maxRows, timeout: timeout}
}

// Result is the outcome of one statement.
type Result struct {
	Statement    string           `json:"statement"`
	Kind         Kind             `json:"kind"`
	Columns      []string         `json:"columns"`
	Rows         []map[string]any `json:"rows"`
	RowsAffected int64            `json:"rows_affected"`
	LastInsertID int64            `json:"last_insert_id"`
	DurationMS   float64          `json:"duration_ms"`
	Truncated    bool             `json:"truncated"`
	Error        string           `json:"error,omitempty"`
}

// Response is the outcome of an entire script.
type Response struct {
	Results    []Result `json:"results"`
	DurationMS float64  `json:"duration_ms"`
	ReadOnly   bool     `json:"read_only"`
	// Failed reports whether any statement errored, which the dashboard uses
	// to decide how to present the run.
	Failed bool `json:"failed"`
}

// ExecOptions controls one script execution.
type ExecOptions struct {
	// ReadOnly forces execution over a connection SQLite will not let write.
	// The caller sets this from the user's permissions.
	ReadOnly bool
	// MaxRows overrides the service default for this run.
	MaxRows int
	// Params are bound to "?" placeholders. The SQL editor does not use them;
	// the generated API does.
	Params []any
}

// ErrScriptEmpty is returned when there is nothing to execute.
var ErrScriptEmpty = errors.New("no SQL statement to execute")

// Execute runs a script and returns one result per statement.
//
// Statements run sequentially on a single connection so that a script may
// depend on the effects of its earlier statements, and so an explicit
// BEGIN/COMMIT behaves as written.
func (s *Service) Execute(ctx context.Context, dbName, script string, opts ExecOptions) (*Response, error) {
	statements := SplitStatements(script)
	if len(statements) == 0 {
		return nil, ErrScriptEmpty
	}
	if len(statements) > 100 {
		return nil, fmt.Errorf("a script may contain at most 100 statements")
	}

	maxRows := opts.MaxRows
	if maxRows <= 0 || maxRows > s.maxRows {
		maxRows = s.maxRows
	}

	// A deadline bounds a runaway query so one request cannot occupy a
	// connection indefinitely.
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	conn, release, err := s.connection(ctx, dbName, opts.ReadOnly)
	if err != nil {
		return nil, err
	}
	defer release()

	start := time.Now()
	resp := &Response{Results: make([]Result, 0, len(statements)), ReadOnly: opts.ReadOnly}

	for _, stmt := range statements {
		res := s.executeOne(ctx, conn, stmt, maxRows, opts.Params)
		resp.Results = append(resp.Results, res)
		if res.Error != "" {
			// Stop at the first failure: continuing would run later statements
			// against a state the author did not intend.
			resp.Failed = true
			break
		}
	}
	resp.DurationMS = float64(time.Since(start).Microseconds()) / 1000
	return resp, nil
}

// connection returns a connection appropriate to the requested access level.
func (s *Service) connection(ctx context.Context, dbName string, readOnly bool) (*sql.Conn, func(), error) {
	if readOnly {
		// mode=ro makes SQLite reject writes regardless of what the SQL says.
		db, err := s.mgr.OpenReadOnly(ctx, dbName)
		if err != nil {
			return nil, nil, err
		}
		conn, err := db.Conn(ctx)
		if err != nil {
			db.Close()
			return nil, nil, err
		}
		return conn, func() { conn.Close(); db.Close() }, nil
	}

	h, err := s.mgr.Get(ctx, dbName)
	if err != nil {
		return nil, nil, err
	}
	// The writer pool holds a single connection, so ad-hoc SQL is serialised
	// against other writers rather than competing for the write lock.
	conn, err := h.Writer().Conn(ctx)
	if err != nil {
		h.Release()
		return nil, nil, err
	}
	return conn, func() { conn.Close(); h.Release() }, nil
}

func (s *Service) executeOne(ctx context.Context, conn *sql.Conn, stmt string, maxRows int, params []any) Result {
	res := Result{Statement: stmt, Kind: Classify(stmt), Columns: []string{}, Rows: []map[string]any{}}
	start := time.Now()
	defer func() { res.DurationMS = float64(time.Since(start).Microseconds()) / 1000 }()

	// A statement that returns rows is queried; anything else is executed so
	// that RowsAffected and LastInsertID are available.
	if res.Kind == KindSelect || res.Kind == KindPragma || res.Kind == KindOther {
		rows, err := conn.QueryContext(ctx, stmt, params...)
		if err != nil {
			// Some statements classified as row-returning do not return rows;
			// fall back to Exec rather than reporting a spurious error.
			if isNoRowsReturned(err) {
				return s.execStatement(ctx, conn, stmt, params, res)
			}
			res.Error = userFacingSQLError(err)
			return res
		}
		defer rows.Close()

		cols, err := rows.Columns()
		if err != nil {
			res.Error = userFacingSQLError(err)
			return res
		}
		res.Columns = cols

		for rows.Next() {
			if len(res.Rows) >= maxRows {
				// The cap protects the server's memory and the browser's; the
				// client is told the result was cut short.
				res.Truncated = true
				break
			}
			values, targets := database.ScanTargets(len(cols))
			if err := rows.Scan(targets...); err != nil {
				res.Error = userFacingSQLError(err)
				return res
			}
			record := make(map[string]any, len(cols))
			for i, name := range cols {
				record[name] = database.NormalizeValue(values[i])
			}
			res.Rows = append(res.Rows, record)
		}
		if err := rows.Err(); err != nil {
			res.Error = userFacingSQLError(err)
		}
		return res
	}

	return s.execStatement(ctx, conn, stmt, params, res)
}

func (s *Service) execStatement(ctx context.Context, conn *sql.Conn, stmt string, params []any, res Result) Result {
	out, err := conn.ExecContext(ctx, stmt, params...)
	if err != nil {
		res.Error = userFacingSQLError(err)
		return res
	}
	if n, err := out.RowsAffected(); err == nil {
		res.RowsAffected = n
	}
	if id, err := out.LastInsertId(); err == nil {
		res.LastInsertID = id
	}
	return res
}

// isNoRowsReturned reports whether the driver refused a Query because the
// statement produces no result set.
func isNoRowsReturned(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no result set") || strings.Contains(msg, "not a query")
}

// userFacingSQLError renders a SQLite error for display in the editor.
//
// SQLite's messages describe the operator's own SQL ("no such column: foo"),
// which is exactly the feedback the editor exists to give. They are still
// filtered so that filesystem paths never reach the client.
func userFacingSQLError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "the query took too long and was cancelled"
	}
	if errors.Is(err, context.Canceled) {
		return "the query was cancelled"
	}

	msg := err.Error()
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "readonly") || strings.Contains(lower, "attempt to write") {
		return "this statement writes to the database, and your role only allows read-only SQL"
	}
	// Strip anything that looks like a filesystem path.
	if i := strings.Index(msg, "/"); i >= 0 && strings.Contains(msg, ".db") {
		return "the statement could not be executed"
	}
	const maxLen = 500
	if len(msg) > maxLen {
		msg = msg[:maxLen] + "…"
	}
	return msg
}
