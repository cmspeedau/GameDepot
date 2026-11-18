package tasks

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// blockFn returns a task body that waits until the test releases it, so "running"
// state is deterministic rather than a race against a sleep.
func blockFn(release <-chan struct{}) func(context.Context, *Task) error {
	return func(ctx context.Context, t *Task) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func waitFor(t *testing.T, r *Registry, id uuid.UUID, state State) *Task {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if task, ok := r.Get(id); ok && task.View().State == state {
			return task
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("task %s never reached %s", id, state)
	return nil
}

func TestStartRunsAndCompletes(t *testing.T) {
	r := New()
	release := make(chan struct{})
	task := r.Start("publish", uuid.Nil, blockFn(release))
	if task.View().State != StateRunning {
		t.Fatalf("state = %s, want running immediately", task.View().State)
	}
	close(release)
	waitFor(t, r, task.ID, StateDone)
}

func TestFailureIsRecorded(t *testing.T) {
	r := New()
	task := r.Start("publish", uuid.Nil, func(ctx context.Context, task *Task) error {
		return errors.New("boom")
	})
	got := waitFor(t, r, task.ID, StateFailed)
	if got.View().Error != "boom" {
		t.Fatalf("error = %q", got.View().Error)
	}
}

func TestStartForDeduplicatesSameTarget(t *testing.T) {
	r := New()
	release := make(chan struct{})
	first := r.StartFor("ingest", "gog:1711765822:windows", uuid.Nil, blockFn(release))
	startedSecond := false
	second := r.StartFor("ingest", "gog:1711765822:windows", uuid.Nil, func(ctx context.Context, task *Task) error {
		startedSecond = true
		return nil
	})
	if first.ID != second.ID {
		t.Fatalf("got two tasks %s and %s for the same kind and target", first.ID, second.ID)
	}
	close(release)
	time.Sleep(20 * time.Millisecond)
	if startedSecond {
		t.Fatal("the duplicate request started a second run")
	}
}

func TestStartForSeparatesDifferentTargets(t *testing.T) {
	r := New()
	release := make(chan struct{})
	a := r.StartFor("ingest", "gog:1", uuid.Nil, blockFn(release))
	b := r.StartFor("ingest", "gog:2", uuid.Nil, blockFn(release))
	if a.ID == b.ID {
		t.Fatal("different products must not share a task")
	}
	c := r.StartFor("publish", "gog:1", uuid.Nil, blockFn(release))
	if c.ID == a.ID {
		t.Fatal("the same target under a different kind is a different job")
	}
	close(release)
}

func TestStartForEmptyTargetNeverDeduplicates(t *testing.T) {
	r := New()
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	body := func(ctx context.Context, task *Task) error {
		wg.Done()
		<-release
		return nil
	}
	a := r.StartFor("integrity", "", uuid.Nil, body)
	b := r.StartFor("integrity", "", uuid.Nil, body)
	if a.ID == b.ID {
		t.Fatal("an empty target means no dedupe")
	}
	// Both bodies are running; release them so the goroutines finish.
	release <- struct{}{}
	release <- struct{}{}
	close(release)
	wg.Wait()
}

func TestCompletedTaskIsNotReused(t *testing.T) {
	r := New()
	first := r.StartFor("ingest", "gog:1", uuid.Nil, func(ctx context.Context, task *Task) error { return nil })
	waitFor(t, r, first.ID, StateDone)
	second := r.StartFor("ingest", "gog:1", uuid.Nil, func(ctx context.Context, task *Task) error { return nil })
	if second.ID == first.ID {
		t.Fatal("a finished task must not swallow a new request")
	}
}

func TestProgressAndLogAreSynchronized(t *testing.T) {
	r := New()
	task := r.Start("ingest", uuid.Nil, func(ctx context.Context, task *Task) error {
		var wg sync.WaitGroup
		for i := 1; i <= 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				task.SetProgress(float64(i) / 8)
				task.AppendLog("line")
			}(i)
		}
		wg.Wait()
		return nil
	})
	got := waitFor(t, r, task.ID, StateDone)
	if got.View().Progress != 1 {
		t.Fatalf("progress = %v, want Done() to settle at 1", got.View().Progress)
	}
	if lines := len(splitLines(got.View().Log)); lines != 8 {
		t.Fatalf("log has %d lines, want 8", lines)
	}
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
