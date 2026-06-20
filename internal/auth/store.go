package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"

	"github.com/Toshik1978/folio/internal/db"
	"github.com/Toshik1978/folio/internal/db/dbq"
)

// View reports the configured OPDS username and whether a password is set. The
// password itself is write-only and never returned.
func (a *Authenticator) View(ctx context.Context) (user string, set bool, err error) {
	c, err := a.loadCredentials(ctx)
	if err != nil {
		return "", false, fmt.Errorf("failed to load credentials: %w", err)
	}

	return c.user, c.hash != "", nil
}

// loadCredentials reads both settings.
func (a *Authenticator) loadCredentials(ctx context.Context) (*credentials, error) {
	user, err := a.setting(ctx, db.SettingOPDSUser)
	if err != nil {
		return nil, fmt.Errorf("load user name: %w", err)
	}
	hash, err := a.setting(ctx, db.SettingOPDSPassHash)
	if err != nil {
		return nil, fmt.Errorf("load password hash: %w", err)
	}

	return &credentials{user: user, hash: hash, configured: user != "" && hash != ""}, nil
}

// setting returns a setting value, or "" if it has not been set.
func (a *Authenticator) setting(ctx context.Context, key string) (string, error) {
	v, err := a.q.GetSetting(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get setting %q: %w", key, err)
	}

	return v, nil
}

// SetCredentials updates the OPDS credentials and drops the caches. A nil user
// leaves the username unchanged; a nil or empty pass leaves the password
// unchanged. A provided password is bcrypt-hashed before storage. Hashing
// happens before the transaction is opened so a hash failure can't leave a
// transaction open.
func (a *Authenticator) SetCredentials(ctx context.Context, user, pass *string) error {
	var passHash string
	if pass != nil && *pass != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(*pass), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash opds password: %w", err)
		}
		passHash = string(hash)
	}

	if err := a.writeCredentials(ctx, user, passHash); err != nil {
		return err
	}
	a.invalidate()

	return nil
}

// writeCredentials persists the provided settings in a single transaction, so a
// failure between the two upserts can't leave the username changed but the
// password not (or vice versa). A nil user or empty passHash is left unchanged.
func (a *Authenticator) writeCredentials(ctx context.Context, user *string, passHash string) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin settings tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	q := a.q.WithTx(tx)
	if user != nil {
		if err := q.UpsertSetting(ctx, dbq.UpsertSettingParams{Key: db.SettingOPDSUser, Value: *user}); err != nil {
			return fmt.Errorf("upsert opds user: %w", err)
		}
	}
	if passHash != "" {
		if err := q.UpsertSetting(
			ctx,
			dbq.UpsertSettingParams{Key: db.SettingOPDSPassHash, Value: passHash},
		); err != nil {
			return fmt.Errorf("upsert opds pass: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit settings tx: %w", err)
	}

	return nil
}
