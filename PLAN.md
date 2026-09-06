# Harvester Runner Scale Set Implementation Plan

## Goal

Build a Go service that uses `github.com/actions/scaleset` to create and listen to a GitHub Actions runner scale set, while provisioning each ephemeral runner as a Harvester virtual machine instead of a Docker container.

In this document, **scale-set administrator** means the human operator who supplies the GitHub and Harvester configuration and runs the service.

The first implementation will be a single active controller process. It will create one GitHub runner scale set, respond to desired-runner-count and job lifecycle messages, and own only the Harvester resources that it labels as belonging to that scale set.

## What the source establishes

The Docker example in `github.com/actions/scaleset/examples/dockerscaleset` provides the control-plane lifecycle to preserve:

1. Validate configuration and create a PAT-authenticated `scaleset.Client`.
2. Resolve the runner group and create the GitHub runner scale set with updates disabled.
3. Open a message session and run `listener.Run` with a `listener.Scaler` implementation.
4. In `HandleDesiredRunnerCount`, maintain `minRunners + assignedJobs`, capped by `maxRunners`, and generate one JIT runner configuration per new runner.
5. Mark a runner busy on `HandleJobStarted`.
6. Delete its compute resource on `HandleJobCompleted`.
7. On graceful shutdown, remove owned compute resources and then remove the GitHub runner scale set.

Harvester changes the implementation details because VM and disk creation are asynchronous and can partially fail. The scaler therefore needs idempotent resource operations, Kubernetes labels for ownership, readiness/error observation, bounded parallel creation, and rollback of partial creations.

The Terraform provider source also establishes reusable patterns:

- `github.com/harvester/terraform-provider-harvester/pkg/client.NewClient` demonstrates accepting either a kubeconfig file path or base64 kubeconfig plus an optional context. The service will preserve that input behavior in its own small client adapter.
- The provider's VM resource is under `internal/`, so it cannot be imported by this repository. Its behavior can still be followed using the public `github.com/harvester/harvester/pkg/builder.VMBuilder` and typed clientsets.
- A boot disk cloned from a Harvester image is described through a PVC volume-claim template using the image's namespace/name and storage class.
- A secondary Harvester network is a namespaced `NetworkAttachmentDefinition` connected through a bridge interface.
- Per-VM cloud-init can refer to a Kubernetes Secret whose `userdata` key contains the cloud-config document.
- Foreground VM deletion and auto-delete annotations are needed to avoid leaving cloned runner disks behind.

## Administrator contract

### Required inputs

| Input | CLI | Environment | Notes |
|---|---|---|---|
| GitHub registration URL | `--url` | `GITHUB_CONFIG_URL` | Repository, organization, or enterprise URL. |
| Scale set name | `--name` | `GITHUB_SCALESET_NAME` | Unique within the runner group. |
| GitHub PAT | `--token-file` | `GITHUB_TOKEN` | Prefer a token file or environment variable so the PAT is not exposed in the process list. The PAT stays in the controller and is never passed to a runner VM. |
| Harvester kubeconfig | `--kubeconfig` | `KUBECONFIG` | File path; also accept base64 content through `HARVESTER_KUBECONFIG_B64`. |
| Runner image | `--runner-image` | `HARVESTER_RUNNER_IMAGE` | Harvester VM image resource name, or `namespace/name`. |
| VM network | `--network` | `HARVESTER_NETWORK` | Harvester VM network/NAD name, or `namespace/name`. |

CLI values take precedence over environment values. Secrets must be redacted from validation errors and logs.

### Optional inputs and proposed defaults

| Input | Default |
|---|---|
| `--namespace` / `HARVESTER_NAMESPACE` | `default` |
| `--kube-context` / `HARVESTER_KUBE_CONTEXT` | kubeconfig current context |
| `--runner-group` | `default` |
| `--labels` | scale set name |
| `--min-runners` | `0` |
| `--max-runners` | `10` |
| `--cpu` | `2` |
| `--memory` | `4Gi` |
| `--disk-size` | `20Gi`, and never smaller than the source image's requirement |
| `--runner-version` | latest stable release resolved at controller startup |
| `--runner-user` | `runner` |
| `--runner-home` | `/opt/actions-runner` |
| `--skip-runner-install` / `HARVESTER_SKIP_RUNNER_INSTALL` | `false`; set for a runner-ready image |
| `--vm-create-timeout` | `10m` |
| `--vm-delete-timeout` | `5m` |
| `--max-parallel-creates` | `5` |
| `--log-level` | `info` |
| `--log-format` | `text` |

Validate quantities, positive capacities, `max-runners >= min-runners`, namespaced names, readable credentials, and URL syntax before creating any external resources.

### Runner image contract

The MVP expects a Linux, cloud-init-capable Harvester image with:

- a supported package manager for installing runner dependencies;
- `systemd`;
- `curl` and `tar`, or the ability to install them; and
- DHCP on the supplied VM network; and
- outbound HTTPS and DNS access to the configured GitHub host and any endpoints needed by jobs.

By default, cloud-init creates the runner user, installs OS dependencies, downloads and verifies the configured GitHub Actions runner release for the guest architecture, extracts it under `/opt/actions-runner`, writes the one-time JIT configuration to a root-readable environment file, creates/starts a systemd unit, and removes the environment file after the runner exits. With `--skip-runner-install`, cloud-init requires the user and executable runner to exist in the image and performs only JIT injection and service startup. The controller resolves `latest` once at startup for default-mode VMs and skips release resolution for runner-ready images.

The bootstrap script will initially support the Linux distributions/package managers exercised by validation. Unsupported images fail clearly in cloud-init output instead of attempting an unsafe partial installation.

## Proposed repository structure

```text
.
├── cmd/harvester-scaleset/main.go       # Cobra command, signals, dependency wiring
├── internal/config/config.go            # flags/env loading, defaults, validation
├── internal/github/client.go            # PAT scaleset client and scale-set lifecycle
├── internal/harvester/client.go         # client construction and startup preflight
├── internal/harvester/resources.go      # image/network lookup, VM/Secret build/create/delete
├── internal/harvester/cloudinit.go      # safe cloud-config rendering
├── internal/scaler/scaler.go            # listener.Scaler implementation
├── internal/scaler/state.go             # concurrency-safe runner/VM state and reconciliation
├── internal/scaler/reconcile.go         # discovery and cleanup of labeled resources
├── internal/logging/logging.go
├── go.mod
├── go.sum
├── README.md
└── *_test.go
```

Use narrow local interfaces around GitHub and Harvester operations. This keeps the scaler testable without a real cluster and avoids spreading generated client types across the codebase.

## Resource model

Each runner gets:

1. A unique DNS-safe name such as `<scale-set>-<random-suffix>`.
2. A Kubernetes Secret containing only that runner's cloud-init `userdata`, including its JIT configuration. Do not put the administrator PAT in this Secret.
3. A KubeVirt `VirtualMachine` with:
   - `RunStrategy: Once`, so a completed/crashed ephemeral runner is not restarted with an expired or already-used JIT configuration;
   - configured CPU and memory;
   - a bootable root PVC cloned from the selected Harvester image;
   - the selected Harvester network through a bridge/Multus interface;
   - a NoCloud disk referencing the per-runner Secret; and
   - foreground deletion plus the Harvester disk auto-delete annotation.

Apply the same ownership labels to the Secret, VM, VMI template, and generated PVC metadata where possible:

```text
app.kubernetes.io/name=harvester-scaleset
app.kubernetes.io/managed-by=harvester-scaleset
actions.github.com/scale-set-name=<name>
actions.github.com/scale-set-id=<numeric-id>
actions.github.com/runner-name=<runner-name>
actions.github.com/controller-id=<startup-session-id>
```

The labels provide durable discovery after process restarts and prevent cleanup from touching administrator-created VMs. Add a VM owner reference to the Secret after VM creation where Kubernetes ownership rules allow it; explicit label-based deletion remains the fallback.

## Startup and preflight

Before creating the GitHub scale set:

1. Load the kubeconfig, build clients, and verify API access.
2. Confirm the namespace exists.
3. Resolve the image by resource name; if desired later, add an explicit `--runner-image-display-name` mode rather than ambiguously treating names as display names.
4. Require the image to be ready and capture its storage class.
5. Resolve the network attachment definition in its namespace.
6. Unless runner installation is disabled, resolve the requested runner version, architecture-specific download URL, and published checksum before accepting jobs.
7. Create the GitHub scale set, session, listener, and scaler.

Avoid a Terraform subprocess and Terraform state. This is a dynamic controller, so it should reproduce the provider's resource shapes with a narrow Kubernetes dynamic-client adapter. Importing the provider client directly would pull in every generated Harvester client and depend on `replace` directives that are intentionally local to the provider module.

## Scaling lifecycle

### Scale up

`HandleDesiredRunnerCount(count)` will:

1. Reconcile the in-memory view with owned VMs from Harvester.
2. Compute `target = min(maxRunners, minRunners + count)`.
3. Reserve the missing runner slots under a mutex so overlapping callbacks cannot over-provision.
4. Create missing runners with a bounded worker group.
5. For each runner:
   - generate the GitHub JIT config;
   - render and create its cloud-init Secret;
   - build and create the VM;
   - patch ownership if needed; and
   - record the API-accepted VM as provisioning.
6. On any partial failure, delete that runner's VM, generated PVC, and Secret idempotently. Return the actual managed count and an aggregated error.

The returned managed count includes provisioning VMs, which prevents repeated desired-count messages from over-provisioning. A background reconciler observes VM/VMI readiness and boot deadlines, moves healthy runners to idle, and removes failed/timed-out VMs so a subsequent desired-count message can replace them. The listener callback must not wait several minutes for guests to boot. API object creation should still be bounded and concurrent, with context cancellation honored throughout.

### Job events

- `HandleJobStarted` moves the named runner from idle/provisioning to busy. Unknown runners return a descriptive error rather than panic.
- `HandleJobCompleted` removes the runner from state and foreground-deletes its VM. Deletion is idempotent; `NotFound` is success. Then delete any remaining Secret/PVC bearing that runner's ownership labels.
- No speculative scale-down is needed for ordinary job completions, matching the source listener semantics. A reconciliation pass handles failed VMs, timed-out provisioning, or externally deleted resources.

### Restart and reconciliation

At startup and before count decisions, list only resources bearing this controller's managed-by and scale-set-name labels.

- Delete stale resources that belong to the same configured scale-set name but a previous GitHub scale-set ID/session; their JIT configurations cannot safely be reused.
- Adopt resources for the current ID into provisioning/idle state based on VM/VMI status.
- Garbage-collect orphan Secrets/PVCs after confirming no matching VM exists.
- Treat terminal failed VMs as unavailable, delete them, and allow the desired-count callback to replace them.

Only one controller should manage a given scale-set name. Leader election/high availability is out of scope for the MVP; startup should fail clearly if an active conflicting controller can be detected.

### Shutdown

On SIGINT/SIGTERM:

1. Stop accepting/listening for new work.
2. Cancel in-flight provisioning and wait for bounded cleanup.
3. Delete all VMs and auxiliary resources owned by this GitHub scale-set ID, including busy runners, using a non-cancelled timeout context.
4. Close the message session.
5. Delete the GitHub runner scale set created by this process.

Cleanup failures should be logged and included in the final error. Never use an unscoped namespace-wide delete.

## Security and operational behavior

- Keep the PAT only in controller memory and never include it in VM specs, Secrets, events, or logs.
- The VM receives only its short-lived JIT configuration. Store it in a Kubernetes Secret rather than inline in the VM object.
- Use structured logs with scale-set ID, runner name, VM namespace/name, lifecycle state, duration, and sanitized errors.
- Apply API timeouts, context cancellation, retry with jitter for transient conflicts/server errors, and idempotent `AlreadyExists`/`NotFound` handling.
- Use least-privilege Harvester/Kubernetes RBAC: read images and network attachment definitions; list/watch/create/get/delete VMs and VMIs; create/get/delete/patch Secrets; and inspect/delete the generated PVCs in the runner namespace.
- Document that anyone able to read the per-runner Secret or VM console during bootstrap can obtain the JIT configuration.
- Expose health/readiness endpoints only if the service is later intended to run as a long-lived Kubernetes workload; they are not required for the first CLI MVP.

## Implementation phases

### Phase 1: project and configuration

- Initialize the Go module and pin compatible versions of `github.com/actions/scaleset`, the Harvester provider client package, Harvester builder/types, Cobra, and test libraries.
- Add CLI/env precedence, PAT file loading, validation, logging, version metadata, signal handling, and unit tests.
- Write the README's administrator prerequisites and example invocation without real credentials.

### Phase 2: Harvester resource backend

- Build the kubeconfig client and read-only image/network preflight.
- Implement cloud-init rendering.
- Implement VM/Secret builders based on the Terraform provider's public client/builder patterns.
- Implement idempotent create, readiness observation, foreground delete, rollback, and label-scoped garbage collection.
- Cover object construction and error paths with fake clients.

### Phase 3: GitHub scale set and scaler

- Port the PAT client, runner-group, scale-set, message-session, and listener wiring from the Docker example.
- Implement concurrency-safe runner state without panic paths.
- Implement desired count, job start, job completion, bounded provisioning, and graceful cleanup.
- Add unit tests for min/max arithmetic, duplicate callbacks, partial failure, cancellation, unknown runners, and cleanup.

### Phase 4: integration and documentation

- Add a gated end-to-end test that creates a temporary GitHub scale set, dispatches a workflow, observes a Harvester VM run the job, and verifies VM/PVC/Secret cleanup.
- Add a Harvester-only smoke test for image clone, network attachment, cloud-init execution, and deletion.
- Document required PAT permissions, Harvester RBAC, image preparation, network/DNS requirements, all flags/env vars, troubleshooting, and the cleanup model.
- Verify `go test ./...`, static analysis, binary build, graceful interruption during provisioning, and interruption during a running job.

## Definition of done

- An administrator can start the binary using a PAT, GitHub URL/name, kubeconfig, Harvester image name, and network name.
- A queued workflow causes exactly the required number of VMs to be created, subject to min/max limits.
- Each VM registers once using its own JIT configuration and successfully executes a targeted workflow.
- Completion removes the corresponding VM, cloned disk/PVC, and cloud-init Secret.
- SIGINT/SIGTERM performs label-scoped cleanup and removes the GitHub scale set.
- Retries, duplicate messages, externally deleted VMs, partial creates, and process restart do not cause unbounded over-provisioning or deletion of unowned resources.
- Unit tests pass without a live cluster; real GitHub/Harvester tests are explicitly opt-in.

## Decisions needed before implementation

1. Confirm whether image and network inputs are Kubernetes resource names (recommended) or Harvester UI display names. If display names are required, the CLI should expose that choice explicitly and reject duplicates.
2. Confirm acceptable production defaults for VM CPU, memory, root disk size, and namespace. Validation is capped at two VMs with 1 GiB each because the supplied test cluster is capacity constrained.
3. Confirm whether graceful shutdown should delete busy runner VMs immediately (matching the Docker example) or drain active jobs before cleanup.
4. Confirm the supported Harvester version range and whether the first version must support GitHub Enterprise Server/custom CA certificates.
