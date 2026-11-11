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
	// What the server read from the sites: one SQLite file.
	DatabaseFile string `json:"database_file"`
	// Minutes between reads of every site; 0 is DefaultRefreshMinutes, -1 turns reading off.
	RefreshMinutes int `json:"refresh_minutes"`
	// Days a headline no longer on its site is kept, with its article; 0 is DefaultKeepDays, -1 keeps everything.
	KeepDays int `json:"keep_days"`
	// Sources read from their RSS feed instead of their pages, and each site's wait between its requests in
	// milliseconds (the others wait 250 ms): the starting values; the app's Sources screen changes them (setSource),
	// and its choices, kept in the database, win.
	FeedSources []string       `json:"feed_sources"`
	SiteWaitsMs map[string]int `json:"site_waits_ms"`
	// Stories: the ONNX Runtime library ("" looks in ./onnxruntime and the usual places), the folder the models are
	// in (news-server models downloads them), the model (bge-m3 or multilingual-e5-small), what headlines are
	// compared by (title, subtitle, title_and_subtitle, title_times_subtitle), how strict the grouping is (10 to 90,
	// the app's slider; 0 is DefaultStoryStrictness, -1 turns stories off), and the days of headlines grouped.
	ONNXRuntime     string `json:"onnxruntime"`
	ModelsDir       string `json:"models_dir"`
	StoryModel      string `json:"story_model"`
	StoryText       string `json:"story_text"`
	StoryStrictness int    `json:"story_strictness"`
	StoryDays       int    `json:"story_days"`
	// Summaries (Gemini, free tier only): the file holding the API key (Google AI Studio, a project without
	// billing; "" is gemini.key, and no file turns summaries off), the model ("" is gemini-3.5-flash-lite), the
	// day's request budget (0 is 400, -1 turns summaries off), the hours of stories summarized (0 is 12), and the
	// fewest reports a summarized story has (0 is 2). A run sends more story requests while stories wait, up to
	// summary_requests_per_run (0 is 4), as long as the budget keeps two for each run left in the day. Summaries
	// need stories.
	GeminiKeyFile         string `json:"gemini_key_file"`
	GeminiModel           string `json:"gemini_model"`
	SummaryBudget         int    `json:"summary_budget"`
	SummaryHours          int    `json:"summary_hours"`
	SummaryMinReports     int    `json:"summary_min_reports"`
	SummaryRequestsPerRun int    `json:"summary_requests_per_run"`
	// Whether the server asks the home router (UPnP or NAT-PMP) to forward its port, so the phone reaches it away
	// from home; on unless false. With public_url empty, the router's outside address goes in the enrollment link.
	PortMapping *bool `json:"port_mapping"`
	// Whether the server publishes its addresses in the BitTorrent DHT, so the app finds it when its address
	// changes (a home IP); on unless false.
	Discovery *bool `json:"discovery"`
}

// DiscoveryOn says whether the server uses the DHT: on unless "discovery": false.
func (c AppConfig) DiscoveryOn() bool { return c.Discovery == nil || *c.Discovery }

// PortMappingOn says whether the server asks the router to forward its port: on unless "port_mapping": false.
func (c AppConfig) PortMappingOn() bool { return c.PortMapping == nil || *c.PortMapping }

const (
	// DefaultPort is one above the journal server's 56398, so both can run on one PC.
	DefaultPort           = "56399"
	DefaultDevicesFile    = "devices.json"
	DefaultAdminAddr      = "127.0.0.1:8082"
	DefaultAdminTokenFile = "admin.token"
	DefaultSourcesDir     = "news/sources"
	DefaultDatabaseFile   = "news.db"
	DefaultRefreshMinutes = 10
	DefaultKeepDays       = -1
	DefaultModelsDir      = "models"
	// bge-m3 is the more accurate model, too big for the phone; the text and strictness are the app's defaults.
	DefaultStoryModel      = "bge-m3"
	DefaultStoryText       = "title_and_subtitle"
	DefaultStoryStrictness = 60
	DefaultStoryDays       = 7
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
	if c.DatabaseFile == "" {
		c.DatabaseFile = DefaultDatabaseFile
	}
	if c.RefreshMinutes == 0 {
		c.RefreshMinutes = DefaultRefreshMinutes
	}
	if c.KeepDays == 0 {
		c.KeepDays = DefaultKeepDays
	}
	if c.ModelsDir == "" {
		c.ModelsDir = DefaultModelsDir
	}
	if c.StoryModel == "" {
		c.StoryModel = DefaultStoryModel
	}
	if c.StoryText == "" {
		c.StoryText = DefaultStoryText
	}
	if c.StoryStrictness == 0 {
		c.StoryStrictness = DefaultStoryStrictness
	}
	if c.StoryDays == 0 {
		c.StoryDays = DefaultStoryDays
	}
	if c.GeminiKeyFile == "" {
		c.GeminiKeyFile = "gemini.key"
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
