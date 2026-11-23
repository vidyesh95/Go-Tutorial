package httpapi

import (
	"net/http"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/service"
)

func (s *server) listTasks(w http.ResponseWriter, r *http.Request) error {
	afterID, limit, err := pageParams(r)
	if err != nil {
		return err
	}
	params := service.ListTasksParams{AfterID: afterID, Limit: limit}
	if v := r.URL.Query().Get("status"); v != "" {
		st := domain.TaskStatus(v)
		params.Status = &st // validated by the service
	}
	page, err := s.Tasks.List(r.Context(), userIDFrom(r.Context()), r.PathValue("id"), params)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, newPageResponse(page, func(t domain.Task) string { return t.ID }))
	return nil
}

func (s *server) createTask(w http.ResponseWriter, r *http.Request) error {
	var in domain.CreateTaskInput
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	t, err := s.Tasks.Create(r.Context(), userIDFrom(r.Context()), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/tasks/"+t.ID)
	writeJSON(w, http.StatusCreated, t)
	return nil
}

func (s *server) getTask(w http.ResponseWriter, r *http.Request) error {
	t, err := s.Tasks.Get(r.Context(), userIDFrom(r.Context()), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, t)
	return nil
}

func (s *server) updateTask(w http.ResponseWriter, r *http.Request) error {
	var in domain.UpdateTaskInput
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	t, err := s.Tasks.Update(r.Context(), userIDFrom(r.Context()), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, t)
	return nil
}

func (s *server) deleteTask(w http.ResponseWriter, r *http.Request) error {
	if err := s.Tasks.Delete(r.Context(), userIDFrom(r.Context()), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
