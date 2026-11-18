package tasks

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

type State string

const (
	StateQueued  State = "queued"
	StateRunning State = "running"
	StateDone    State = "done"
	StateFailed  State = "failed"
)

type Task struct {
	ID        uuid.UUID
	Kind      string
	Target    string
	State     State
	Progress  float64
	Log       string
	Error     string
	OwnerID   uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time

	mu sync.Mutex
}

func (t *Task) SetProgress(p float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Progress = p
	t.UpdatedAt = time.Now()
}

func (t *Task) AppendLog(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Log != "" {
		t.Log += "\n"
	}
	t.Log += s
	t.UpdatedAt = time.Now()
}

func (t *Task) Fail(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.State = StateFailed
	t.Error = err.Error()
	t.UpdatedAt = time.Now()
}

func (t *Task) Done() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.State = StateDone
	t.Progress = 1
	t.UpdatedAt = time.Now()
}

type View struct {
	ID        uuid.UUID
	Kind      string
	State     State
	Progress  float64
	Log       string
	Error     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (t *Task) View() View {
	t.mu.Lock()
	defer t.mu.Unlock()
	return View{
		ID:        t.ID,
		Kind:      t.Kind,
		State:     t.State,
		Progress:  t.Progress,
		Log:       t.Log,
		Error:     t.Error,
		CreatedAt: t.CreatedAt,
		UpdatedAt: t.UpdatedAt,
	}
}

type Registry struct {
	mu    sync.Mutex
	tasks map[uuid.UUID]*Task
}

func New() *Registry {
	return &Registry{tasks: make(map[uuid.UUID]*Task)}
}

func (r *Registry) Start(kind string, owner uuid.UUID, fn func(context.Context, *Task) error) *Task {
	return r.StartFor(kind, "", owner, fn)
}

// StartFor starts a task tagged with a target, and returns the already-running task
// when one for the same kind and target exists. A store ingest of a 40 GB title must
// not run twice because someone refreshed the dashboard, and the caller does not need
// to know whether it got a new task or the existing one: the redirect goes to the same
// task page either way. An empty target means "no dedupe".
func (r *Registry) StartFor(kind, target string, owner uuid.UUID, fn func(context.Context, *Task) error) *Task {
	r.mu.Lock()
	if target != "" {
		for _, existing := range r.tasks {
			if existing.Kind == kind && existing.Target == target && existing.State == StateRunning {
				r.mu.Unlock()
				return existing
			}
		}
	}
	t := &Task{
		ID:        uuid.New(),
		Kind:      kind,
		Target:    target,
		State:     StateRunning,
		OwnerID:   owner,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	r.tasks[t.ID] = t
	r.mu.Unlock()

	go func() {
		ctx := context.Background()
		if err := fn(ctx, t); err != nil {
			t.Fail(err)
			return
		}
		t.Done()
	}()
	return t
}

func (r *Registry) Get(id uuid.UUID) (*Task, bool) {
	r.mu.Lock()
	t, ok := r.tasks[id]
	r.mu.Unlock()
	return t, ok
}
