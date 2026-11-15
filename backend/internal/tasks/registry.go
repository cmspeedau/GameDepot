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
	t := &Task{
		ID:        uuid.New(),
		Kind:      kind,
		State:     StateRunning,
		OwnerID:   owner,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	r.mu.Lock()
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
