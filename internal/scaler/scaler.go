package scaler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
	"github.com/google/uuid"

	"github.com/bk201-org/harvester-scaleset/internal/harvester"
)

type JITClient interface {
	GenerateJitRunnerConfig(context.Context, *scaleset.RunnerScaleSetJitRunnerSetting, int) (*scaleset.RunnerScaleSetJitRunnerConfig, error)
}

type runner struct {
	createdAt time.Time
	state     string
}

type Scaler struct {
	opMu          sync.Mutex
	mu            sync.Mutex
	runners       map[string]runner
	backend       harvester.Backend
	github        JITClient
	scaleSetID    int
	scaleSetName  string
	minRunners    int
	maxRunners    int
	parallel      int
	createTimeout time.Duration
	logger        *slog.Logger
}

type Config struct {
	Backend       harvester.Backend
	GitHub        JITClient
	ScaleSetID    int
	ScaleSetName  string
	MinRunners    int
	MaxRunners    int
	Parallel      int
	CreateTimeout time.Duration
	Logger        *slog.Logger
}

func New(c Config) (*Scaler, error) {
	if c.Backend == nil || c.GitHub == nil {
		return nil, errors.New("Harvester backend and GitHub client are required")
	}
	if c.ScaleSetID <= 0 || c.MinRunners < 0 || c.MaxRunners < c.MinRunners || c.Parallel < 1 || c.CreateTimeout <= 0 {
		return nil, errors.New("invalid scaler configuration")
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	return &Scaler{
		runners:       make(map[string]runner),
		backend:       c.Backend,
		github:        c.GitHub,
		scaleSetID:    c.ScaleSetID,
		scaleSetName:  c.ScaleSetName,
		minRunners:    c.MinRunners,
		maxRunners:    c.MaxRunners,
		parallel:      c.Parallel,
		createTimeout: c.CreateTimeout,
		logger:        c.Logger,
	}, nil
}

func (s *Scaler) HandleDesiredRunnerCount(ctx context.Context, count int) (int, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := s.reconcile(ctx); err != nil {
		return s.count(), err
	}
	target := min(s.maxRunners, s.minRunners+count)
	current := s.count()
	if target <= current {
		return current, nil
	}
	needed := target - current
	s.logger.Info("Scaling up runners", "currentCount", current, "desiredCount", target, "scaleUp", needed)

	jobs := make(chan string)
	errCh := make(chan error, needed)
	workers := min(needed, s.parallel)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for name := range jobs {
				if err := s.startRunner(ctx, name); err != nil {
					s.remove(name)
					errCh <- err
				}
			}
		}()
	}
	for range needed {
		name := s.newRunnerName()
		s.add(name, "provisioning", time.Now())
		jobs <- name
	}
	close(jobs)
	wg.Wait()
	close(errCh)
	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	return s.count(), errors.Join(errs...)
}

func (s *Scaler) HandleJobStarted(_ context.Context, job *scaleset.JobStarted) error {
	s.logger.Info("Job started", "runner", job.RunnerName, "runnerRequestId", job.RunnerRequestID, "jobId", job.JobID)
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.runners[job.RunnerName]
	if !ok {
		return fmt.Errorf("job started on unknown runner %q", job.RunnerName)
	}
	state.state = "busy"
	s.runners[job.RunnerName] = state
	return nil
}

func (s *Scaler) HandleJobCompleted(ctx context.Context, job *scaleset.JobCompleted) error {
	s.logger.Info("Job completed", "runner", job.RunnerName, "runnerRequestId", job.RunnerRequestID, "jobId", job.JobID, "result", job.Result)
	if err := s.backend.DeleteRunner(ctx, job.RunnerName); err != nil {
		return fmt.Errorf("delete completed runner %s: %w", job.RunnerName, err)
	}
	s.remove(job.RunnerName)
	return nil
}

func (s *Scaler) RunReconciler(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.opMu.Lock()
			err := s.reconcile(ctx)
			s.opMu.Unlock()
			if err != nil && !errors.Is(err, context.Canceled) {
				s.logger.Error("Runner reconciliation failed", "error", err)
			}
		}
	}
}

func (s *Scaler) Shutdown(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.logger.Info("Deleting runner VMs", "count", s.count())
	if err := s.backend.DeleteAll(ctx, s.scaleSetID); err != nil {
		return err
	}
	s.mu.Lock()
	clear(s.runners)
	s.mu.Unlock()
	return nil
}

func (s *Scaler) startRunner(ctx context.Context, name string) error {
	jit, err := s.github.GenerateJitRunnerConfig(ctx, &scaleset.RunnerScaleSetJitRunnerSetting{Name: name, WorkFolder: "_work"}, s.scaleSetID)
	if err != nil {
		return fmt.Errorf("generate JIT config for %s: %w", name, err)
	}
	if jit == nil || jit.EncodedJITConfig == "" {
		return fmt.Errorf("generate JIT config for %s: empty response", name)
	}
	if err := s.backend.CreateRunner(ctx, harvester.RunnerSpec{Name: name, ScaleSetID: s.scaleSetID, JITConfig: jit.EncodedJITConfig}); err != nil {
		return fmt.Errorf("create runner %s: %w", name, err)
	}
	return nil
}

func (s *Scaler) reconcile(ctx context.Context) error {
	resources, err := s.backend.ListRunners(ctx, s.scaleSetID)
	if err != nil {
		return err
	}
	now := time.Now()
	seen := make(map[string]struct{}, len(resources))
	var errs []error
	for _, resource := range resources {
		seen[resource.Name] = struct{}{}
		if resource.Failed || (resource.Phase != "Ready" && !resource.CreatedAt.IsZero() && now.Sub(resource.CreatedAt) > s.createTimeout) {
			s.logger.Warn("Removing unhealthy runner VM", "runner", resource.Name, "phase", resource.Phase)
			if err := s.backend.DeleteRunner(ctx, resource.Name); err != nil {
				errs = append(errs, fmt.Errorf("delete unhealthy runner %s: %w", resource.Name, err))
				continue
			}
			s.remove(resource.Name)
			continue
		}
		s.mu.Lock()
		state, ok := s.runners[resource.Name]
		if !ok {
			state = runner{createdAt: resource.CreatedAt, state: "provisioning"}
		}
		if resource.Phase == "Ready" && state.state != "busy" {
			state.state = "idle"
		}
		s.runners[resource.Name] = state
		s.mu.Unlock()
	}
	s.mu.Lock()
	for name, state := range s.runners {
		if _, ok := seen[name]; !ok {
			if state.state != "provisioning" || now.Sub(state.createdAt) > s.createTimeout {
				delete(s.runners, name)
			}
		}
	}
	s.mu.Unlock()
	return errors.Join(errs...)
}

func (s *Scaler) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runners)
}

func (s *Scaler) add(name, state string, createdAt time.Time) {
	s.mu.Lock()
	s.runners[name] = runner{createdAt: createdAt, state: state}
	s.mu.Unlock()
}

func (s *Scaler) remove(name string) {
	s.mu.Lock()
	delete(s.runners, name)
	s.mu.Unlock()
}

var invalidName = regexp.MustCompile(`[^a-z0-9-]+`)

func (s *Scaler) newRunnerName() string {
	prefix := strings.ToLower(s.scaleSetName)
	prefix = invalidName.ReplaceAllString(prefix, "-")
	prefix = strings.Trim(prefix, "-")
	if prefix == "" {
		prefix = "runner"
	}
	if len(prefix) > 45 {
		prefix = strings.TrimRight(prefix[:45], "-")
	}
	return fmt.Sprintf("%s-%s", prefix, uuid.NewString()[:8])
}

var _ listener.Scaler = (*Scaler)(nil)
