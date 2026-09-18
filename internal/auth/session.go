package auth

import (
	"context"
	"database/sql"
	"time"
)

// Session is the authenticated identity attached to the current request.
type Session struct {
	UserSub   string
	Email     string
	Name      string
	CSRFToken string
	ExpiresAt time.Time
}

// sessionRow is one row in oidc_sessions. Rows live in two phases: pre-auth
// rows carry the state/nonce/code_verifier trio and an empty UserSub;
// authenticated rows carry the user identity and empty state/nonce/verifier.
type sessionRow struct {
	ID           string
	State        string
	Nonce        string
	CodeVerifier string
	ReturnTo     string
	CSRFToken    string
	UserSub      string
	Email        string
	Name         string
	IDToken      string
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

type sessionStore struct{ db *sql.DB }

const oidcSessionsSchema = `
	CREATE TABLE IF NOT EXISTS oidc_sessions (
		id TEXT PRIMARY KEY, state TEXT NOT NULL, nonce TEXT NOT NULL,
		code_verifier TEXT NOT NULL, return_to TEXT NOT NULL, csrf_token TEXT NOT NULL,
		user_sub TEXT NOT NULL, email TEXT NOT NULL, name TEXT NOT NULL, id_token TEXT NOT NULL,
		created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL
	)`

func (s *sessionStore) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, oidcSessionsSchema)
	return err
}

// Create inserts a session row and opportunistically sweeps expired rows so
// the table does not grow without bound.
func (s *sessionStore) Create(ctx context.Context, row sessionRow) error {
	_ = s.Sweep(ctx)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO oidc_sessions (id, state, nonce, code_verifier, return_to, csrf_token, user_sub, email, name, id_token, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.State, row.Nonce, row.CodeVerifier, row.ReturnTo, row.CSRFToken,
		row.UserSub, row.Email, row.Name, row.IDToken,
		row.CreatedAt.Unix(), row.ExpiresAt.Unix())
	return err
}

// Get returns the row with the given id, or sql.ErrNoRows when absent.
func (s *sessionStore) Get(ctx context.Context, id string) (sessionRow, error) {
	var row sessionRow
	var created, expires int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, state, nonce, code_verifier, return_to, csrf_token, user_sub, email, name, id_token, created_at, expires_at
		FROM oidc_sessions WHERE id = ?`, id).
		Scan(&row.ID, &row.State, &row.Nonce, &row.CodeVerifier, &row.ReturnTo, &row.CSRFToken,
			&row.UserSub, &row.Email, &row.Name, &row.IDToken, &created, &expires)
	if err != nil {
		return sessionRow{}, err
	}
	row.CreatedAt = time.Unix(created, 0)
	row.ExpiresAt = time.Unix(expires, 0)
	return row, nil
}

func (s *sessionStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM oidc_sessions WHERE id = ?`, id)
	return err
}

// Sweep deletes every row whose expiry has passed.
func (s *sessionStore) Sweep(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM oidc_sessions WHERE expires_at < ?`, time.Now().Unix())
	return err
}
