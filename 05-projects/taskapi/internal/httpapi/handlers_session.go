package httpapi

import (
	"net/http"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/service"
)

// Handlers of the cookie-session ("Better Auth-style") mode, mounted under /v1/session-auth/.
// Endpoint names follow Better Auth's API. They are as thin as the rest of the handlers: decode,
// call SessionAuthService, set a cookie, encode.

// sessionBody is the response of sign-up, sign-in, OAuth callback and get-session. The session
// token is deliberately absent: it travels ONLY in the HttpOnly cookie, out of reach of scripts.
type sessionBody struct {
	User    domain.User    `json:"user"`
	Session domain.Session `json:"session"`
}

// statusBody is the response of endpoints that only report success.
type statusBody struct {
	Status bool `json:"status"`
}

func clientMeta(r *http.Request) service.ClientMeta {
	return service.ClientMeta{IP: clientIP(r), UserAgent: r.UserAgent()}
}

// sessionOnly extracts the cookie session behind the request. Endpoints about "the current
// session" (list, revoke...) make no sense for a Bearer JWT, which has no server-side session.
func sessionOnly(r *http.Request) (authInfo, error) {
	info := authFrom(r.Context())
	if info.Method != authSession {
		return authInfo{}, domain.ErrUnauthorized
	}
	return info, nil
}

func (s *server) signUpEmail(w http.ResponseWriter, r *http.Request) error {
	var in domain.SignupInput
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	res, err := s.SessionAuth.SignUp(r.Context(), in, clientMeta(r))
	if err != nil {
		return err
	}
	s.cookies.set(w, res.Token)
	writeJSON(w, http.StatusCreated, sessionBody{res.User, res.Session})
	return nil
}

func (s *server) signInEmail(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	res, err := s.SessionAuth.SignIn(r.Context(), in.Email, in.Password, clientMeta(r))
	if err != nil {
		return err
	}
	s.cookies.set(w, res.Token)
	writeJSON(w, http.StatusOK, sessionBody{res.User, res.Session})
	return nil
}

// signOut ends the session behind the cookie and clears the cookie. It does not require a VALID
// session: a client holding a stale cookie must still be able to clean up.
func (s *server) signOut(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(s.cookies.Name); err == nil {
		if err := s.SessionAuth.SignOut(r.Context(), c.Value); err != nil {
			return err
		}
	}
	s.cookies.clear(w)
	writeJSON(w, http.StatusOK, statusBody{true})
	return nil
}

func (s *server) getSession(w http.ResponseWriter, r *http.Request) error {
	info, err := sessionOnly(r)
	if err != nil {
		return err
	}
	user, err := s.Auth.GetUser(r.Context(), info.UserID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, sessionBody{user, info.Session})
	return nil
}

func (s *server) listSessions(w http.ResponseWriter, r *http.Request) error {
	info, err := sessionOnly(r)
	if err != nil {
		return err
	}
	list, err := s.SessionAuth.ListSessions(r.Context(), info.UserID)
	if err != nil {
		return err
	}
	type item struct {
		domain.Session
		Current bool `json:"current"`
	}
	out := make([]item, 0, len(list))
	for _, sess := range list {
		out = append(out, item{sess, sess.ID == info.Session.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
	return nil
}

func (s *server) revokeSession(w http.ResponseWriter, r *http.Request) error {
	info, err := sessionOnly(r)
	if err != nil {
		return err
	}
	var in struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	if err := s.SessionAuth.RevokeSession(r.Context(), info.UserID, in.ID); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, statusBody{true})
	return nil
}

func (s *server) revokeOtherSessions(w http.ResponseWriter, r *http.Request) error {
	info, err := sessionOnly(r)
	if err != nil {
		return err
	}
	if err := s.SessionAuth.RevokeOtherSessions(r.Context(), info.UserID, info.Session.ID); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, statusBody{true})
	return nil
}

// sendVerificationEmail works with either authentication mode: any signed-in user may ask.
func (s *server) sendVerificationEmail(w http.ResponseWriter, r *http.Request) error {
	if err := s.SessionAuth.SendVerificationEmail(r.Context(), userIDFrom(r.Context())); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, statusBody{true})
	return nil
}

// verifyEmail is a GET because the user arrives by clicking a link in an email. The token in the
// query string is single-use; the access log records only the path, never the query.
func (s *server) verifyEmail(w http.ResponseWriter, r *http.Request) error {
	if err := s.SessionAuth.VerifyEmail(r.Context(), r.URL.Query().Get("token")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, statusBody{true})
	return nil
}

// forgetPassword always answers 200 for a well-formed email, whether or not the account exists.
func (s *server) forgetPassword(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	if err := s.SessionAuth.ForgetPassword(r.Context(), in.Email); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, statusBody{true})
	return nil
}

func (s *server) resetPassword(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	if err := s.SessionAuth.ResetPassword(r.Context(), in.Token, in.NewPassword); err != nil {
		return err
	}
	// Every session of this user was just deleted, including the one in this browser (if any).
	s.cookies.clear(w)
	writeJSON(w, http.StatusOK, statusBody{true})
	return nil
}
