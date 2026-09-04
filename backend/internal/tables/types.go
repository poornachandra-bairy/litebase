// Package tables implements schema introspection and DDL for user databases.
package tables

import "time"

// ObjectKind distinguishes the schema objects SQLite stores.
type ObjectKind string

const (
	KindTable   ObjectKind = "table"
	KindView    ObjectKind = "view"
	KindIndex   ObjectKind = "index"
	KindTrigger ObjectKind = "trigger"
)

// Column describes one column of a table.
type Column struct {
	Name          string  `json:"name"`
	Type          string  `json:"type"`
	NotNull       bool    `json:"not_null"`
	PrimaryKey    bool    `json:"primary_key"`
	AutoIncrement bool    `json:"auto_increment"`
	Unique        bool    `json:"unique"`
	Default       *string `json:"default"`
	// Position is the column's ordinal within the table.
	Position int `json:"position"`
	// References, when set, declares a foreign key on this column.
	References *ForeignKeyRef `json:"references,omitempty"`
}

// ForeignKeyRef is the target of a column-level foreign key.
type ForeignKeyRef struct {
	Table    string `json:"table"`
	Column   string `json:"column"`
	OnDelete string `json:"on_delete,omitempty"`
	OnUpdate string `json:"on_update,omitempty"`
}

// ForeignKey is a foreign key as reported by SQLite, which may span columns.
type ForeignKey struct {
	ID       int      `json:"id"`
	Table    string   `json:"table"`
	From     []string `json:"from"`
	To       []string `json:"to"`
	OnDelete string   `json:"on_delete"`
	OnUpdate string   `json:"on_update"`
}

// Index describes an index on a table.
type Index struct {
	Name    string   `json:"name"`
	Table   string   `json:"table"`
	Unique  bool     `json:"unique"`
	Partial bool     `json:"partial"`
	Columns []string `json:"columns"`
	// Origin is SQLite's own classification: "c" for a CREATE INDEX statement,
	// "u" for a UNIQUE constraint and "pk" for a primary key. Only "c" indexes
	// can be dropped independently of the table.
	Origin string `json:"origin"`
	SQL    string `json:"sql"`
}

// Trigger describes a trigger.
type Trigger struct {
	Name  string `json:"name"`
	Table string `json:"table"`
	SQL   string `json:"sql"`
}

// View describes a view.
type View struct {
	Name string `json:"name"`
	SQL  string `json:"sql"`
}

// TableSummary is the lightweight listing entry.
type TableSummary struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	RowCount    int64  `json:"row_count"`
	ColumnCount int    `json:"column_count"`
	// WithoutRowID and Strict report SQLite table options that change how the
	// table behaves.
	WithoutRowID bool `json:"without_rowid"`
	Strict       bool `json:"strict"`
}

// Table is the full description of a table.
type Table struct {
	Name         string       `json:"name"`
	Kind         string       `json:"kind"`
	Columns      []Column     `json:"columns"`
	ForeignKeys  []ForeignKey `json:"foreign_keys"`
	Indexes      []Index      `json:"indexes"`
	Triggers     []Trigger    `json:"triggers"`
	RowCount     int64        `json:"row_count"`
	WithoutRowID bool         `json:"without_rowid"`
	Strict       bool         `json:"strict"`
	SQL          string       `json:"sql"`
}

// Schema is everything the browser sidebar needs for one database.
type Schema struct {
	Tables   []TableSummary `json:"tables"`
	Views    []View         `json:"views"`
	Indexes  []Index        `json:"indexes"`
	Triggers []Trigger      `json:"triggers"`
}

// Stats summarises a database for the dashboard.
type Stats struct {
	Name          string    `json:"name"`
	SizeBytes     int64     `json:"size_bytes"`
	PageSize      int       `json:"page_size"`
	PageCount     int64     `json:"page_count"`
	FreelistCount int64     `json:"freelist_count"`
	Encoding      string    `json:"encoding"`
	JournalMode   string    `json:"journal_mode"`
	ForeignKeys   bool      `json:"foreign_keys"`
	UserVersion   int       `json:"user_version"`
	SchemaVersion int       `json:"schema_version"`
	TableCount    int       `json:"table_count"`
	ViewCount     int       `json:"view_count"`
	IndexCount    int       `json:"index_count"`
	TriggerCount  int       `json:"trigger_count"`
	TotalRows     int64     `json:"total_rows"`
	CollectedAt   time.Time `json:"collected_at"`
}

// IntegrityReport is the result of a consistency check.
type IntegrityReport struct {
	OK               bool          `json:"ok"`
	Problems         []string      `json:"problems"`
	ForeignKeyErrors []FKViolation `json:"foreign_key_errors"`
	Duration         string        `json:"duration"`
}

// FKViolation is one row that breaks a foreign key constraint.
type FKViolation struct {
	Table   string `json:"table"`
	RowID   *int64 `json:"rowid"`
	Parent  string `json:"parent"`
	FKIndex int    `json:"fk_index"`
}
