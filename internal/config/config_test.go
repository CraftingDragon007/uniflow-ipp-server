package config

import "testing"

func TestValidation(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8631", "[::1]:8631"} {
		cfg := DefaultConfig()
		cfg.Server.Listen = addr
		if err := cfg.Validate(); err != nil {
			t.Fatal(err)
		}
		if cfg.PrinterURI() != "ipp://"+addr+"/ipp" {
			t.Fatal("IPv6 printer URI malformed")
		}
	}
	for _, addr := range []string{"127.0.0.1", "127.0.0.1:0", "0.0.0.0:8631", ":8631", "127.0.0.1:70000"} {
		cfg := DefaultConfig()
		cfg.Server.Listen = addr
		if err := cfg.Validate(); err == nil {
			t.Errorf("invalid listen address accepted: %s", addr)
		}
	}
}

func TestCloudConfiguration(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Cloud.Enabled = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("unconfigured cloud accepted")
	}
	cfg.Cloud.ManagementServer = "management.example.invalid"
	cfg.Cloud.UserID = "12345678-90ab-cdef-1234-567890abcdef"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Cloud.SubmissionURL(); got != "https://management.example.invalid/api/v1/smartclient/printjob/1234567890abcdef1234567890abcdef" {
		t.Fatalf("submission URL = %s", got)
	}
	cfg.Cloud.ManagementServer = "management.example.invalid/guessed"
	if err := cfg.Validate(); err == nil {
		t.Fatal("management server path accepted")
	}
	cfg.Cloud.ManagementServer = "management.example.invalid"
	cfg.Cloud.TokenEndpoint = "http://identity.example.invalid/token"
	if err := cfg.Validate(); err == nil {
		t.Fatal("insecure OAuth endpoint accepted")
	}
}
