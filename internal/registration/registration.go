// Package registration creates or adopts the GitHub runner scale set and
// acquires its message session.
package registration

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/actions/scaleset"
)

const (
	scaleSetExistsException = "RunnerScaleSetExistsException"
	sessionConflictSuffix   = "SessionConflictException"
)

// ScaleSetClient is the subset of the GitHub scale set client used to
// register the scale set.
type ScaleSetClient interface {
	GetRunnerScaleSet(context.Context, int, string) (*scaleset.RunnerScaleSet, error)
	CreateRunnerScaleSet(context.Context, *scaleset.RunnerScaleSet) (*scaleset.RunnerScaleSet, error)
	UpdateRunnerScaleSet(context.Context, int, *scaleset.RunnerScaleSet) (*scaleset.RunnerScaleSet, error)
}

// EnsureScaleSet returns the scale set named desired.Name in
// desired.RunnerGroupID, creating it when it does not exist. The returned
// bool reports whether this call created it. An existing scale set is
// returned as-is; call Reconcile once its message session is held.
func EnsureScaleSet(ctx context.Context, client ScaleSetClient, desired scaleset.RunnerScaleSet) (*scaleset.RunnerScaleSet, bool, error) {
	existing, err := client.GetRunnerScaleSet(ctx, desired.RunnerGroupID, desired.Name)
	if err != nil {
		return nil, false, fmt.Errorf("get runner scale set %q: %w", desired.Name, err)
	}
	if existing != nil {
		return existing, false, nil
	}
	create := desired
	created, err := client.CreateRunnerScaleSet(ctx, &create)
	if err == nil {
		return created, true, nil
	}
	if !IsScaleSetExists(err) {
		return nil, false, fmt.Errorf("create runner scale set %q: %w", desired.Name, err)
	}
	// Another process created it between the lookup and the create.
	existing, getErr := client.GetRunnerScaleSet(ctx, desired.RunnerGroupID, desired.Name)
	if getErr != nil {
		return nil, false, fmt.Errorf("get runner scale set %q after create conflict: %w", desired.Name, getErr)
	}
	if existing == nil {
		return nil, false, fmt.Errorf("create runner scale set %q: %w", desired.Name, err)
	}
	return existing, false, nil
}

// Reconcile updates an adopted scale set so its labels and runner settings
// match the desired configuration.
func Reconcile(ctx context.Context, client ScaleSetClient, id int, desired scaleset.RunnerScaleSet) (*scaleset.RunnerScaleSet, error) {
	update := desired
	update.ID = id
	updated, err := client.UpdateRunnerScaleSet(ctx, id, &update)
	if err != nil {
		return nil, fmt.Errorf("update runner scale set %d: %w", id, err)
	}
	return updated, nil
}

// SessionRetry bounds how long AcquireSession waits for a conflicting
// message session to expire.
type SessionRetry struct {
	Interval time.Duration
	Timeout  time.Duration
}

// AcquireSession opens the scale set message session, retrying while GitHub
// reports a session conflict. A conflict means either another controller
// owns the scale set or a session from an uncleanly stopped controller has
// not expired yet; only one controller may listen at a time.
func AcquireSession[T any](ctx context.Context, open func(context.Context) (T, error), retry SessionRetry, logger *slog.Logger) (T, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	deadline := time.Now().Add(retry.Timeout)
	for attempt := 1; ; attempt++ {
		session, err := open(ctx)
		if err == nil {
			return session, nil
		}
		var zero T
		if !IsSessionConflict(err) {
			return zero, err
		}
		if !time.Now().Add(retry.Interval).Before(deadline) {
			return zero, fmt.Errorf("another controller still holds the message session after %s; stop it or wait for its session to expire: %w", retry.Timeout, err)
		}
		logger.Warn("Message session is held by another controller or a previous run; retrying", "attempt", attempt, "retryIn", retry.Interval)
		timer := time.NewTimer(retry.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
		}
	}
}

// IsScaleSetExists reports whether err is GitHub's
// RunnerScaleSetExistsException. The scaleset client does not export a
// typed error for it, so the exception name is matched.
func IsScaleSetExists(err error) bool {
	return err != nil && strings.Contains(err.Error(), scaleSetExistsException)
}

// IsSessionConflict reports whether err is GitHub's
// TaskAgentSessionConflictException.
func IsSessionConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), sessionConflictSuffix)
}

var _ ScaleSetClient = (*scaleset.Client)(nil)
