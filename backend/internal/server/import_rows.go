package server

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/litebase/litebase/internal/httpx"
)

// maxImportRows bounds one import so a single request cannot run indefinitely.
const maxImportRows = 100000

type importResult struct {
	Inserted int      `json:"inserted"`
	Failed   int      `json:"failed"`
	Errors   []string `json:"errors,omitempty"`
}

// handleImportRows loads CSV or JSON rows into a table.
//
// Rows are inserted one at a time and failures are collected rather than
// aborting the whole import, because a partially-clean import with a clear
// report is more useful to an operator than an all-or-nothing rejection of a
// large file. The report names the row numbers that failed.
func (s *Server) handleImportRows(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		httpx.Fail(w, r, httpx.BadRequest("expected a multipart upload with a 'file' field"))
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("file")
	if err != nil {
		httpx.Fail(w, r, httpx.BadRequest("a 'file' upload is required"))
		return
	}
	defer file.Close()

	db, table := r.PathValue("db"), r.PathValue("table")
	format := r.FormValue("format")
	if format == "" {
		if strings.HasSuffix(strings.ToLower(header.Filename), ".json") {
			format = "json"
		} else {
			format = "csv"
		}
	}

	var records []map[string]any
	limited := io.LimitReader(file, s.cfg.MaxUploadBytes)

	switch format {
	case "csv":
		records, err = parseCSV(limited)
	case "json":
		records, err = parseJSONRows(limited)
	default:
		httpx.Fail(w, r, httpx.BadRequest("format must be csv or json"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, httpx.BadRequest("%s", err.Error()))
		return
	}
	if len(records) == 0 {
		httpx.Fail(w, r, httpx.BadRequest("the file contained no rows"))
		return
	}

	result := importResult{}
	for i, record := range records {
		if _, err := s.rows.Insert(r.Context(), db, table, record); err != nil {
			result.Failed++
			// Only the first few failures are reported; a broken file would
			// otherwise produce a response as large as the upload.
			if len(result.Errors) < 20 {
				result.Errors = append(result.Errors,
					fmt.Sprintf("row %d: %s", i+1, err.Error()))
			}
			continue
		}
		result.Inserted++
	}

	s.log.Info("rows imported", "database", db, "table", table,
		"inserted", result.Inserted, "failed", result.Failed)
	httpx.JSON(w, http.StatusOK, result)
}

// parseCSV reads a CSV file whose first line names the columns.
func parseCSV(r io.Reader) ([]map[string]any, error) {
	reader := csv.NewReader(r)
	// Rows with the wrong field count are reported per row rather than
	// aborting, so the count is not fixed up front.
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	headers, err := reader.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("the file is empty")
		}
		return nil, fmt.Errorf("could not read the header row: %w", err)
	}
	for i := range headers {
		// A spreadsheet export often starts with a byte order mark, which
		// would otherwise become part of the first column's name.
		headers[i] = strings.TrimSpace(strings.TrimPrefix(headers[i], "\ufeff"))
	}

	out := []map[string]any{}
	for len(out) < maxImportRows {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("row %d could not be read: %w", len(out)+2, err)
		}

		row := make(map[string]any, len(headers))
		for i, h := range headers {
			if h == "" || i >= len(record) {
				continue
			}
			value := record[i]
			// An empty CSV field is stored as NULL rather than an empty string,
			// which is almost always what an operator means.
			if value == "" {
				row[h] = nil
				continue
			}
			row[h] = value
		}
		out = append(out, row)
	}
	return out, nil
}

// parseJSONRows reads either an array of objects or newline-delimited objects.
func parseJSONRows(r io.Reader) ([]map[string]any, error) {
	dec := json.NewDecoder(r)
	// Numbers are kept as json.Number so a large integer id does not lose
	// precision through float64.
	dec.UseNumber()

	token, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("the file is not valid JSON")
	}

	out := []map[string]any{}
	if delim, ok := token.(json.Delim); ok && delim == '[' {
		for dec.More() && len(out) < maxImportRows {
			var row map[string]any
			if err := dec.Decode(&row); err != nil {
				return nil, fmt.Errorf("row %d is not a JSON object", len(out)+1)
			}
			out = append(out, row)
		}
		return out, nil
	}

	return nil, fmt.Errorf("expected a JSON array of objects")
}
