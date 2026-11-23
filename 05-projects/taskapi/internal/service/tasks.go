package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
)

// TaskService manages tasks. Tasks have no owner of their own; the owner is whoever owns the
// project, and every method proves that before touching anything.
type TaskService struct {
	projects ProjectRepo
	tasks    TaskRepo
	now      clock
	newID    idGen
}

func NewTaskService(projects ProjectRepo, tasks TaskRepo) *TaskService {
	return &TaskService{projects: projects, tasks: tasks, now: defaultClock, newID: newID}
}

// ListTasksParams are the optional filters and paging controls for listing tasks.
type ListTasksParams struct {
	Status  *domain.TaskStatus // nil = any
	AfterID string             // cursor: return tasks with id > AfterID
	Limit   int                // 0 = default
}

// Create adds a task to a project the caller owns.
func (s *TaskService) Create(ctx context.Context, ownerID, projectID string, in domain.CreateTaskInput) (domain.Task, error) {
	if err := in.Validate(); err != nil {
		return domain.Task{}, err
	}
	// Cheap checks first: reject bad input without spending a query.
	if _, err := s.projects.GetProject(ctx, ownerID, projectID); err != nil {
		return domain.Task{}, fmt.Errorf("create task: %w", err)
	}
	priority := domain.DefaultPriority
	if in.Priority != nil {
		priority = *in.Priority
	}
	now := s.now()
	t := domain.Task{
		ID: s.newID(), ProjectID: projectID, Title: strings.TrimSpace(in.Title),
		Status: domain.StatusTodo, Priority: priority, DueDate: in.DueDate,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.tasks.CreateTask(ctx, t); err != nil {
		return domain.Task{}, fmt.Errorf("create task: %w", err)
	}
	return t, nil
}

func (s *TaskService) Get(ctx context.Context, ownerID, id string) (domain.Task, error) {
	t, err := s.tasks.GetTask(ctx, ownerID, id)
	if err != nil {
		return domain.Task{}, fmt.Errorf("get task: %w", err)
	}
	return t, nil
}

// List returns one page of a project's tasks, optionally filtered by status.
func (s *TaskService) List(ctx context.Context, ownerID, projectID string, p ListTasksParams) (domain.Page[domain.Task], error) {
	if p.Status != nil && !p.Status.Valid() {
		return domain.Page[domain.Task]{}, domain.NewValidationError("status", "must be one of todo, doing, done")
	}
	if _, err := s.projects.GetProject(ctx, ownerID, projectID); err != nil {
		return domain.Page[domain.Task]{}, fmt.Errorf("list tasks: %w", err)
	}
	limit := domain.NormalizeLimit(p.Limit)
	rows, err := s.tasks.ListTasks(ctx, projectID, p.Status, p.AfterID, limit+1)
	if err != nil {
		return domain.Page[domain.Task]{}, fmt.Errorf("list tasks: %w", err)
	}
	return paginate(rows, limit), nil
}

// Update applies a partial update (PATCH semantics): only the fields present in in change.
func (s *TaskService) Update(ctx context.Context, ownerID, id string, in domain.UpdateTaskInput) (domain.Task, error) {
	if err := in.Validate(); err != nil {
		return domain.Task{}, err
	}
	if in.Title != nil {
		trimmed := strings.TrimSpace(*in.Title)
		in.Title = &trimmed
	}
	now := s.now()
	t, err := s.tasks.UpdateTask(ctx, ownerID, id, func(t *domain.Task) error {
		in.Apply(t, now)
		return nil
	})
	if err != nil {
		return domain.Task{}, fmt.Errorf("update task: %w", err)
	}
	return t, nil
}

func (s *TaskService) Delete(ctx context.Context, ownerID, id string) error {
	if err := s.tasks.DeleteTask(ctx, ownerID, id); err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	return nil
}
