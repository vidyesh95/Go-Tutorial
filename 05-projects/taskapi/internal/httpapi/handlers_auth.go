package httpapi

import (
	"net/http"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
)

func (s *server) signup(w http.ResponseWriter, r *http.Request) error {
	var in domain.SignupInput
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	user, err := s.Auth.Signup(r.Context(), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, user)
	return nil
}

func (s *server) login(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	res, err := s.Auth.Login(r.Context(), in.Email, in.Password)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

func (s *server) me(w http.ResponseWriter, r *http.Request) error {
	user, err := s.Auth.GetUser(r.Context(), userIDFrom(r.Context()))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, user)
	return nil
}
