package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
)

// ProjectService manages a user's projects. Every method takes the caller's ownerID, which comes
// from the verified JWT, never from the request body: a client cannot ask to act as someone else.
type ProjectService struct {
	projects ProjectRepo
	now      clock
	newID    idGen
}

func NewProjectService(projects ProjectRepo) *ProjectService {
	return &ProjectService{projects: projects, now: defaultClock, newID: newID}
}

func (s *ProjectService) Create(ctx context.Context, ownerID, name string) (domain.Project, error) {
	name = strings.TrimSpace(name)
	if err := domain.ValidateProjectName(name); err != nil {
		return domain.Project{}, err
	}
	now := s.now()
	p := domain.Project{ID: s.newID(), OwnerID: ownerID, Name: name, CreatedAt: now, UpdatedAt: now}
	if err := s.projects.CreateProject(ctx, p); err != nil {
		return domain.Project{}, fmt.Errorf("create project: %w", err)
	}
	return p, nil
}

func (s *ProjectService) Get(ctx context.Context, ownerID, id string) (domain.Project, error) {
	p, err := s.projects.GetProject(ctx, ownerID, id)
	if err != nil {
		return domain.Project{}, fmt.Errorf("get project: %w", err)
	}
	return p, nil
}

// List returns one page of the owner's projects after the cursor id afterID ("" = first page).
func (s *ProjectService) List(ctx context.Context, ownerID, afterID string, limit int) (domain.Page[domain.Project], error) {
	limit = domain.NormalizeLimit(limit)
	rows, err := s.projects.ListProjects(ctx, ownerID, afterID, limit+1)
	if err != nil {
		return domain.Page[domain.Project]{}, fmt.Errorf("list projects: %w", err)
	}
	return paginate(rows, limit), nil
}

func (s *ProjectService) Rename(ctx context.Context, ownerID, id, name string) (domain.Project, error) {
	name = strings.TrimSpace(name)
	if err := domain.ValidateProjectName(name); err != nil {
		return domain.Project{}, err
	}
	p, err := s.projects.RenameProject(ctx, ownerID, id, name, s.now())
	if err != nil {
		return domain.Project{}, fmt.Errorf("rename project: %w", err)
	}
	return p, nil
}

// Delete removes the project; its tasks go with it (ON DELETE CASCADE).
func (s *ProjectService) Delete(ctx context.Context, ownerID, id string) error {
	if err := s.projects.DeleteProject(ctx, ownerID, id); err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	return nil
}
