package radar

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		PRAGMA journal_mode=WAL;
		CREATE TABLE IF NOT EXISTS events (
			uid TEXT PRIMARY KEY, source TEXT NOT NULL, source_id TEXT NOT NULL,
			title TEXT NOT NULL, description TEXT NOT NULL, location TEXT NOT NULL, url TEXT NOT NULL,
			starts_at TEXT NOT NULL, ends_at TEXT NOT NULL, status TEXT NOT NULL,
			anchor INTEGER NOT NULL, score INTEGER NOT NULL, fingerprint TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS source_health (
			name TEXT PRIMARY KEY, enabled INTEGER NOT NULL, state TEXT NOT NULL,
			last_success TEXT, last_error TEXT
		);
		CREATE TABLE IF NOT EXISTS candidates (
			source TEXT NOT NULL, url TEXT NOT NULL PRIMARY KEY, title TEXT NOT NULL,
			snippet TEXT NOT NULL, score INTEGER NOT NULL, discovered_at TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending', verification TEXT NOT NULL DEFAULT 'unverified',
			event_title TEXT NOT NULL DEFAULT '', start_time TEXT NOT NULL DEFAULT '',
			end_time TEXT NOT NULL DEFAULT '', location TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '', evidence_url TEXT NOT NULL DEFAULT '',
			date_evidence TEXT NOT NULL DEFAULT '', location_evidence TEXT NOT NULL DEFAULT '',
			confidence TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '',
			verified_at TEXT, reviewed_at TEXT, review_note TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE IF NOT EXISTS deliveries (
			kind TEXT PRIMARY KEY, content_hash TEXT NOT NULL, delivered_at TEXT NOT NULL
		);
	`)
	if err != nil {
		return err
	}
	// Existing installations predate the candidate workflow. SQLite has no
	// portable IF NOT EXISTS form for columns, so add each missing column.
	columns := []string{
		"status TEXT NOT NULL DEFAULT 'pending'",
		"verification TEXT NOT NULL DEFAULT 'unverified'",
		"event_title TEXT NOT NULL DEFAULT ''",
		"start_time TEXT NOT NULL DEFAULT ''",
		"end_time TEXT NOT NULL DEFAULT ''",
		"location TEXT NOT NULL DEFAULT ''",
		"description TEXT NOT NULL DEFAULT ''",
		"evidence_url TEXT NOT NULL DEFAULT ''",
		"date_evidence TEXT NOT NULL DEFAULT ''",
		"location_evidence TEXT NOT NULL DEFAULT ''",
		"confidence TEXT NOT NULL DEFAULT ''",
		"last_error TEXT NOT NULL DEFAULT ''",
		"verified_at TEXT",
		"reviewed_at TEXT",
		"review_note TEXT NOT NULL DEFAULT ''",
	}
	for _, column := range columns {
		name := column[:strings.IndexByte(column, ' ')]
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('candidates') WHERE name = ?`, name).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			if _, err := s.db.ExecContext(ctx, "ALTER TABLE candidates ADD COLUMN "+column); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) UpsertEvent(ctx context.Context, event Event) error {
	now := time.Now().UTC()
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	event.UpdatedAt = now
	if event.UID == "" {
		event.UID = EventUID(event.Source, event.SourceID, event.Title, event.StartsAt)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO events (uid, source, source_id, title, description, location, url, starts_at, ends_at, status, anchor, score, fingerprint, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(fingerprint) DO UPDATE SET
			source=excluded.source, source_id=excluded.source_id, title=excluded.title,
			description=excluded.description, location=excluded.location, url=excluded.url,
			ends_at=excluded.ends_at, status=excluded.status, anchor=excluded.anchor,
			score=excluded.score, updated_at=excluded.updated_at`,
		event.UID, event.Source, event.SourceID, event.Title, event.Description, event.Location, event.URL,
		event.StartsAt.UTC().Format(time.RFC3339), event.EndsAt.UTC().Format(time.RFC3339), event.Status,
		boolInt(event.Anchor), event.Score, EventFingerprint(event), event.CreatedAt.UTC().Format(time.RFC3339), event.UpdatedAt.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) UpcomingEvents(ctx context.Context, from time.Time) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT uid, source, source_id, title, description, location, url, starts_at, ends_at, status, anchor, score, created_at, updated_at FROM events WHERE starts_at >= ? ORDER BY starts_at`, from.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var events []Event
	for rows.Next() {
		var event Event
		var starts, ends, created, updated string
		var anchor int
		if err := rows.Scan(&event.UID, &event.Source, &event.SourceID, &event.Title, &event.Description, &event.Location, &event.URL, &starts, &ends, &event.Status, &anchor, &event.Score, &created, &updated); err != nil {
			return nil, err
		}
		event.Anchor = anchor == 1
		event.StartsAt, _ = time.Parse(time.RFC3339, starts)
		event.EndsAt, _ = time.Parse(time.RFC3339, ends)
		event.CreatedAt, _ = time.Parse(time.RFC3339, created)
		event.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) PruneCandidates(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM candidates
		WHERE status <> 'approved'
		AND verification = 'verified'
		AND start_time <> '' AND start_time <= ?`,
		now.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) SaveSourceHealth(ctx context.Context, health SourceHealth) error {
	var success any
	if health.LastSuccess != nil {
		success = health.LastSuccess.UTC().Format(time.RFC3339)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO source_health(name, enabled, state, last_success, last_error) VALUES (?, ?, ?, ?, ?) ON CONFLICT(name) DO UPDATE SET enabled=excluded.enabled, state=excluded.state, last_success=excluded.last_success, last_error=excluded.last_error`, health.Name, boolInt(health.Enabled), health.State, success, health.LastError)
	return err
}

func (s *Store) SourceHealth(ctx context.Context) ([]SourceHealth, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, enabled, state, last_success, last_error FROM source_health ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var output []SourceHealth
	for rows.Next() {
		var h SourceHealth
		var enabled int
		var success sql.NullString
		if err := rows.Scan(&h.Name, &enabled, &h.State, &success, &h.LastError); err != nil {
			return nil, err
		}
		h.Enabled = enabled == 1
		if success.Valid {
			value, err := time.Parse(time.RFC3339, success.String)
			if err != nil {
				return nil, fmt.Errorf("parse health time: %w", err)
			}
			h.LastSuccess = &value
		}
		output = append(output, h)
	}
	return output, rows.Err()
}

func (s *Store) UpsertCandidate(ctx context.Context, candidate Candidate) error {
	if candidate.Status == "" {
		candidate.Status = CandidatePending
	}
	if candidate.Verification == "" {
		candidate.Verification = "unverified"
	}
	if candidate.Discovered.IsZero() {
		candidate.Discovered = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO candidates (
			source, url, title, snippet, score, discovered_at, status, verification,
			event_title, start_time, end_time, location, description, evidence_url,
			date_evidence, location_evidence, confidence, last_error, verified_at,
			reviewed_at, review_note
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(url) DO UPDATE SET
			source=excluded.source, title=excluded.title, snippet=excluded.snippet,
			score=excluded.score, discovered_at=excluded.discovered_at,
			verification=excluded.verification, last_error=excluded.last_error,
			verified_at=excluded.verified_at,
			event_title=CASE WHEN candidates.event_title = '' THEN excluded.event_title ELSE candidates.event_title END,
			start_time=CASE WHEN candidates.start_time = '' THEN excluded.start_time ELSE candidates.start_time END,
			end_time=CASE WHEN candidates.end_time = '' THEN excluded.end_time ELSE candidates.end_time END,
			location=CASE WHEN candidates.location = '' THEN excluded.location ELSE candidates.location END,
			description=CASE WHEN candidates.description = '' THEN excluded.description ELSE candidates.description END,
			evidence_url=CASE WHEN candidates.evidence_url = '' THEN excluded.evidence_url ELSE candidates.evidence_url END,
			date_evidence=CASE WHEN candidates.date_evidence = '' THEN excluded.date_evidence ELSE candidates.date_evidence END,
			location_evidence=CASE WHEN candidates.location_evidence = '' THEN excluded.location_evidence ELSE candidates.location_evidence END,
			confidence=CASE WHEN candidates.confidence = '' THEN excluded.confidence ELSE candidates.confidence END`,
		candidate.Source, candidate.URL, candidate.Title, candidate.Snippet, candidate.Score,
		candidate.Discovered.UTC().Format(time.RFC3339), candidate.Status, candidate.Verification,
		candidate.EventTitle, formatOptionalTime(candidate.StartTime), formatOptionalTime(candidate.EndTime),
		candidate.Location, candidate.Description, candidate.EvidenceURL, candidate.DateEvidence,
		candidate.LocationEvidence, candidate.Confidence, candidate.LastError,
		formatOptionalTimePtr(candidate.VerifiedAt), formatOptionalTimePtr(candidate.ReviewedAt), candidate.ReviewNote)
	return err
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func formatOptionalTimePtr(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339)
}

func (s *Store) Candidates(ctx context.Context, includeRejected bool) ([]Candidate, error) {
	query := `SELECT source, url, title, snippet, score, discovered_at, status, verification,
		event_title, start_time, end_time, location, description, evidence_url,
		date_evidence, location_evidence, confidence, last_error, verified_at,
		reviewed_at, review_note FROM candidates`
	if !includeRejected {
		query += ` WHERE status <> 'rejected'`
	}
	query += ` ORDER BY CASE status WHEN 'pending' THEN 0 WHEN 'verified' THEN 1 ELSE 2 END, score DESC, discovered_at DESC`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var output []Candidate
	for rows.Next() {
		var candidate Candidate
		var discovered, start, end, verified, reviewed sql.NullString
		if err := rows.Scan(&candidate.Source, &candidate.URL, &candidate.Title, &candidate.Snippet,
			&candidate.Score, &discovered, &candidate.Status, &candidate.Verification,
			&candidate.EventTitle, &start, &end, &candidate.Location, &candidate.Description,
			&candidate.EvidenceURL, &candidate.DateEvidence, &candidate.LocationEvidence,
			&candidate.Confidence, &candidate.LastError, &verified, &reviewed, &candidate.ReviewNote); err != nil {
			return nil, err
		}
		candidate.Discovered, _ = time.Parse(time.RFC3339, discovered.String)
		candidate.StartTime, _ = time.Parse(time.RFC3339, start.String)
		candidate.EndTime, _ = time.Parse(time.RFC3339, end.String)
		if verified.Valid {
			value, parseErr := time.Parse(time.RFC3339, verified.String)
			if parseErr != nil {
				return nil, parseErr
			}
			candidate.VerifiedAt = &value
		}
		if reviewed.Valid {
			value, parseErr := time.Parse(time.RFC3339, reviewed.String)
			if parseErr != nil {
				return nil, parseErr
			}
			candidate.ReviewedAt = &value
		}
		output = append(output, candidate)
	}
	return output, rows.Err()
}

func (s *Store) UpdateCandidate(ctx context.Context, candidate Candidate) error {
	if candidate.Status == "" {
		candidate.Status = CandidatePending
	}
	_, err := s.db.ExecContext(ctx, `UPDATE candidates SET title=?, status=?, verification=?,
		event_title=?, start_time=?, end_time=?, location=?, description=?, evidence_url=?,
		date_evidence=?, location_evidence=?, confidence=?, last_error=?, verified_at=?,
		reviewed_at=?, review_note=? WHERE url=?`,
		candidate.Title, candidate.Status, candidate.Verification, candidate.EventTitle,
		formatOptionalTime(candidate.StartTime), formatOptionalTime(candidate.EndTime), candidate.Location,
		candidate.Description, candidate.EvidenceURL, candidate.DateEvidence, candidate.LocationEvidence,
		candidate.Confidence, candidate.LastError, formatOptionalTimePtr(candidate.VerifiedAt),
		formatOptionalTimePtr(candidate.ReviewedAt), candidate.ReviewNote, candidate.URL)
	return err
}

func (s *Store) Candidate(ctx context.Context, rawURL string) (Candidate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source, url, title, snippet, score, discovered_at, status, verification,
		event_title, start_time, end_time, location, description, evidence_url, date_evidence,
		location_evidence, confidence, last_error, verified_at, reviewed_at, review_note
		FROM candidates WHERE url = ?`, rawURL)
	if err != nil {
		return Candidate{}, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Candidate{}, err
		}
		return Candidate{}, sql.ErrNoRows
	}
	var candidate Candidate
	var discovered, start, end, verified, reviewed sql.NullString
	if err := rows.Scan(&candidate.Source, &candidate.URL, &candidate.Title, &candidate.Snippet,
		&candidate.Score, &discovered, &candidate.Status, &candidate.Verification,
		&candidate.EventTitle, &start, &end, &candidate.Location, &candidate.Description,
		&candidate.EvidenceURL, &candidate.DateEvidence, &candidate.LocationEvidence,
		&candidate.Confidence, &candidate.LastError, &verified, &reviewed, &candidate.ReviewNote); err != nil {
		return Candidate{}, err
	}
	candidate.Discovered, _ = time.Parse(time.RFC3339, discovered.String)
	candidate.StartTime, _ = time.Parse(time.RFC3339, start.String)
	candidate.EndTime, _ = time.Parse(time.RFC3339, end.String)
	if verified.Valid {
		value, parseErr := time.Parse(time.RFC3339, verified.String)
		if parseErr != nil {
			return Candidate{}, parseErr
		}
		candidate.VerifiedAt = &value
	}
	if reviewed.Valid {
		value, parseErr := time.Parse(time.RFC3339, reviewed.String)
		if parseErr != nil {
			return Candidate{}, parseErr
		}
		candidate.ReviewedAt = &value
	}
	return candidate, nil
}

func (s *Store) CandidateCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM candidates GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	counts := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

func (s *Store) DeliveryChanged(ctx context.Context, kind, contentHash string) (bool, error) {
	var previous string
	err := s.db.QueryRowContext(ctx, `SELECT content_hash FROM deliveries WHERE kind = ?`, kind).Scan(&previous)
	if err == sql.ErrNoRows {
		return true, nil
	}
	return previous != contentHash, err
}

func (s *Store) MarkDelivered(ctx context.Context, kind, contentHash string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO deliveries(kind, content_hash, delivered_at) VALUES (?, ?, ?) ON CONFLICT(kind) DO UPDATE SET content_hash=excluded.content_hash, delivered_at=excluded.delivered_at`, kind, contentHash, time.Now().UTC().Format(time.RFC3339))
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
