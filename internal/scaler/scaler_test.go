package scaler

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/actions/scaleset"

	"github.com/bk201-org/harvester-scaleset/internal/harvester"
)

type fakeGitHub struct{}

func (fakeGitHub) GenerateJitRunnerConfig(_ context.Context, setting *scaleset.RunnerScaleSetJitRunnerSetting, _ int) (*scaleset.RunnerScaleSetJitRunnerConfig, error) {
	return &scaleset.RunnerScaleSetJitRunnerConfig{EncodedJITConfig: "jit-" + setting.Name}, nil
}

type fakeBackend struct {
	mu      sync.Mutex
	runners map[string]harvester.RunnerResource
	created []harvester.RunnerSpec
	deleted []string
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{runners: make(map[string]harvester.RunnerResource)}
}

func (f *fakeBackend) CreateRunner(_ context.Context, spec harvester.RunnerSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, spec)
	f.runners[spec.Name] = harvester.RunnerResource{Name: spec.Name, CreatedAt: time.Now(), Phase: "Starting"}
	return nil
}

func (f *fakeBackend) DeleteRunner(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, name)
	delete(f.runners, name)
	return nil
}

func (f *fakeBackend) ListRunners(_ context.Context, _ int) ([]harvester.RunnerResource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]harvester.RunnerResource, 0, len(f.runners))
	for _, runner := range f.runners {
		result = append(result, runner)
	}
	return result, nil
}

func (f *fakeBackend) DeleteAll(_ context.Context, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	clear(f.runners)
	return nil
}

func TestDesiredCountHonorsMinAndMax(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	s := newTestScaler(t, backend, 1, 2)

	count, err := s.HandleDesiredRunnerCount(context.Background(), 0)
	if err != nil || count != 1 {
		t.Fatalf("initial count = %d, err = %v", count, err)
	}
	count, err = s.HandleDesiredRunnerCount(context.Background(), 99)
	if err != nil || count != 2 {
		t.Fatalf("capped count = %d, err = %v", count, err)
	}
	if len(backend.created) != 2 {
		t.Fatalf("created = %d, want 2", len(backend.created))
	}
}

func TestJobLifecycleDeletesVM(t *testing.T) {
	t.Parallel()
	backend := newFakeBackend()
	s := newTestScaler(t, backend, 0, 1)
	if _, err := s.HandleDesiredRunnerCount(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	name := backend.created[0].Name
	if err := s.HandleJobStarted(context.Background(), &scaleset.JobStarted{RunnerName: name}); err != nil {
		t.Fatal(err)
	}
	if err := s.HandleJobCompleted(context.Background(), &scaleset.JobCompleted{RunnerName: name}); err != nil {
		t.Fatal(err)
	}
	if len(backend.deleted) != 1 || backend.deleted[0] != name {
		t.Fatalf("deleted = %v", backend.deleted)
	}
}

func TestUnknownRunnerDoesNotPanic(t *testing.T) {
	t.Parallel()
	s := newTestScaler(t, newFakeBackend(), 0, 1)
	if err := s.HandleJobStarted(context.Background(), &scaleset.JobStarted{RunnerName: "missing"}); err == nil {
		t.Fatal("expected unknown runner error")
	}
	if err := s.HandleJobCompleted(context.Background(), &scaleset.JobCompleted{RunnerName: "missing"}); err != nil {
		t.Fatalf("idempotent completion failed: %v", err)
	}
}

func newTestScaler(t *testing.T, backend harvester.Backend, minRunners, maxRunners int) *Scaler {
	t.Helper()
	s, err := New(Config{
		Backend:       backend,
		GitHub:        fakeGitHub{},
		ScaleSetID:    42,
		ScaleSetName:  fmt.Sprintf("test-%d", maxRunners),
		MinRunners:    minRunners,
		MaxRunners:    maxRunners,
		Parallel:      2,
		CreateTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
