package config

import "testing"

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
