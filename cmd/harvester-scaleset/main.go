package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/bk201-org/harvester-scaleset/internal/config"
	"github.com/bk201-org/harvester-scaleset/internal/harvester"
	"github.com/bk201-org/harvester-scaleset/internal/release"
	"github.com/bk201-org/harvester-scaleset/internal/scaler"
)

var (
	version   = "dev"
	commitSHA = "unknown"
)

func main() {
	var cfg config.Config
	cmd := &cobra.Command{
		Use:   "harvester-scaleset",
		Short: "Provision ephemeral GitHub Actions scale set runners as Harvester VMs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.Complete(); err != nil {
				return fmt.Errorf("invalid configuration: %w", err)
			}
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return run(ctx, cfg)
		},
	}
	config.BindFlags(cmd, &cfg)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config) (runErr error) {
	logger := cfg.Logger()
	var artifact release.Artifact
	if cfg.SkipRunnerInstall {
		artifact.Version = "preinstalled"
		logger.Info("Using preinstalled Actions runner from the Harvester image")
	} else {
		var err error
		artifact, err = (release.Resolver{Client: &http.Client{Timeout: 30 * time.Second}, Token: cfg.Token}).Resolve(ctx, cfg.RunnerVersion, cfg.RunnerArch)
		if err != nil {
			return err
		}
		logger.Info("Resolved Actions runner release", "version", artifact.Version, "architecture", cfg.RunnerArch)
	}

	harvesterClient, err := harvester.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("create Harvester client: %w", err)
	}
	controllerID := uuid.NewString()[:12]
	backend, err := harvester.NewManager(harvesterClient, cfg, artifact, controllerID, logger.WithGroup("harvester"))
	if err != nil {
		return err
	}
	if err := backend.Preflight(ctx); err != nil {
		return fmt.Errorf("Harvester preflight: %w", err)
	}
	if cfg.ValidateOnly {
		logger.Info("Validation succeeded; no resources were created")
		return nil
	}

	githubClient, err := scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{
		GitHubConfigURL:     cfg.RegistrationURL,
		PersonalAccessToken: cfg.Token,
		SystemInfo:          systemInfo(0),
	})
	if err != nil {
		return fmt.Errorf("create GitHub scale set client: %w", err)
	}
	runnerGroupID := 1
	if cfg.RunnerGroup != scaleset.DefaultRunnerGroup {
		group, err := githubClient.GetRunnerGroupByName(ctx, cfg.RunnerGroup)
		if err != nil {
			return fmt.Errorf("resolve runner group %q: %w", cfg.RunnerGroup, err)
		}
		runnerGroupID = group.ID
	}
	createdScaleSet, err := githubClient.CreateRunnerScaleSet(ctx, &scaleset.RunnerScaleSet{
		Name:          cfg.ScaleSetName,
		RunnerGroupID: runnerGroupID,
		Labels:        cfg.ScaleSetLabels(),
		RunnerSetting: scaleset.RunnerSetting{DisableUpdate: true},
	})
	if err != nil {
		return fmt.Errorf("create GitHub runner scale set: %w", err)
	}
	githubClient.SetSystemInfo(systemInfo(createdScaleSet.ID))
	logger.Info("Created GitHub runner scale set", "scaleSetID", createdScaleSet.ID, "name", createdScaleSet.Name)

	var (
		scaleSetScaler *scaler.Scaler
		closeSession   func(context.Context) error
	)
	defer func() {
		if scaleSetScaler != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), cfg.VMDeleteTimeout)
			if err := scaleSetScaler.Shutdown(cleanupCtx); err != nil {
				logger.Error("Failed to clean up runner VMs", "error", err)
				runErr = errors.Join(runErr, fmt.Errorf("clean up runner VMs: %w", err))
			}
			cancel()
		}
		if closeSession != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := closeSession(closeCtx); err != nil {
				logger.Error("Failed to close GitHub message session", "error", err)
				runErr = errors.Join(runErr, fmt.Errorf("close GitHub message session: %w", err))
			}
			cancel()
		}
		deleteCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := githubClient.DeleteRunnerScaleSet(deleteCtx, createdScaleSet.ID); err != nil {
			logger.Error("Failed to delete GitHub runner scale set", "scaleSetID", createdScaleSet.ID, "error", err)
			runErr = errors.Join(runErr, fmt.Errorf("delete GitHub runner scale set: %w", err))
		}
	}()

	hostname, err := os.Hostname()
	if err != nil {
		hostname = controllerID
	}
	session, err := githubClient.MessageSessionClient(ctx, createdScaleSet.ID, hostname)
	if err != nil {
		return fmt.Errorf("create GitHub message session: %w", err)
	}
	closeSession = session.Close

	scaleSetScaler, err = scaler.New(scaler.Config{
		Backend:       backend,
		GitHub:        githubClient,
		ScaleSetID:    createdScaleSet.ID,
		ScaleSetName:  cfg.ScaleSetName,
		MinRunners:    cfg.MinRunners,
		MaxRunners:    cfg.MaxRunners,
		Parallel:      cfg.MaxParallelCreate,
		CreateTimeout: cfg.VMCreateTimeout,
		Logger:        logger.WithGroup("scaler"),
	})
	if err != nil {
		return err
	}
	go scaleSetScaler.RunReconciler(ctx, cfg.ReconcileInterval)

	messageListener, err := listener.New(session, listener.Config{
		ScaleSetID: createdScaleSet.ID,
		MaxRunners: cfg.MaxRunners,
		Logger:     logger.WithGroup("listener"),
	})
	if err != nil {
		return fmt.Errorf("create GitHub message listener: %w", err)
	}
	logger.Info("Starting GitHub runner scale set listener", slog.Int("scaleSetID", createdScaleSet.ID))
	if err := messageListener.Run(ctx, scaleSetScaler); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run GitHub message listener: %w", err)
	}
	return nil
}

func systemInfo(scaleSetID int) scaleset.SystemInfo {
	return scaleset.SystemInfo{
		System:     "harvester-scaleset",
		Subsystem:  "harvester-scaleset",
		Version:    version,
		CommitSHA:  commitSHA,
		ScaleSetID: scaleSetID,
	}
}
