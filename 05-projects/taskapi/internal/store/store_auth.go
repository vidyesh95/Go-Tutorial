package store

import (
	"context"
	"time"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/store/sqlcdb"
)

// Persistence for the Better Auth-style tables: account, session, verification.

// --- Accounts -----------------------------------------------------------------------------

func (s *Store) CreateAccount(ctx context.Context, a domain.Account) error {
	err := s.q.CreateAccount(ctx, sqlcdb.CreateAccountParams{
		ID: a.ID, UserID: a.UserID, ProviderID: a.ProviderID, AccountID: a.AccountID, Password: a.Password,
		AccessToken: a.AccessToken, RefreshToken: a.RefreshToken, IDToken: a.IDToken,
		AccessTokenExpiresAt:  formatOptTime(a.AccessTokenExpiresAt),
		RefreshTokenExpiresAt: formatOptTime(a.RefreshTokenExpiresAt),
		Scope:                 a.Scope,
		CreatedAt:             formatTime(a.CreatedAt), UpdatedAt: formatTime(a.UpdatedAt),
	})
	return wrap("create account", err)
}

// GetAccount finds the account of provider by the provider's own user id.
func (s *Store) GetAccount(ctx context.Context, providerID, accountID string) (domain.Account, error) {
	row, err := s.q.GetAccountByProvider(ctx, sqlcdb.GetAccountByProviderParams{ProviderID: providerID, AccountID: accountID})
	if err != nil {
		return domain.Account{}, wrap("get account", err)
	}
	return toAccount(row)
}

// GetCredentialAccount returns the user's email+password account; ErrNotFound for users who only
// ever signed in with an OAuth provider.
func (s *Store) GetCredentialAccount(ctx context.Context, userID string) (domain.Account, error) {
	row, err := s.q.GetCredentialAccountByUser(ctx, userID)
	if err != nil {
		return domain.Account{}, wrap("get credential account", err)
	}
	return toAccount(row)
}

// UpdatePasswordHash replaces the stored hash of one account (rehash on login).
func (s *Store) UpdatePasswordHash(ctx context.Context, accountID, hash string, at time.Time) error {
	n, err := s.q.UpdateAccountPassword(ctx, sqlcdb.UpdateAccountPasswordParams{
		Password: &hash, UpdatedAt: formatTime(at), ID: accountID,
	})
	if err != nil {
		return wrap("update password", err)
	}
	return wrapAffected("update password", n)
}

// ResetCredentialPassword sets a user's password after a successful reset and signs them out
// everywhere, atomically. If the user has no credential account yet (they only used GitHub),
// a.* describes the one to create. All sessions are deleted in the same transaction: whoever
// knew the OLD password, or held a stolen session, must not stay logged in after a reset.
func (s *Store) ResetCredentialPassword(ctx context.Context, a domain.Account) error {
	return s.WithTx(ctx, func(tx *Store) error {
		n, err := tx.q.UpdateCredentialPasswordByUser(ctx, sqlcdb.UpdateCredentialPasswordByUserParams{
			Password: a.Password, UpdatedAt: formatTime(a.UpdatedAt), UserID: a.UserID,
		})
		if err != nil {
			return wrap("reset password", err)
		}
		if n == 0 {
			if err := tx.CreateAccount(ctx, a); err != nil {
				return err
			}
		}
		_, err = tx.q.DeleteSessionsByUser(ctx, a.UserID)
		return wrap("reset password: revoke sessions", err)
	})
}

// UpdateAccountTokens refreshes the stored provider credentials of an OAuth account.
func (s *Store) UpdateAccountTokens(ctx context.Context, a domain.Account) error {
	n, err := s.q.UpdateAccountTokens(ctx, sqlcdb.UpdateAccountTokensParams{
		AccessToken: a.AccessToken, RefreshToken: a.RefreshToken, IDToken: a.IDToken,
		AccessTokenExpiresAt:  formatOptTime(a.AccessTokenExpiresAt),
		RefreshTokenExpiresAt: formatOptTime(a.RefreshTokenExpiresAt),
		Scope:                 a.Scope, UpdatedAt: formatTime(a.UpdatedAt), ID: a.ID,
	})
	if err != nil {
		return wrap("update account tokens", err)
	}
	return wrapAffected("update account tokens", n)
}

// --- Sessions -----------------------------------------------------------------------------

func (s *Store) CreateSession(ctx context.Context, sess domain.Session) error {
	err := s.q.CreateSession(ctx, sqlcdb.CreateSessionParams{
		ID: sess.ID, UserID: sess.UserID, TokenHash: sess.TokenHash, ExpiresAt: formatTime(sess.ExpiresAt),
		IpAddress: nullable(sess.IPAddress), UserAgent: nullable(sess.UserAgent),
		CreatedAt: formatTime(sess.CreatedAt), UpdatedAt: formatTime(sess.UpdatedAt),
	})
	return wrap("create session", err)
}

// GetSessionByTokenHash returns the session regardless of expiry; the service decides what an
// expired one means (and deletes it).
func (s *Store) GetSessionByTokenHash(ctx context.Context, tokenHash string) (domain.Session, error) {
	row, err := s.q.GetSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		return domain.Session{}, wrap("get session", err)
	}
	return toSession(row)
}

// ExtendSession pushes expires_at out (sliding expiry).
func (s *Store) ExtendSession(ctx context.Context, id string, expiresAt, at time.Time) error {
	n, err := s.q.ExtendSession(ctx, sqlcdb.ExtendSessionParams{ExpiresAt: formatTime(expiresAt), UpdatedAt: formatTime(at), ID: id})
	if err != nil {
		return wrap("extend session", err)
	}
	return wrapAffected("extend session", n)
}

// ListActiveSessions returns the user's unexpired sessions, newest first.
func (s *Store) ListActiveSessions(ctx context.Context, userID string, now time.Time) ([]domain.Session, error) {
	rows, err := s.q.ListActiveSessionsByUser(ctx, sqlcdb.ListActiveSessionsByUserParams{UserID: userID, Now: formatTime(now)})
	if err != nil {
		return nil, wrap("list sessions", err)
	}
	out := make([]domain.Session, 0, len(rows))
	for _, r := range rows {
		sess, err := toSession(r)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, nil
}

// DeleteSessionByTokenHash and DeleteSessionByID are idempotent: deleting nothing is success
// (sign-out twice, or two requests racing to delete the same expired session).
func (s *Store) DeleteSessionByTokenHash(ctx context.Context, tokenHash string) error {
	_, err := s.q.DeleteSessionByTokenHash(ctx, tokenHash)
	return wrap("delete session", err)
}

func (s *Store) DeleteSessionByID(ctx context.Context, id string) error {
	_, err := s.q.DeleteSessionByID(ctx, id)
	return wrap("delete session", err)
}

// DeleteUserSession deletes one of the USER'S sessions. A session id that belongs to someone else
// is indistinguishable from a missing one: domain.ErrNotFound.
func (s *Store) DeleteUserSession(ctx context.Context, userID, id string) error {
	n, err := s.q.DeleteUserSession(ctx, sqlcdb.DeleteUserSessionParams{ID: id, UserID: userID})
	if err != nil {
		return wrap("delete user session", err)
	}
	return wrapAffected("delete user session", n)
}

// DeleteOtherSessions deletes all of the user's sessions except keepID.
func (s *Store) DeleteOtherSessions(ctx context.Context, userID, keepID string) error {
	_, err := s.q.DeleteOtherSessions(ctx, sqlcdb.DeleteOtherSessionsParams{UserID: userID, ID: keepID})
	return wrap("delete other sessions", err)
}

func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	n, err := s.q.DeleteExpiredSessions(ctx, formatTime(now))
	return n, wrap("delete expired sessions", err)
}

// --- Verifications --------------------------------------------------------------------------

// ReplaceVerification stores v after deleting older tokens with the same identifier, so asking
// for a new reset email invalidates the previous link. One transaction: never zero valid tokens
// after a failure in between.
func (s *Store) ReplaceVerification(ctx context.Context, v domain.Verification) error {
	return s.WithTx(ctx, func(tx *Store) error {
		if err := tx.q.DeleteVerificationsByIdentifier(ctx, v.Identifier); err != nil {
			return wrap("replace verification", err)
		}
		return wrap("replace verification", tx.q.CreateVerification(ctx, sqlcdb.CreateVerificationParams{
			ID: v.ID, Identifier: v.Identifier, Value: v.Value, ExpiresAt: formatTime(v.ExpiresAt),
			CreatedAt: formatTime(v.CreatedAt), UpdatedAt: formatTime(v.UpdatedAt),
		}))
	})
}

// ConsumeVerification atomically finds, deletes and returns the identifier of an unexpired token
// of the given purpose whose stored value (the token's SHA-256) matches. domain.ErrNotFound when
// there is none: unknown, already used, expired, or wrong purpose.
func (s *Store) ConsumeVerification(ctx context.Context, purpose, valueHash string, now time.Time) (string, error) {
	id, err := s.q.ConsumeVerification(ctx, sqlcdb.ConsumeVerificationParams{
		Value: valueHash, IdentifierPattern: purpose + ":%", Now: formatTime(now),
	})
	if err != nil {
		return "", wrap("consume verification", err)
	}
	return id, nil
}

func (s *Store) DeleteExpiredVerifications(ctx context.Context, now time.Time) (int64, error) {
	n, err := s.q.DeleteExpiredVerifications(ctx, formatTime(now))
	return n, wrap("delete expired verifications", err)
}

// --- Row -> domain conversion -----------------------------------------------------------------

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// nullable stores "" as NULL.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func formatOptTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := formatTime(*t)
	return &s
}

func parseOptTime(s *string) (*time.Time, error) {
	if s == nil {
		return nil, nil
	}
	t, err := parseTime(*s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func toAccount(r sqlcdb.Account) (domain.Account, error) {
	created, err := parseTime(r.CreatedAt)
	if err != nil {
		return domain.Account{}, err
	}
	updated, err := parseTime(r.UpdatedAt)
	if err != nil {
		return domain.Account{}, err
	}
	accessExp, err := parseOptTime(r.AccessTokenExpiresAt)
	if err != nil {
		return domain.Account{}, err
	}
	refreshExp, err := parseOptTime(r.RefreshTokenExpiresAt)
	if err != nil {
		return domain.Account{}, err
	}
	return domain.Account{
		ID: r.ID, UserID: r.UserID, ProviderID: r.ProviderID, AccountID: r.AccountID, Password: r.Password,
		AccessToken: r.AccessToken, RefreshToken: r.RefreshToken, IDToken: r.IDToken,
		AccessTokenExpiresAt: accessExp, RefreshTokenExpiresAt: refreshExp, Scope: r.Scope,
		CreatedAt: created, UpdatedAt: updated,
	}, nil
}

func toSession(r sqlcdb.Session) (domain.Session, error) {
	expires, err := parseTime(r.ExpiresAt)
	if err != nil {
		return domain.Session{}, err
	}
	created, err := parseTime(r.CreatedAt)
	if err != nil {
		return domain.Session{}, err
	}
	updated, err := parseTime(r.UpdatedAt)
	if err != nil {
		return domain.Session{}, err
	}
	sess := domain.Session{
		ID: r.ID, UserID: r.UserID, TokenHash: r.TokenHash, ExpiresAt: expires,
		CreatedAt: created, UpdatedAt: updated,
	}
	if r.IpAddress != nil {
		sess.IPAddress = *r.IpAddress
	}
	if r.UserAgent != nil {
		sess.UserAgent = *r.UserAgent
	}
	return sess, nil
}
