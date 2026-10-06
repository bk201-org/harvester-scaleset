package registration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/actions/scaleset"
)

var (
	errExists   = errors.New(`request POST https://broker.actions.githubusercontent.com/rest/_apis/runtime/runnerscalesets failed(status="400 Bad Request"): unexpected status code: 400: GitHub.Actions.Runtime.WebApi.RunnerScaleSetExistsException, GitHub.Actions.Runtime.WebApi: The runner scale set already exists in runner group Default.`)
	errConflict = errors.New(`failed to create message session: failed to do the session request: request POST failed(status="409 Conflict"): unexpected status code: 409: GitHub.DistributedTask.WebApi.TaskAgentSessionConflictException: The scale set already has an active session`)
)

type fakeClient struct {
	gets      []*scaleset.RunnerScaleSet
	getCalls  int
	createErr error
	created   []scaleset.RunnerScaleSet
	updated   []scaleset.RunnerScaleSet
}

func (f *fakeClient) GetRunnerScaleSet(_ context.Context, _ int, _ string) (*scaleset.RunnerScaleSet, error) {
	result := f.gets[f.getCalls]
	f.getCalls++
	return result, nil
}

func (f *fakeClient) CreateRunnerScaleSet(_ context.Context, set *scaleset.RunnerScaleSet) (*scaleset.RunnerScaleSet, error) {
	f.created = append(f.created, *set)
	if f.createErr != nil {
		return nil, f.createErr
	}
	result := *set
	result.ID = 42
	return &result, nil
}

func (f *fakeClient) UpdateRunnerScaleSet(_ context.Context, id int, set *scaleset.RunnerScaleSet) (*scaleset.RunnerScaleSet, error) {
	f.updated = append(f.updated, *set)
	result := *set
	result.ID = id
	return &result, nil
}

func desired() scaleset.RunnerScaleSet {
	return scaleset.RunnerScaleSet{
		Name:          "harvester",
		RunnerGroupID: 1,
		Labels:        []scaleset.Label{{Name: "self-hosted"}, {Name: "harvester"}},
		RunnerSetting: scaleset.RunnerSetting{DisableUpdate: true},
	}
}

func TestEnsureScaleSetCreatesWhenMissing(t *testing.T) {
	client := &fakeClient{gets: []*scaleset.RunnerScaleSet{nil}}
	set, created, err := EnsureScaleSet(context.Background(), client, desired())
	if err != nil {
		t.Fatal(err)
	}
	if !created || set.ID != 42 || len(client.created) != 1 {
		t.Fatalf("created=%v set=%+v creates=%d", created, set, len(client.created))
	}
}

func TestEnsureScaleSetAdoptsExisting(t *testing.T) {
	client := &fakeClient{gets: []*scaleset.RunnerScaleSet{{ID: 7, Name: "harvester"}}}
	set, created, err := EnsureScaleSet(context.Background(), client, desired())
	if err != nil {
		t.Fatal(err)
	}
	if created || set.ID != 7 || len(client.created) != 0 || len(client.updated) != 0 {
		t.Fatalf("created=%v set=%+v creates=%d updates=%d", created, set, len(client.created), len(client.updated))
	}
}

func TestEnsureScaleSetAdoptsAfterCreateRace(t *testing.T) {
	client := &fakeClient{
		gets:      []*scaleset.RunnerScaleSet{nil, {ID: 9, Name: "harvester"}},
		createErr: errExists,
	}
	set, created, err := EnsureScaleSet(context.Background(), client, desired())
	if err != nil {
		t.Fatal(err)
	}
	if created || set.ID != 9 || client.getCalls != 2 {
		t.Fatalf("created=%v set=%+v gets=%d", created, set, client.getCalls)
	}
}

func TestEnsureScaleSetReturnsOtherCreateErrors(t *testing.T) {
	client := &fakeClient{gets: []*scaleset.RunnerScaleSet{nil}, createErr: errors.New("boom")}
	if _, _, err := EnsureScaleSet(context.Background(), client, desired()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if client.getCalls != 1 {
		t.Fatalf("gets = %d", client.getCalls)
	}
}

func TestReconcileUpdatesLabelsAndSettings(t *testing.T) {
	client := &fakeClient{}
	set, err := Reconcile(context.Background(), client, 7, desired())
	if err != nil {
		t.Fatal(err)
	}
	if set.ID != 7 || len(client.updated) != 1 {
		t.Fatalf("set=%+v updates=%d", set, len(client.updated))
	}
	got := client.updated[0]
	if got.ID != 7 || len(got.Labels) != 2 || !got.RunnerSetting.DisableUpdate {
		t.Fatalf("update = %+v", got)
	}
}

func TestAcquireSessionRetriesConflict(t *testing.T) {
	calls := 0
	session, err := AcquireSession(context.Background(), func(context.Context) (string, error) {
		calls++
		if calls < 3 {
			return "", errConflict
		}
		return "session", nil
	}, SessionRetry{Interval: time.Millisecond, Timeout: time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if session != "session" || calls != 3 {
		t.Fatalf("session=%q calls=%d", session, calls)
	}
}

func TestAcquireSessionGivesUpOnPersistentConflict(t *testing.T) {
	calls := 0
	_, err := AcquireSession(context.Background(), func(context.Context) (string, error) {
		calls++
		return "", errConflict
	}, SessionRetry{Interval: 5 * time.Millisecond, Timeout: 20 * time.Millisecond}, nil)
	if !IsSessionConflict(err) || !strings.Contains(err.Error(), "another controller") {
		t.Fatalf("err = %v", err)
	}
	if calls < 2 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestAcquireSessionDoesNotRetryOtherErrors(t *testing.T) {
	calls := 0
	_, err := AcquireSession(context.Background(), func(context.Context) (string, error) {
		calls++
		return "", errors.New("unauthorized")
	}, SessionRetry{Interval: time.Millisecond, Timeout: time.Second}, nil)
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestAcquireSessionStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, err := AcquireSession(ctx, func(context.Context) (string, error) {
		cancel()
		return "", errConflict
	}, SessionRetry{Interval: time.Hour, Timeout: 2 * time.Hour}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
