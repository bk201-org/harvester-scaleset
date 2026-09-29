package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/actions/scaleset"
	"github.com/spf13/cobra"
)

func TestValidate(t *testing.T) {
	t.Parallel()
	valid := Config{
		RegistrationURL:   "https://github.com/example/repo",
		ScaleSetName:      "test",
		Token:             "secret",
		Kubeconfig:        "/tmp/kubeconfig",
		Namespace:         "default",
		RunnerImage:       "ubuntu",
		Network:           "network",
		MinRunners:        0,
		MaxRunners:        2,
		CPU:               1,
		Memory:            "1Gi",
		DiskSize:          "10Gi",
		RunnerArch:        "x64",
		RunnerUser:        "runner",
		RunnerHome:        "/opt/actions-runner",
		VMCreateTimeout:   1,
		VMDeleteTimeout:   1,
		ReconcileInterval: 1,
		MaxParallelCreate: 1,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	withKeyPair := valid
	withKeyPair.KeyPair = "default/hvst-dev-pubkey"
	withKeyPair.SSHUsername = "ubuntu"
	if err := withKeyPair.Validate(); err != nil {
		t.Fatalf("valid keypair config rejected: %v", err)
	}
	byDisplayName := valid
	byDisplayName.RunnerImage = ""
	byDisplayName.RunnerImageDisplayName = "default/ubuntu.qcow2"
	if err := byDisplayName.Validate(); err != nil {
		t.Fatalf("valid display-name config rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"missing token", func(c *Config) { c.Token = "" }},
		{"missing image", func(c *Config) { c.RunnerImage = "" }},
		{"both image flags", func(c *Config) { c.RunnerImageDisplayName = "ubuntu.qcow2" }},
		{"invalid counts", func(c *Config) { c.MinRunners, c.MaxRunners = 2, 1 }},
		{"invalid memory", func(c *Config) { c.Memory = "lots" }},
		{"invalid user", func(c *Config) { c.RunnerUser = "root\nExecStart=/bin/sh" }},
		{"unqualified keypair", func(c *Config) { c.KeyPair = "hvst-dev-pubkey" }},
		{"empty keypair namespace", func(c *Config) { c.KeyPair = "/hvst-dev-pubkey" }},
		{"extra keypair path", func(c *Config) { c.KeyPair = "default/team/key" }},
		{"invalid keypair name", func(c *Config) { c.KeyPair = "default/Bad_Key" }},
		{"missing SSH username", func(c *Config) { c.KeyPair = "default/key"; c.SSHUsername = "" }},
		{"invalid SSH username", func(c *Config) { c.KeyPair = "default/key"; c.SSHUsername = "root\nhello" }},
		{"root SSH username", func(c *Config) { c.KeyPair = "default/key"; c.SSHUsername = "root" }},
		{"relative home", func(c *Config) { c.RunnerHome = "runner" }},
		{"unsupported arch", func(c *Config) { c.RunnerArch = "mips" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestScaleSetLabels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		config Config
		want   []scaleset.Label
	}{
		{
			name:   "default",
			config: Config{ScaleSetName: "harvester-runners"},
			want:   []scaleset.Label{{Name: "self-hosted"}, {Name: "harvester-runners"}},
		},
		{
			name:   "custom labels",
			config: Config{ScaleSetName: "harvester-runners", Labels: []string{"harvester-runners", "linux"}},
			want:   []scaleset.Label{{Name: "self-hosted"}, {Name: "harvester-runners"}, {Name: "linux"}},
		},
		{
			name:   "already configured",
			config: Config{Labels: []string{"harvester-runners", " Self-Hosted "}},
			want:   []scaleset.Label{{Name: "self-hosted"}, {Name: "harvester-runners"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.config.ScaleSetLabels(); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("scale set labels = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestEnvBool(t *testing.T) {
	t.Setenv("TEST_BOOLEAN", "true")
	got, err := envBool("TEST_BOOLEAN", false)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("expected true")
	}

	t.Setenv("TEST_BOOLEAN", "sometimes")
	if _, err := envBool("TEST_BOOLEAN", false); err == nil {
		t.Fatal("expected invalid boolean error")
	}
}

func completeTestConfig() Config {
	return Config{
		RegistrationURL: "https://github.com/example/repo",
		ScaleSetName:    "test", Kubeconfig: "/tmp/kubeconfig",
		RunnerImage: "ubuntu", Network: "network",
		MinRunners: -1, MaxRunners: -1, CPU: -1, MaxParallelCreate: -1,
	}
}

func testCertificatePEM(t *testing.T, serial int64) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestCompleteExtraInputsFromEnv(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	dir := t.TempDir()
	first := testCertificatePEM(t, 1)
	second := testCertificatePEM(t, 2)
	third := testCertificatePEM(t, 3)
	bundle := filepath.Join(dir, "bundle.pem")
	single := filepath.Join(dir, "single.pem")
	if err := os.WriteFile(bundle, []byte(first+second), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(single, []byte(third), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARVESTER_EXTRA_HOSTS", "git.internal=192.0.2.10,ipv6.internal=2001:db8::10")
	t.Setenv("HARVESTER_EXTRA_CA_CERT_FILES", bundle+","+single)
	cfg := completeTestConfig()
	if err := cfg.Complete(); err != nil {
		t.Fatal(err)
	}
	wantHosts := []HostMapping{{Host: "git.internal", IP: "192.0.2.10"}, {Host: "ipv6.internal", IP: "2001:db8::10"}}
	if !reflect.DeepEqual(cfg.HostMappings, wantHosts) {
		t.Fatalf("host mappings = %#v, want %#v", cfg.HostMappings, wantHosts)
	}
	if !reflect.DeepEqual(cfg.TrustedCACerts, []string{first, second, third}) {
		t.Fatalf("trusted CA certificate count = %d, want 3", len(cfg.TrustedCACerts))
	}
}

func TestExtraFlagsOverrideEnv(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	t.Setenv("HARVESTER_EXTRA_HOSTS", "invalid")
	t.Setenv("HARVESTER_EXTRA_CA_CERT_FILES", "/missing/cert.pem")
	certFile := filepath.Join(t.TempDir(), "ca.pem")
	cert := testCertificatePEM(t, 4)
	if err := os.WriteFile(certFile, []byte(cert), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := completeTestConfig()
	cmd := &cobra.Command{}
	BindFlags(cmd, &cfg)
	for _, value := range []string{"a.internal=192.0.2.1", "b.internal=2001:db8::1"} {
		if err := cmd.Flags().Set("extra-host", value); err != nil {
			t.Fatal(err)
		}
	}
	if err := cmd.Flags().Set("extra-ca-cert-file", certFile); err != nil {
		t.Fatal(err)
	}
	cfg.RegistrationURL = "https://github.com/example/repo"
	cfg.ScaleSetName = "test"
	cfg.Kubeconfig = "/tmp/kubeconfig"
	cfg.RunnerImage = "ubuntu"
	cfg.Network = "network"
	if err := cfg.Complete(); err != nil {
		t.Fatal(err)
	}
	if len(cfg.HostMappings) != 2 || len(cfg.TrustedCACerts) != 1 || cfg.TrustedCACerts[0] != cert {
		t.Fatalf("flag values were not used: hosts=%#v certs=%d", cfg.HostMappings, len(cfg.TrustedCACerts))
	}
}

func TestParseHostMappingsRejectsInvalid(t *testing.T) {
	t.Parallel()
	for _, specs := range [][]string{
		{"missing-separator"}, {"bad/name=192.0.2.1"}, {"host=bad-ip"},
		{"host=fe80::1%eth0"}, {"host=192.0.2.1", "HOST=192.0.2.2"},
		{"host=192.0.2.1\nother=192.0.2.2"},
	} {
		if _, err := parseHostMappings(specs); err == nil {
			t.Errorf("accepted invalid extra hosts: %#v", specs)
		}
	}
}

func TestLoadTrustedCACertsRejectsInvalid(t *testing.T) {
	valid := testCertificatePEM(t, 5)
	for name, contents := range map[string]string{
		"empty": "", "garbage": "hello",
		"bad-cert":    "-----BEGIN CERTIFICATE-----\nYmFk\n-----END CERTIFICATE-----\n",
		"private-key": "-----BEGIN PRIVATE KEY-----\nYmFk\n-----END PRIVATE KEY-----\n",
		"mixed":       valid + "-----BEGIN PRIVATE KEY-----\nYmFk\n-----END PRIVATE KEY-----\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cert.pem")
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadTrustedCACerts([]string{path}); err == nil {
				t.Fatal("expected certificate validation error")
			}
		})
	}
	if _, err := loadTrustedCACerts([]string{filepath.Join(t.TempDir(), "missing.pem")}); err == nil || !strings.Contains(err.Error(), "read extra CA") {
		t.Fatalf("expected missing certificate file error, got %v", err)
	}
}
