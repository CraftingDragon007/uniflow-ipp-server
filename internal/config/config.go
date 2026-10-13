package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server ServerConfig `toml:"server"`
	Tenant TenantConfig `toml:"tenant"`
	CUPS   CUPSConfig   `toml:"cups"`
	Cloud  CloudConfig  `toml:"cloud"`
}

type ServerConfig struct {
	Listen   string `toml:"listen"`
	SpoolDir string `toml:"spool_dir"`
}

type TenantConfig struct {
	Domain         string `toml:"domain"`
	InstallerName  string `toml:"installer_name"`
	ProductName    string `toml:"product_name"`
	ProductVersion string `toml:"product_version"`
}

type CUPSConfig struct {
	QueueName    string `toml:"queue_name"`
	QueueDisplay string `toml:"queue_display"`
	UseIPP       bool   `toml:"use_ipp"`
	PPDPath      string `toml:"ppd_path"`
	ShareQueue   bool   `toml:"share_queue"`
}

type CloudConfig struct {
	Enabled bool `toml:"enabled"`
}

func Load(path string) (*Config, error) {
	cfg := defaultConfig()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return cfg, fmt.Errorf("config file not found: %s", path)
	}

	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return cfg, nil
}

func DefaultConfig() *Config {
	return defaultConfig()
}

func defaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Listen:   "127.0.0.1:8631",
			SpoolDir: "/var/spool/uniflow-ipp",
		},
		Tenant: TenantConfig{
			Domain:         "",
			InstallerName:  "Linux IPP Client",
			ProductName:    "uniFLOW IPP Server",
			ProductVersion: "1.0.0",
		},
		CUPS: CUPSConfig{
			QueueName:    "uniflow_secure",
			QueueDisplay: "uniFLOW Secure Print",
			UseIPP:       true,
			PPDPath:      "/usr/share/cups/model/uniflow-ipp.ppd",
			ShareQueue:   false,
		},
		Cloud: CloudConfig{
			Enabled: false,
		},
	}
}

func (c *Config) PrinterURI() string {
	parts := strings.Split(c.Server.Listen, ":")
	host := "localhost"
	port := "8631"
	if len(parts) >= 1 {
		host = parts[0]
	}
	if len(parts) >= 2 {
		port = parts[1]
	}
	return fmt.Sprintf("ipp://%s:%s/ipp", host, port)
}

func (c *Config) Validate() error {
	if c.Tenant.Domain != "" && c.Cloud.Enabled {
		if c.Tenant.InstallerName == "" {
			return fmt.Errorf("tenant.installer_name is required when cloud.enabled is true")
		}
	}
	return nil
}