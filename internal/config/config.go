package config

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/actions/scaleset"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	defaultRunnerVersion = "latest"
	defaultRunnerArch    = "x64"
)

var guestUserPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]*[$]?$`)

type HostMapping struct {
	Host string
	IP   string
}

type Config struct {
	RegistrationURL        string
	ScaleSetName           string
	RunnerGroup            string
	Labels                 []string
	Token                  string
	TokenFile              string
	Kubeconfig             string
	KubeconfigBase64       string
	KubeContext            string
	Namespace              string
	RunnerImage            string
	RunnerImageDisplayName string
	Network                string
	KeyPair                string
	SSHUsername            string
	ExtraHosts             []string
	HostMappings           []HostMapping
	ExtraCACertFiles       []string
	TrustedCACerts         []string
	MinRunners             int
	MaxRunners             int
	CPU                    int
	Memory                 string
	DiskSize               string
	RunnerVersion          string
	RunnerArch             string
	RunnerUser             string
	RunnerHome             string
	SkipRunnerInstall      bool
	VMCreateTimeout        time.Duration
	VMDeleteTimeout        time.Duration
	ReconcileInterval      time.Duration
	MaxParallelCreate      int
	LogLevel               string
	LogFormat              string
	ValidateOnly           bool
}

func BindFlags(cmd *cobra.Command, c *Config) {
	f := cmd.Flags()
	f.StringVar(&c.RegistrationURL, "url", "", "GitHub repository, organization, or enterprise URL")
	f.StringVar(&c.ScaleSetName, "name", "", "GitHub runner scale set name")
	f.StringVar(&c.RunnerGroup, "runner-group", "", "GitHub runner group name")
	f.StringSliceVar(&c.Labels, "labels", nil, "Runner labels (comma-separated or repeated)")
	f.StringVar(&c.TokenFile, "token-file", "", "File containing the GitHub PAT")
	f.StringVar(&c.Kubeconfig, "kubeconfig", "", "Harvester kubeconfig file")
	f.StringVar(&c.KubeContext, "kube-context", "", "Kubeconfig context")
	f.StringVar(&c.Namespace, "namespace", "", "Namespace for runner VMs")
	f.StringVar(&c.RunnerImage, "runner-image", "", "Harvester image resource name, optionally namespace/name")
	f.StringVar(&c.RunnerImageDisplayName, "runner-image-display-name", "", "Exact Harvester image spec.displayName, optionally namespace/name")
	f.StringVar(&c.Network, "network", "", "Harvester VM network resource name or namespace/name")
	f.StringVar(&c.KeyPair, "ssh-key", "", "Existing Harvester SSH keypair as namespace/name")
	f.StringVar(&c.SSHUsername, "ssh-username", "", "Guest OS user to receive the keypair public key (defaults to runner-user)")
	f.StringSliceVar(&c.ExtraHosts, "extra-host", nil, "Additional runner /etc/hosts entry as HOST=IP (comma-separated or repeated)")
	f.StringSliceVar(&c.ExtraCACertFiles, "extra-ca-cert-file", nil, "PEM certificate file to trust in runner VMs (comma-separated or repeated)")
	f.IntVar(&c.MinRunners, "min-runners", -1, "Minimum runner count")
	f.IntVar(&c.MaxRunners, "max-runners", -1, "Maximum runner count")
	f.IntVar(&c.CPU, "cpu", -1, "vCPUs per runner VM")
	f.StringVar(&c.Memory, "memory", "", "Memory per runner VM")
	f.StringVar(&c.DiskSize, "disk-size", "", "Root disk size per runner VM")
	f.StringVar(&c.RunnerVersion, "runner-version", "", "Actions runner version, or latest")
	f.StringVar(&c.RunnerArch, "runner-arch", "", "Actions runner architecture: x64, arm64, arm, or x86")
	f.StringVar(&c.RunnerUser, "runner-user", "", "Guest OS user for the runner")
	f.StringVar(&c.RunnerHome, "runner-home", "", "Guest installation directory")
	f.BoolVar(&c.SkipRunnerInstall, "skip-runner-install", false, "Use a runner-ready image and skip installing the Actions runner in cloud-init")
	f.DurationVar(&c.VMCreateTimeout, "vm-create-timeout", 0, "Deadline before a provisioning VM is replaced")
	f.DurationVar(&c.VMDeleteTimeout, "vm-delete-timeout", 0, "VM cleanup timeout")
	f.DurationVar(&c.ReconcileInterval, "reconcile-interval", 0, "Harvester reconciliation interval")
	f.IntVar(&c.MaxParallelCreate, "max-parallel-creates", -1, "Maximum concurrent VM API creates")
	f.StringVar(&c.LogLevel, "log-level", "", "Log level: debug, info, warn, or error")
	f.StringVar(&c.LogFormat, "log-format", "", "Log format: text or json")
	f.BoolVar(&c.ValidateOnly, "validate-only", false, "Validate configured GitHub and Harvester inputs without creating resources")
}

func (c *Config) Complete() error {
	c.RegistrationURL = value(c.RegistrationURL, "GITHUB_CONFIG_URL", "")
	c.ScaleSetName = value(c.ScaleSetName, "GITHUB_SCALESET_NAME", "")
	c.RunnerGroup = value(c.RunnerGroup, "GITHUB_RUNNER_GROUP", scaleset.DefaultRunnerGroup)
	if len(c.Labels) == 0 {
		if labels := os.Getenv("GITHUB_RUNNER_LABELS"); labels != "" {
			c.Labels = strings.Split(labels, ",")
		}
	}
	c.Token = os.Getenv("GITHUB_TOKEN")
	c.TokenFile = value(c.TokenFile, "GITHUB_TOKEN_FILE", "")
	c.Kubeconfig = value(c.Kubeconfig, "KUBECONFIG", "")
	c.KubeconfigBase64 = os.Getenv("HARVESTER_KUBECONFIG_B64")
	c.KubeContext = value(c.KubeContext, "HARVESTER_KUBE_CONTEXT", "")
	c.Namespace = value(c.Namespace, "HARVESTER_NAMESPACE", "default")
	c.RunnerImage = value(c.RunnerImage, "HARVESTER_RUNNER_IMAGE", "")
	c.RunnerImageDisplayName = value(c.RunnerImageDisplayName, "HARVESTER_RUNNER_IMAGE_DISPLAY_NAME", "")
	c.Network = value(c.Network, "HARVESTER_NETWORK", "")
	c.KeyPair = value(c.KeyPair, "HARVESTER_KEYPAIR", "")
	if len(c.ExtraHosts) == 0 {
		c.ExtraHosts = envList("HARVESTER_EXTRA_HOSTS")
	}
	if len(c.ExtraCACertFiles) == 0 {
		c.ExtraCACertFiles = envList("HARVESTER_EXTRA_CA_CERT_FILES")
	}
	c.Memory = value(c.Memory, "HARVESTER_MEMORY", "4Gi")
	c.DiskSize = value(c.DiskSize, "HARVESTER_DISK_SIZE", "20Gi")
	c.RunnerVersion = strings.TrimPrefix(value(c.RunnerVersion, "ACTIONS_RUNNER_VERSION", defaultRunnerVersion), "v")
	c.RunnerArch = value(c.RunnerArch, "ACTIONS_RUNNER_ARCH", defaultRunnerArch)
	c.RunnerUser = value(c.RunnerUser, "HARVESTER_RUNNER_USER", "runner")
	c.SSHUsername = value(c.SSHUsername, "HARVESTER_SSH_USERNAME", c.RunnerUser)
	c.RunnerHome = value(c.RunnerHome, "HARVESTER_RUNNER_HOME", "/opt/actions-runner")
	if !c.SkipRunnerInstall {
		skipRunnerInstall, err := envBool("HARVESTER_SKIP_RUNNER_INSTALL", false)
		if err != nil {
			return err
		}
		c.SkipRunnerInstall = skipRunnerInstall
	}
	c.LogLevel = value(c.LogLevel, "LOG_LEVEL", "info")
	c.LogFormat = value(c.LogFormat, "LOG_FORMAT", "text")

	var err error
	if c.MinRunners == -1 {
		c.MinRunners, err = envInt("MIN_RUNNERS", 0)
		if err != nil {
			return err
		}
	}
	if c.MaxRunners == -1 {
		c.MaxRunners, err = envInt("MAX_RUNNERS", 10)
		if err != nil {
			return err
		}
	}
	if c.CPU == -1 {
		c.CPU, err = envInt("HARVESTER_CPU", 2)
		if err != nil {
			return err
		}
	}
	if c.MaxParallelCreate == -1 {
		c.MaxParallelCreate, err = envInt("MAX_PARALLEL_CREATES", 5)
		if err != nil {
			return err
		}
	}
	if c.VMCreateTimeout == 0 {
		c.VMCreateTimeout, err = envDuration("VM_CREATE_TIMEOUT", 10*time.Minute)
		if err != nil {
			return err
		}
	}
	if c.VMDeleteTimeout == 0 {
		c.VMDeleteTimeout, err = envDuration("VM_DELETE_TIMEOUT", 5*time.Minute)
		if err != nil {
			return err
		}
	}
	if c.ReconcileInterval == 0 {
		c.ReconcileInterval, err = envDuration("RECONCILE_INTERVAL", 15*time.Second)
		if err != nil {
			return err
		}
	}

	if c.TokenFile != "" {
		contents, readErr := os.ReadFile(c.TokenFile)
		if readErr != nil {
			return fmt.Errorf("read GitHub token file: %w", readErr)
		}
		c.Token = strings.TrimSpace(string(contents))
	}
	if err := c.Validate(); err != nil {
		return err
	}
	c.HostMappings, err = parseHostMappings(c.ExtraHosts)
	if err != nil {
		return err
	}
	c.TrustedCACerts, err = loadTrustedCACerts(c.ExtraCACertFiles)
	return err
}

func (c Config) Validate() error {
	if _, err := url.ParseRequestURI(c.RegistrationURL); err != nil || !strings.HasPrefix(c.RegistrationURL, "https://") {
		return errors.New("--url must be a valid HTTPS GitHub registration URL")
	}
	if c.ScaleSetName == "" {
		return errors.New("--name is required")
	}
	if c.Token == "" {
		return errors.New("GitHub PAT is required through --token-file, GITHUB_TOKEN_FILE, or GITHUB_TOKEN")
	}
	if c.Kubeconfig == "" && c.KubeconfigBase64 == "" {
		return errors.New("Harvester kubeconfig is required through --kubeconfig, KUBECONFIG, or HARVESTER_KUBECONFIG_B64")
	}
	if c.RunnerImage == "" && c.RunnerImageDisplayName == "" {
		return errors.New("either --runner-image or --runner-image-display-name is required")
	}
	if c.RunnerImage != "" && c.RunnerImageDisplayName != "" {
		return errors.New("--runner-image and --runner-image-display-name cannot be used together")
	}
	if c.Network == "" {
		return errors.New("--network is required")
	}
	if c.MinRunners < 0 || c.MaxRunners < c.MinRunners {
		return errors.New("runner counts must satisfy 0 <= min-runners <= max-runners")
	}
	if c.CPU < 1 || c.MaxParallelCreate < 1 {
		return errors.New("cpu and max-parallel-creates must be positive")
	}
	if c.VMCreateTimeout <= 0 || c.VMDeleteTimeout <= 0 || c.ReconcileInterval <= 0 {
		return errors.New("timeouts and reconcile-interval must be positive")
	}
	mem, err := resource.ParseQuantity(c.Memory)
	if err != nil || mem.Sign() <= 0 {
		return fmt.Errorf("invalid memory quantity %q", c.Memory)
	}
	disk, err := resource.ParseQuantity(c.DiskSize)
	if err != nil || disk.Sign() <= 0 {
		return fmt.Errorf("invalid disk-size quantity %q", c.DiskSize)
	}
	for _, name := range []string{c.Namespace, c.RunnerUser} {
		if strings.TrimSpace(name) == "" {
			return errors.New("namespace and runner-user cannot be empty")
		}
	}
	if !guestUserPattern.MatchString(c.RunnerUser) {
		return fmt.Errorf("runner-user %q is not a valid Linux account name", c.RunnerUser)
	}
	if c.KeyPair != "" {
		parts := strings.Split(c.KeyPair, "/")
		if len(parts) != 2 || len(validation.IsDNS1123Label(parts[0])) != 0 || len(validation.IsDNS1123Subdomain(parts[1])) != 0 {
			return fmt.Errorf("keypair %q must be namespace/name", c.KeyPair)
		}
		if !guestUserPattern.MatchString(c.SSHUsername) {
			return fmt.Errorf("ssh-username %q is not a valid Linux account name", c.SSHUsername)
		}
		if c.SSHUsername == "root" {
			return errors.New("ssh-username cannot be root")
		}
	}
	if !strings.HasPrefix(c.RunnerHome, "/") {
		return errors.New("runner-home must be an absolute path")
	}
	switch c.RunnerArch {
	case "x64", "arm64", "arm", "x86":
	default:
		return fmt.Errorf("unsupported runner architecture %q", c.RunnerArch)
	}
	for i, label := range c.Labels {
		if strings.TrimSpace(label) == "" {
			return fmt.Errorf("runner label at index %d is empty", i)
		}
	}
	if _, err := parseHostMappings(c.ExtraHosts); err != nil {
		return err
	}
	for _, path := range c.ExtraCACertFiles {
		if strings.TrimSpace(path) == "" {
			return errors.New("extra-ca-cert-file path cannot be empty")
		}
	}
	return nil
}

func parseHostMappings(specs []string) ([]HostMapping, error) {
	mappings := make([]HostMapping, 0, len(specs))
	seen := make(map[string]bool, len(specs))
	for _, spec := range specs {
		host, ip, ok := strings.Cut(spec, "=")
		host = strings.ToLower(strings.TrimSpace(host))
		ip = strings.TrimSpace(ip)
		if !ok || len(validation.IsDNS1123Subdomain(host)) != 0 {
			return nil, fmt.Errorf("extra-host %q must be HOST=IP with a valid DNS hostname", spec)
		}
		addr, err := netip.ParseAddr(ip)
		if err != nil || addr.Zone() != "" {
			return nil, fmt.Errorf("extra-host %q must contain a valid IPv4 or IPv6 address", spec)
		}
		if seen[host] {
			return nil, fmt.Errorf("extra-host hostname %q is duplicated", host)
		}
		seen[host] = true
		mappings = append(mappings, HostMapping{Host: host, IP: addr.String()})
	}
	return mappings, nil
}

func loadTrustedCACerts(paths []string) ([]string, error) {
	var certs []string
	for _, path := range paths {
		data, err := os.ReadFile(strings.TrimSpace(path))
		if err != nil {
			return nil, fmt.Errorf("read extra CA certificate file %q: %w", path, err)
		}
		count := 0
		for len(bytes.TrimSpace(data)) > 0 {
			data = bytes.TrimSpace(data)
			if !bytes.HasPrefix(data, []byte("-----BEGIN CERTIFICATE-----")) {
				return nil, fmt.Errorf("extra CA certificate file %q contains non-certificate data", path)
			}
			block, rest := pem.Decode(data)
			if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
				return nil, fmt.Errorf("extra CA certificate file %q contains invalid certificate PEM", path)
			}
			if _, err := x509.ParseCertificate(block.Bytes); err != nil {
				return nil, fmt.Errorf("extra CA certificate file %q contains invalid X.509 certificate: %w", path, err)
			}
			certs = append(certs, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes})))
			count++
			data = rest
		}
		if count == 0 {
			return nil, fmt.Errorf("extra CA certificate file %q contains no certificates", path)
		}
	}
	return certs, nil
}

func envList(name string) []string {
	if value := os.Getenv(name); value != "" {
		return strings.Split(value, ",")
	}
	return nil
}

func (c Config) KubeconfigValue() string {
	if c.KubeconfigBase64 != "" {
		return c.KubeconfigBase64
	}
	return c.Kubeconfig
}

func (c Config) ScaleSetLabels() []scaleset.Label {
	labels := []scaleset.Label{{Name: "self-hosted"}}
	if len(c.Labels) == 0 {
		c.Labels = []string{c.ScaleSetName}
	}
	for _, label := range c.Labels {
		name := strings.TrimSpace(label)
		if strings.EqualFold(name, "self-hosted") {
			continue
		}
		labels = append(labels, scaleset.Label{Name: name})
	}
	return labels
}

func (c Config) Logger() *slog.Logger {
	var level slog.Level
	switch strings.ToLower(c.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(c.LogFormat, "json") {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

func value(current, envName, fallback string) string {
	if current != "" {
		return current
	}
	if env := os.Getenv(envName); env != "" {
		return env
	}
	return fallback
}

func envInt(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return n, nil
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return d, nil
}

func envBool(name string, fallback bool) (bool, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", name, err)
	}
	return b, nil
}
