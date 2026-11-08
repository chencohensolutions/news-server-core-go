// Package auth holds the server's configuration and the identity of a request: the user and device
// that the client certificate named. See docs/authentication.md.
package auth

import (
	"encoding/json"
	"fmt"
	"os"
)

// UserConfig is one person whose devices may enroll.
type UserConfig struct {
	// Also the common name (CN) of the user's client certificates: enrollment signs CN=<username>.
	Username string `json:"username"`
}

// AppConfig is config.json. Relative paths are relative to the folder the server runs in.
type AppConfig struct {
	// The server's certificate and key, which devices check.
	TLSCert string `json:"tls_cert"`
	TLSKey  string `json:"tls_key"`
	// The CA that signs device certificates, and its key (enrollment signs with it).
	ClientCA    string `json:"client_ca"`
	ClientCAKey string `json:"client_ca_key"`
	// The address devices reach the server at, shown by enroll-code and put in its QR code ("https://host:port").
	PublicURL string `json:"public_url"`
	Port      string `json:"port"`
	// Enrolled devices, revocations and renewals.
	DevicesFile string `json:"devices_file"`
	// The loopback-only admin API, and the file holding its token (rewritten on every start).
	AdminAddr      string `json:"admin_addr"`
	AdminTokenFile string `json:"admin_token_file"`
	// The site configs: the news repo's sources/ folder (a git submodule at news/).
	SourcesDir string       `json:"sources_dir"`
	Users      []UserConfig `json:"users"`
}

const (
	// DefaultPort is one above the journal server's 56398, so both can run on one PC.
	DefaultPort           = "56399"
	DefaultDevicesFile    = "devices.json"
	DefaultAdminAddr      = "127.0.0.1:8082"
	DefaultAdminTokenFile = "admin.token"
	DefaultSourcesDir     = "news/sources"
)

// LoadConfig reads config.json, fills in the defaults and checks the users.
func LoadConfig(path string) (AppConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AppConfig{}, fmt.Errorf("failed to open %s: %w", path, err)
	}
	var config AppConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return AppConfig{}, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	seen := map[string]bool{}
	for _, user := range config.Users {
		if user.Username == "" {
			return AppConfig{}, fmt.Errorf("%s: a user has no username", path)
		}
		if seen[user.Username] {
			return AppConfig{}, fmt.Errorf("%s: user %q appears twice", path, user.Username)
		}
		seen[user.Username] = true
	}
	return config.WithDefaults(), nil
}

// WithDefaults fills in what config.json left out.
func (c AppConfig) WithDefaults() AppConfig {
	if c.Port == "" {
		c.Port = DefaultPort
	}
	if c.DevicesFile == "" {
		c.DevicesFile = DefaultDevicesFile
	}
	if c.AdminAddr == "" {
		c.AdminAddr = DefaultAdminAddr
	}
	if c.AdminTokenFile == "" {
		c.AdminTokenFile = DefaultAdminTokenFile
	}
	if c.SourcesDir == "" {
		c.SourcesDir = DefaultSourcesDir
	}
	return c
}

// FindUser is the user named username, or nil.
func (c AppConfig) FindUser(username string) *UserConfig {
	for i := range c.Users {
		if c.Users[i].Username == username {
			return &c.Users[i]
		}
	}
	return nil
}
