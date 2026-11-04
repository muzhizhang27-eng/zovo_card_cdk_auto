package db

import (
	"database/sql"
	"fmt"
	"time"
)

// This additive migration preserves existing usernames and password hashes.
// Triggers also revoke sessions after direct maintenance updates to admin_users.
func MigrateAdminSessions() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS admin_sessions (
			session_hash TEXT PRIMARY KEY,
			admin_id INTEGER NOT NULL,
			csrf_hash TEXT NOT NULL,
			expires_at INTEGER NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_admin_sessions_user ON admin_sessions(admin_id)`,
		`CREATE INDEX IF NOT EXISTS idx_admin_sessions_expiry ON admin_sessions(expires_at)`,
		`CREATE TRIGGER IF NOT EXISTS revoke_admin_sessions_on_change
		AFTER UPDATE OF password_hash, is_active, username ON admin_users
		WHEN OLD.password_hash IS NOT NEW.password_hash OR OLD.is_active IS NOT NEW.is_active OR OLD.username IS NOT NEW.username
		BEGIN DELETE FROM admin_sessions WHERE admin_id = OLD.id; END`,
		`CREATE TRIGGER IF NOT EXISTS revoke_admin_sessions_on_delete
		AFTER DELETE ON admin_users
		BEGIN DELETE FROM admin_sessions WHERE admin_id = OLD.id; END`,
	}
	for _, query := range queries {
		if _, err := DB.Exec(query); err != nil {
			return fmt.Errorf("admin session migration: %w", err)
		}
	}
	return nil
}

func CreateAdminSession(userID int64, sessionHash, csrfHash string, expires time.Time, expectedHash ...string) error {
	if DB == nil {
		return fmt.Errorf("database unavailable")
	}
	now := time.Now().Unix()
	if _, err := DB.Exec(`DELETE FROM admin_sessions WHERE expires_at <= ?`, now); err != nil {
		return err
	}
	query := `INSERT INTO admin_sessions(session_hash, admin_id, csrf_hash, expires_at, created_at)
		SELECT ?, id, ?, ?, ? FROM admin_users WHERE id = ? AND is_active = 1`
	args := []any{sessionHash, csrfHash, expires.Unix(), now, userID}
	if len(expectedHash) > 0 {
		query += ` AND password_hash = ?`
		args = append(args, expectedHash[0])
	}
	result, err := DB.Exec(query, args...)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("admin unavailable")
	}
	return nil
}

func ValidateAdminSession(sessionHash string, userID int64, username string) (csrfHash string, err error) {
	if DB == nil {
		return "", fmt.Errorf("database unavailable")
	}
	err = DB.QueryRow(`SELECT s.csrf_hash FROM admin_sessions s
		JOIN admin_users a ON a.id = s.admin_id
		WHERE s.session_hash = ? AND s.admin_id = ? AND a.username = ?
		AND a.is_active = 1 AND s.expires_at > ?`, sessionHash, userID, username, time.Now().Unix()).Scan(&csrfHash)
	return
}

func RevokeAdminSession(sessionHash string) error {
	if DB == nil {
		return sql.ErrConnDone
	}
	_, err := DB.Exec(`DELETE FROM admin_sessions WHERE session_hash = ?`, sessionHash)
	return err
}
