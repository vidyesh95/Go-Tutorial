package httpapi

import (
	"net/http"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
)

// projectInput is the body of POST /v1/projects and PATCH /v1/projects/{id}.
type projectInput struct {
	Name string `json:"name"`
}

func (s *server) listProjects(w http.ResponseWriter, r *http.Request) error {
	afterID, limit, err := pageParams(r)
	if err != nil {
		return err
	}
	page, err := s.Projects.List(r.Context(), userIDFrom(r.Context()), afterID, limit)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, newPageResponse(page, func(p domain.Project) string { return p.ID }))
	return nil
}

func (s *server) createProject(w http.ResponseWriter, r *http.Request) error {
	var in projectInput
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	p, err := s.Projects.Create(r.Context(), userIDFrom(r.Context()), in.Name)
	if err != nil {
		return err
	}
	// 201 + Location is the REST convention for "created; here is where to find it".
	w.Header().Set("Location", "/v1/projects/"+p.ID)
	writeJSON(w, http.StatusCreated, p)
	return nil
}

func (s *server) getProject(w http.ResponseWriter, r *http.Request) error {
	p, err := s.Projects.Get(r.Context(), userIDFrom(r.Context()), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, p)
	return nil
}

func (s *server) renameProject(w http.ResponseWriter, r *http.Request) error {
	var in projectInput
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	p, err := s.Projects.Rename(r.Context(), userIDFrom(r.Context()), r.PathValue("id"), in.Name)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, p)
	return nil
}

func (s *server) deleteProject(w http.ResponseWriter, r *http.Request) error {
	if err := s.Projects.Delete(r.Context(), userIDFrom(r.Context()), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
