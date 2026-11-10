package resolver

import (
	"time"

	"github.com/chencohensolutions/news-server-core-go/enroll"
	"github.com/chencohensolutions/news-server-core-go/graph/model"
	"github.com/chencohensolutions/news-server-core-go/sites"
	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/stories"
	"github.com/chencohensolutions/news-server-core-go/summary"
)

// This file will not be regenerated automatically.
//
// It serves as dependency injection for your app, add any dependencies you require
// here.

type Resolver struct {
	Enrollment *enroll.Service
	// The sites the server reads.
	Sites []sites.Source
	// Each site's settings: whether the server reads it, from its feed, and its wait.
	Settings *sites.Settings
	// The server's build, shown by serverInfo.
	Version string
	// What the server read from the sites.
	Store *store.Store
	// The stories last grouped and when; nil when stories are off.
	Stories func() ([]stories.Story, time.Time)
	// Summaries says the server writes summaries after each grouping.
	Summaries bool
	// Gemini is the summary job's model, budget and latest run, for stats; nil when summaries are off.
	Gemini *GeminiInfo
	// Who the server is and where (serverInfo.discovery); nil, or nil from it, before that is known.
	Discovery func() *model.ServerDiscovery
}

// Codes in extensions.code that a device acts on.
const (
	CodeUnauthorized = "UNAUTHORIZED"
	// The request asks for more than a field allows (articles' 200 urls).
	CodeBadRequest = "BAD_REQUEST"
)

// MaxArticles is how many urls one articles query may ask for.
const MaxArticles = 200

// APIVersion is serverInfo.api: raised when a change needs newer apps.
const APIVersion = 1

// enabled says whether the server reads site id: switched-off sites leave headlines and stories.
func (r *Resolver) enabled(id string) bool { return r.Settings == nil || r.Settings.Enabled(id) }

// setting is site's settings; every site is on, with its pages and the default wait, without Settings.
func (r *Resolver) setting(site *sites.Source) sites.Setting {
	if r.Settings == nil {
		return sites.Setting{Enabled: true, WaitMs: sites.DefaultWaitMs}
	}
	return r.Settings.Get(site.ID)
}

// GeminiInfo is what stats says about the summary job.
type GeminiInfo struct {
	Model  string
	Budget int
	Status func() summary.Status
}
