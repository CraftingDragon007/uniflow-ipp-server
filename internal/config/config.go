package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
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
	Enabled           bool   `toml:"enabled"`
	TenantDomain      string `toml:"tenant_domain"`
	InstallerID       string `toml:"installer_id"`
	SecureQueueName   string `toml:"secure_queue_name"`
	OAuthClientID     string `toml:"oauth_client_id"`
	OAuthClientSecret string `toml:"oauth_client_secret"`
	OAuthScope        string `toml:"oauth_scope"`
	DeviceEndpoint    string `toml:"device_endpoint"`
	TokenEndpoint     string `toml:"token_endpoint"`
	TokenPath         string `toml:"token_path"`
	ManagementServer  string `toml:"management_server"`
	UserID            string `toml:"user_id"`
}

func (c *CloudConfig) SubmissionURL() string {
	return "https://" + c.ManagementServer + "/api/v1/smartclient/printjob/" + strings.ToLower(strings.ReplaceAll(c.UserID, "-", ""))
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
			Enabled:         false,
			TokenPath:       "/var/lib/uniflow-ipp/token.json",
			SecureQueueName: "MFD_STANDARD_CANON",
		},
	}
}

func (c *Config) PrinterURI() string {
	return "ipp://" + c.Server.Listen + "/ipp"
}

func (c *Config) Validate() error {
	host, port, err := net.SplitHostPort(c.Server.Listen)
	if err != nil {
		return fmt.Errorf("server.listen must be host:port: %w", err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("server.listen requires a port between 1 and 65535")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("server.listen must use a loopback IP; remote unauthenticated printing is not supported")
	}
	if c.Server.SpoolDir == "" {
		return fmt.Errorf("server.spool_dir is required")
	}
	if c.Cloud.Enabled {
		if c.Cloud.TenantDomain == "" && c.Tenant.Domain != "" {
			c.Cloud.TenantDomain = c.Tenant.Domain
		}
		u, err := url.Parse("https://" + c.Cloud.ManagementServer)
		if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("cloud.management_server must be the registered management-server hostname")
		}
		id := strings.ReplaceAll(c.Cloud.UserID, "-", "")
		if len(c.Cloud.UserID) != 32 {
			if len(c.Cloud.UserID) != 36 || c.Cloud.UserID[8] != '-' || c.Cloud.UserID[13] != '-' || c.Cloud.UserID[18] != '-' || c.Cloud.UserID[23] != '-' {
				return fmt.Errorf("cloud.user_id must be a canonical user UUID")
			}
		}
		if len(id) != 32 || id == strings.Repeat("0", 32) {
			return fmt.Errorf("cloud.user_id must be a nonzero user UUID")
		}
		for _, ch := range id {
			if !strings.ContainsRune("0123456789abcdefABCDEF", ch) {
				return fmt.Errorf("cloud.user_id must be a user UUID")
			}
		}
		if c.Cloud.TokenPath == "" {
			return fmt.Errorf("cloud.token_path is required")
		}
	}
	for _, endpoint := range []string{c.Cloud.DeviceEndpoint, c.Cloud.TokenEndpoint} {
		if endpoint == "" {
			continue
		}
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return fmt.Errorf("OAuth endpoints must be absolute HTTPS URLs")
		}
	}
	return nil
}
