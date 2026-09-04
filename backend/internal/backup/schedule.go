package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/litebase/litebase/internal/database"
)

var ErrScheduleNotFound = errors.New("backup schedule not found")

// Schedule describes recurring backups for one database, or for all of them.
//
// The interval is a plain duration rather than a cron expression: the operator
// need is "every N hours", and a duration is unambiguous across time zones and
// daylight-saving changes, which cron expressions are not.
type Schedule struct {
	ID           string `json:"id"`
	DatabaseID   string `json:"database_id"`
	DatabaseName string `json:"database_name"`
	Name         string `json:"name"`
	IntervalSecs int    `json:"interval_secs"`
	Provider     string `json:"provider"`
	Encrypt      bool   `json:"encrypt"`
	// RetentionCount keeps at most N successful backups; zero means unlimited.
	RetentionCount int `json:"retention_count"`
	// RetentionDays discards backups older than N days; zero means unlimited.
	RetentionDays int        `json:"retention_days"`
	Enabled       bool       `json:"enabled"`
	LastRunAt     *time.Time `json:"last_run_at"`
	LastStatus    string     `json:"last_status"`
	LastError     string     `json:"last_error,omitempty"`
	NextRunAt     time.Time  `json:"next_run_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// MinInterval bounds how often a schedule may fire. Backups are IO-heavy, so a
// very short interval would keep the server permanently busy.
const MinInterval = 5 * time.Minute

// Validate normalises and checks a schedule.
func (s *Schedule) Validate() error {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" || len(s.Name) > 100 {
		return fmt.Errorf("schedule name must be between 1 and 100 characters")
	}
	if time.Duration(s.IntervalSecs)*time.Second < MinInterval {
		return fmt.Errorf("the interval must be at least %s", MinInterval)
	}
	if s.IntervalSecs > int((365 * 24 * time.Hour).Seconds()) {
		return fmt.Errorf("the interval must be at most one year")
	}
	if s.RetentionCount < 0 || s.RetentionDays < 0 {
		return fmt.Errorf("retention values must not be negative")
	}
	if s.Provider == "" {
		s.Provider = "local"
	}
	return nil
}

// CreateSchedule stores a new schedule.
func (s *Service) CreateSchedule(ctx context.Context, sch *Schedule) error {
	if err := sch.Validate(); err != nil {
		return err
	}
	if !s.storage.Has(sch.Provider) {
		return fmt.Errorf("storage provider %q is not configured", sch.Provider)
	}
	if sch.Encrypt && s.encKey == "" {
		return ErrNoKey
	}

	// An empty database id means "every database", which is a deliberate
	// option rather than a missing value.
	var dbID any
	if sch.DatabaseID != "" {
		meta, err := s.mgr.FindByID(ctx, sch.DatabaseID)
		if err != nil {
			return fmt.Errorf("the selected database does not exist")
		}
		sch.DatabaseName = meta.Name
		dbID = sch.DatabaseID
	}

	now := time.Now().UTC()
	sch.ID = database.NewID()
	sch.CreatedAt, sch.UpdatedAt = now, now
	// The first run is one full interval away, so creating a schedule does not
	// immediately trigger a backup.
	sch.NextRunAt = now.Add(time.Duration(sch.IntervalSecs) * time.Second)

	_, err := s.mgr.MetaDB().ExecContext(ctx,
		`INSERT INTO backup_schedules (id, database_id, name, interval_secs, provider, encrypt,
			retention_count, retention_days, enabled, next_run_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sch.ID, dbID, sch.Name, sch.IntervalSecs, sch.Provider, boolToInt(sch.Encrypt),
		sch.RetentionCount, sch.RetentionDays, boolToInt(sch.Enabled),
		database.FormatTime(sch.NextRunAt), database.FormatTime(now), database.FormatTime(now))
	if err != nil {
		return fmt.Errorf("create schedule: %w", err)
	}
	return nil
}

// UpdateSchedule replaces a schedule's settings.
func (s *Service) UpdateSchedule(ctx context.Context, sch *Schedule) error {
	if err := sch.Validate(); err != nil {
		return err
	}
	if !s.storage.Has(sch.Provider) {
		return fmt.Errorf("storage provider %q is not configured", sch.Provider)
	}
	if sch.Encrypt && s.encKey == "" {
		return ErrNoKey
	}

	var dbID any
	if sch.DatabaseID != "" {
		if _, err := s.mgr.FindByID(ctx, sch.DatabaseID); err != nil {
			return fmt.Errorf("the selected database does not exist")
		}
		dbID = sch.DatabaseID
	}

	// Changing the interval re-bases the next run so a shortened interval takes
	// effect immediately rather than after the old, longer wait.
	next := time.Now().UTC().Add(time.Duration(sch.IntervalSecs) * time.Second)
	res, err := s.mgr.MetaDB().ExecContext(ctx,
		`UPDATE backup_schedules SET database_id = ?, name = ?, interval_secs = ?, provider = ?,
			encrypt = ?, retention_count = ?, retention_days = ?, enabled = ?,
			next_run_at = ?, updated_at = ? WHERE id = ?`,
		dbID, sch.Name, sch.IntervalSecs, sch.Provider, boolToInt(sch.Encrypt),
		sch.RetentionCount, sch.RetentionDays, boolToInt(sch.Enabled),
		database.FormatTime(next), database.FormatTime(time.Now().UTC()), sch.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrScheduleNotFound
	}
	return nil
}

// DeleteSchedule removes a schedule. Backups it produced are kept.
func (s *Service) DeleteSchedule(ctx context.Context, id string) error {
	res, err := s.mgr.MetaDB().ExecContext(ctx, `DELETE FROM backup_schedules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrScheduleNotFound
	}
	return nil
}

const scheduleColumns = `s.id, COALESCE(s.database_id, ''), COALESCE(d.name, ''), s.name,
	s.interval_secs, s.provider, s.encrypt, s.retention_count, s.retention_days,
	s.enabled, s.last_run_at, s.last_status, s.last_error, s.next_run_at,
	s.created_at, s.updated_at`

func scanSchedule(sc interface{ Scan(...any) error }) (*Schedule, error) {
	var (
		sch                    Schedule
		encrypt, enabled       int
		lastRun                sql.NullString
		next, created, updated string
	)
	if err := sc.Scan(&sch.ID, &sch.DatabaseID, &sch.DatabaseName, &sch.Name,
		&sch.IntervalSecs, &sch.Provider, &encrypt, &sch.RetentionCount, &sch.RetentionDays,
		&enabled, &lastRun, &sch.LastStatus, &sch.LastError, &next,
		&created, &updated); err != nil {
		return nil, err
	}
	sch.Encrypt = encrypt != 0
	sch.Enabled = enabled != 0
	sch.NextRunAt = database.ParseTime(next)
	sch.CreatedAt = database.ParseTime(created)
	sch.UpdatedAt = database.ParseTime(updated)
	if lastRun.Valid && lastRun.String != "" {
		t := database.ParseTime(lastRun.String)
		sch.LastRunAt = &t
	}
	return &sch, nil
}

// ListSchedules returns every schedule.
func (s *Service) ListSchedules(ctx context.Context) ([]Schedule, error) {
	rows, err := s.mgr.MetaRead().QueryContext(ctx,
		`SELECT `+scheduleColumns+` FROM backup_schedules s
		 LEFT JOIN databases d ON d.id = s.database_id
		 ORDER BY s.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Schedule{}
	for rows.Next() {
		sch, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sch)
	}
	return out, rows.Err()
}

// GetSchedule returns one schedule.
func (s *Service) GetSchedule(ctx context.Context, id string) (*Schedule, error) {
	row := s.mgr.MetaRead().QueryRowContext(ctx,
		`SELECT `+scheduleColumns+` FROM backup_schedules s
		 LEFT JOIN databases d ON d.id = s.database_id
		 WHERE s.id = ?`, id)
	sch, err := scanSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrScheduleNotFound
	}
	return sch, err
}

// DueSchedules returns the enabled schedules whose next run has arrived.
func (s *Service) DueSchedules(ctx context.Context, now time.Time) ([]Schedule, error) {
	rows, err := s.mgr.MetaRead().QueryContext(ctx,
		`SELECT `+scheduleColumns+` FROM backup_schedules s
		 LEFT JOIN databases d ON d.id = s.database_id
		 WHERE s.enabled = 1 AND s.next_run_at <= ?
		 ORDER BY s.next_run_at`, database.FormatTime(now.UTC()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Schedule{}
	for rows.Next() {
		sch, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sch)
	}
	return out, rows.Err()
}

// RunSchedule performs one schedule's backups and applies its retention policy.
func (s *Service) RunSchedule(ctx context.Context, sch *Schedule) error {
	targets := []string{}
	if sch.DatabaseID == "" {
		// A schedule with no database covers every database that exists at run
		// time, so newly created databases are included automatically.
		all, err := s.mgr.List(ctx)
		if err != nil {
			return err
		}
		for _, d := range all {
			targets = append(targets, d.Name)
		}
	} else {
		meta, err := s.mgr.FindByID(ctx, sch.DatabaseID)
		if err != nil {
			return fmt.Errorf("database for schedule %q no longer exists", sch.Name)
		}
		targets = append(targets, meta.Name)
	}

	var failures []string
	for _, dbName := range targets {
		if _, err := s.Create(ctx, CreateOptions{
			Database:   dbName,
			Provider:   sch.Provider,
			Encrypt:    sch.Encrypt,
			Trigger:    TriggerScheduled,
			ScheduleID: sch.ID,
		}); err != nil {
			// One database failing must not stop the others from being backed
			// up; every failure is collected and reported together.
			failures = append(failures, fmt.Sprintf("%s: %v", dbName, err))
			continue
		}
		if err := s.ApplyRetention(ctx, dbName, sch.RetentionCount, sch.RetentionDays); err != nil {
			s.log.Warn("apply retention", "database", dbName, "error", err)
		}
	}

	status, errMsg := "ok", ""
	if len(failures) > 0 {
		status = "failed"
		errMsg = strings.Join(failures, "; ")
	}
	if err := s.recordScheduleRun(ctx, sch, status, errMsg); err != nil {
		return err
	}
	if len(failures) > 0 {
		return fmt.Errorf("schedule %q had %d failure(s): %s", sch.Name, len(failures), errMsg)
	}
	return nil
}

// recordScheduleRun stores the outcome and schedules the next run.
func (s *Service) recordScheduleRun(ctx context.Context, sch *Schedule, status, errMsg string) error {
	now := time.Now().UTC()
	next := now.Add(time.Duration(sch.IntervalSecs) * time.Second)
	_, err := s.mgr.MetaDB().ExecContext(ctx,
		`UPDATE backup_schedules SET last_run_at = ?, last_status = ?, last_error = ?,
			next_run_at = ?, updated_at = ? WHERE id = ?`,
		database.FormatTime(now), status, truncateError(errMsg),
		database.FormatTime(next), database.FormatTime(now), sch.ID)
	return err
}

// ApplyRetention removes backups beyond the configured limits.
//
// Only completed, scheduled or manual backups are considered. The most recent
// successful backup is always kept, whatever the policy says, so a retention
// rule can never leave a database with no recovery point at all.
func (s *Service) ApplyRetention(ctx context.Context, dbName string, keepCount, keepDays int) error {
	if keepCount <= 0 && keepDays <= 0 {
		return nil
	}

	records, err := s.List(ctx, ListOptions{Database: dbName, Limit: 500})
	if err != nil {
		return err
	}

	completed := make([]Record, 0, len(records))
	for _, r := range records {
		if r.Status == StatusCompleted {
			completed = append(completed, r)
		}
	}
	if len(completed) <= 1 {
		return nil
	}

	// records are newest first, so index 0 is the one always retained.
	cutoff := time.Now().UTC().AddDate(0, 0, -keepDays)
	var toDelete []Record
	for i, r := range completed {
		if i == 0 {
			continue
		}
		switch {
		case keepCount > 0 && i >= keepCount:
			toDelete = append(toDelete, r)
		case keepDays > 0 && r.StartedAt.Before(cutoff):
			toDelete = append(toDelete, r)
		}
	}

	for _, r := range toDelete {
		if err := s.Delete(ctx, r.ID); err != nil {
			s.log.Warn("retention delete", "backup_id", r.ID, "error", err)
			continue
		}
		s.log.Info("retention removed backup", "backup_id", r.ID, "database", dbName)
	}
	return nil
}
