package store

import (
	"errors"
	"testing"
	"time"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
)

func strp(s string) *string { return &s }

func TestAccounts(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	u := mustUser(t, s, "ada@example.com")

	cred, err := s.GetCredentialAccount(ctx, u.ID)
	if err != nil || cred.Password == nil || *cred.Password != "hash" || cred.ProviderID != domain.ProviderCredential || cred.AccountID != u.ID {
		t.Fatalf("credential account = %+v, %v", cred, err)
	}
	if err := s.UpdatePasswordHash(ctx, cred.ID, "new-hash", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	cred, _ = s.GetCredentialAccount(ctx, u.ID)
	if *cred.Password != "new-hash" || !cred.UpdatedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("after update: %+v", cred)
	}
	if err := s.UpdatePasswordHash(ctx, "nope", "x", now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("update unknown account: %v", err)
	}

	// A GitHub account links to the same user; (provider, account id) is unique.
	exp := now.Add(8 * time.Hour)
	gh := domain.Account{
		ID: id("a"), UserID: u.ID, ProviderID: domain.ProviderGitHub, AccountID: "4242",
		AccessToken: strp("gho_x"), AccessTokenExpiresAt: &exp, Scope: strp("read:user"),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateAccount(ctx, gh); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAccount(ctx, domain.ProviderGitHub, "4242")
	if err != nil || got.UserID != u.ID || *got.AccessToken != "gho_x" || got.AccessTokenExpiresAt == nil || !got.AccessTokenExpiresAt.Equal(exp) || got.Password != nil {
		t.Fatalf("github account = %+v, %v", got, err)
	}
	other := mustUser(t, s, "bob@example.com")
	dup := gh
	dup.ID, dup.UserID = id("a"), other.ID
	if err := s.CreateAccount(ctx, dup); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("same github identity on a second user: got %v, want ErrConflict", err)
	}
	gh.AccessToken, gh.AccessTokenExpiresAt = strp("gho_y"), nil
	if err := s.UpdateAccountTokens(ctx, gh); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAccount(ctx, domain.ProviderGitHub, "4242")
	if *got.AccessToken != "gho_y" || got.AccessTokenExpiresAt != nil {
		t.Fatalf("after token update: %+v", got)
	}
	if _, err := s.GetAccount(ctx, domain.ProviderGitHub, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing account: %v", err)
	}
}

func mustSession(t *testing.T, s *Store, userID, hash string, expires time.Time) domain.Session {
	t.Helper()
	sess := domain.Session{ID: id("s"), UserID: userID, TokenHash: hash, ExpiresAt: expires, IPAddress: "203.0.113.9", UserAgent: "test/1.0", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateSession(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestSessions(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	alice, bob := mustUser(t, s, "a@example.com"), mustUser(t, s, "b@example.com")

	s1 := mustSession(t, s, alice.ID, "hash-1", now.Add(time.Hour))
	s2 := mustSession(t, s, alice.ID, "hash-2", now.Add(2*time.Hour))
	expired := mustSession(t, s, alice.ID, "hash-old", now.Add(-time.Hour))
	bobs := mustSession(t, s, bob.ID, "hash-bob", now.Add(time.Hour))

	got, err := s.GetSessionByTokenHash(ctx, "hash-1")
	if err != nil || got != s1 {
		t.Fatalf("round trip:\n got %+v\nwant %+v (%v)", got, s1, err)
	}
	if err := s.CreateSession(ctx, domain.Session{ID: id("s"), UserID: alice.ID, TokenHash: "hash-1", ExpiresAt: now, CreatedAt: now, UpdatedAt: now}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate token hash: %v", err)
	}
	if _, err := s.GetSessionByTokenHash(ctx, "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown hash: %v", err)
	}

	// Active sessions exclude expired ones.
	list, err := s.ListActiveSessions(ctx, alice.ID, now)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %d sessions, %v", len(list), err)
	}

	// Extend (sliding expiry).
	if err := s.ExtendSession(ctx, s1.ID, now.Add(48*time.Hour), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSessionByTokenHash(ctx, "hash-1")
	if !got.ExpiresAt.Equal(now.Add(48*time.Hour)) || !got.UpdatedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("extended = %+v", got)
	}

	// A user can delete only their own sessions; someone else's looks like a missing one.
	if err := s.DeleteUserSession(ctx, alice.ID, bobs.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("alice deleting bob's session: %v", err)
	}
	if _, err := s.GetSessionByTokenHash(ctx, "hash-bob"); err != nil {
		t.Fatalf("bob's session was deleted by alice: %v", err)
	}

	// Expired cleanup removes only expired rows.
	n, err := s.DeleteExpiredSessions(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("DeleteExpiredSessions = %d, %v; want 1 (%s)", n, err, expired.ID)
	}

	// Delete others keeps exactly one (and never touches another user's).
	if err := s.DeleteOtherSessions(ctx, alice.ID, s2.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.ListActiveSessions(ctx, alice.ID, now); len(list) != 1 || list[0].ID != s2.ID {
		t.Fatalf("after delete-others: %+v", list)
	}
	if _, err := s.GetSessionByTokenHash(ctx, "hash-bob"); err != nil {
		t.Fatalf("delete-others touched another user: %v", err)
	}

	// Idempotent deletes.
	if err := s.DeleteSessionByTokenHash(ctx, "hash-2"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSessionByTokenHash(ctx, "hash-2"); err != nil {
		t.Fatalf("second delete must succeed: %v", err)
	}
	if err := s.DeleteSessionByID(ctx, "unknown"); err != nil {
		t.Fatal(err)
	}
}

func TestSessionsCascadeWithUser(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	u := mustUser(t, s, "a@example.com")
	mustSession(t, s, u.ID, "h", now.Add(time.Hour))
	if _, err := s.db.ExecContext(t.Context(), `DELETE FROM users WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSessionByTokenHash(t.Context(), "h"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("session survived its user: %v", err)
	}
	if _, err := s.GetCredentialAccount(t.Context(), u.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("account survived its user: %v", err)
	}
}

func TestResetCredentialPassword(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	u := mustUser(t, s, "a@example.com")
	other := mustUser(t, s, "b@example.com")
	mustSession(t, s, u.ID, "h1", now.Add(time.Hour))
	mustSession(t, s, u.ID, "h2", now.Add(time.Hour))
	mustSession(t, s, other.ID, "hb", now.Add(time.Hour))

	at := now.Add(time.Minute)
	if err := s.ResetCredentialPassword(ctx, domain.Account{ID: id("a"), UserID: u.ID, ProviderID: domain.ProviderCredential, AccountID: u.ID, Password: strp("fresh"), CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	cred, _ := s.GetCredentialAccount(ctx, u.ID)
	if *cred.Password != "fresh" {
		t.Fatalf("password = %v", *cred.Password)
	}
	for _, h := range []string{"h1", "h2"} {
		if _, err := s.GetSessionByTokenHash(ctx, h); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("session %s survived the reset: %v", h, err)
		}
	}
	if _, err := s.GetSessionByTokenHash(ctx, "hb"); err != nil {
		t.Errorf("reset revoked ANOTHER user's session: %v", err)
	}

	// A GitHub-only user gets a credential account created by the reset.
	gh := domain.User{ID: id("u"), Email: "gh@example.com", Name: "GH", EmailVerified: true, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateUserWithAccount(ctx, gh, domain.Account{ID: id("a"), UserID: gh.ID, ProviderID: domain.ProviderGitHub, AccountID: "77", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCredentialAccount(ctx, gh.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("github-only user has a credential account: %v", err)
	}
	if err := s.ResetCredentialPassword(ctx, domain.Account{ID: id("a"), UserID: gh.ID, ProviderID: domain.ProviderCredential, AccountID: gh.ID, Password: strp("pw-hash"), CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if cred, err := s.GetCredentialAccount(ctx, gh.ID); err != nil || *cred.Password != "pw-hash" {
		t.Fatalf("created credential = %+v, %v", cred, err)
	}
}

func TestVerifications(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	mk := func(identifier, value string, ttl time.Duration) domain.Verification {
		return domain.Verification{ID: id("v"), Identifier: identifier, Value: value, ExpiresAt: now.Add(ttl), CreatedAt: now, UpdatedAt: now}
	}

	if err := s.ReplaceVerification(ctx, mk("email-verification:u1", "val-1", time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A newer token for the same identifier invalidates the older one.
	if err := s.ReplaceVerification(ctx, mk("email-verification:u1", "val-2", time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeVerification(ctx, domain.PurposeEmailVerification, "val-1", now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("superseded token still works: %v", err)
	}

	// Wrong purpose cannot consume it, and does not burn it.
	if _, err := s.ConsumeVerification(ctx, domain.PurposePasswordReset, "val-2", now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-purpose use: %v", err)
	}
	idf, err := s.ConsumeVerification(ctx, domain.PurposeEmailVerification, "val-2", now)
	if err != nil || idf != "email-verification:u1" {
		t.Fatalf("consume = %q, %v", idf, err)
	}
	// Single use.
	if _, err := s.ConsumeVerification(ctx, domain.PurposeEmailVerification, "val-2", now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("token consumed twice: %v", err)
	}

	// Expired tokens are not consumable, and cleanup removes them.
	if err := s.ReplaceVerification(ctx, mk("password-reset:u1", "val-3", time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeVerification(ctx, domain.PurposePasswordReset, "val-3", now.Add(2*time.Minute)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expired token accepted: %v", err)
	}
	if n, err := s.DeleteExpiredVerifications(ctx, now.Add(2*time.Minute)); err != nil || n != 1 {
		t.Fatalf("DeleteExpiredVerifications = %d, %v", n, err)
	}
}

// Many goroutines race to use ONE token: exactly one may win. This is the property the
// single DELETE ... RETURNING statement exists to provide.
func TestConsumeVerificationIsSingleUseUnderRace(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	if err := s.ReplaceVerification(ctx, domain.Verification{ID: id("v"), Identifier: "password-reset:u1", Value: "v", ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	wins := make(chan bool, 16)
	for range 16 {
		go func() {
			_, err := s.ConsumeVerification(ctx, domain.PurposePasswordReset, "v", now)
			wins <- err == nil
		}()
	}
	won := 0
	for range 16 {
		if <-wins {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d goroutines consumed the same token, want exactly 1", won)
	}
}
