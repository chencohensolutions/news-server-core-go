package resolver

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/enroll"
	"github.com/chencohensolutions/news-server-core-go/graph/model"
	"github.com/chencohensolutions/news-server-core-go/sites"
	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/stories"
	"github.com/chencohensolutions/news-server-core-go/summary"
	"github.com/chencohensolutions/news-server-core-go/video"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// This file will not be regenerated automatically.
//
// It serves as dependency injection for your app, add any dependencies you require
// here.

type Resolver struct {
	Enrollment *enroll.Service
	// The people who may enroll devices, with their roles.
	Accounts *auth.Users
	// InviteLink is the enrollment link (news://enroll?code=…) for code, with the server's address and key; nil
	// gives the bare code's link.
	InviteLink func(code string) string
	// The sites the server reads.
	Sites []sites.Source
	// Each site's settings: whether the server reads it, from its feed, and its wait.
	Settings *sites.Settings
	// The server's build, shown by serverInfo.
	Version string
	// What the server read from the sites.
	Store *store.Store
	// Videos relays articles' videos to the app (videoStream); nil: none.
	Videos *video.Relay
	// The stories last grouped and when; nil when stories are off.
	Stories func() ([]stories.Story, time.Time)
	// Grouping is how the stories are grouped, which the app changes; nil when stories are off.
	Grouping Grouping
	// StoryModel is the story model's id, for grouping.
	StoryModel string
	// Summaries says the server writes summaries after each grouping.
	Summaries bool
	// Gemini is the summary job's model, budget and latest run, for stats; nil when summaries are off.
	Gemini *GeminiInfo
	// Loop is the latest round of reading, grouping and summarizing; nil, or nil from it, before one.
	Loop func() *model.LoopStats
	// Who the server is and where (serverInfo.discovery); nil, or nil from it, before that is known.
	Discovery func() *model.ServerDiscovery
}

// Codes in extensions.code that a device acts on.
const (
	CodeUnauthorized = "UNAUTHORIZED"
	// An @admin field from a user (auth.RequireUserUnlessPublic).
	CodeForbidden = "FORBIDDEN"
	// The request asks for more than a field allows (articles' 200 urls).
	CodeBadRequest = "BAD_REQUEST"
)

// badRequest is a BAD_REQUEST error.
func badRequest(format string, args ...any) error {
	return &gqlerror.Error{Message: fmt.Sprintf(format, args...), Extensions: map[string]any{"code": CodeBadRequest}}
}

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

// Grouping is the story job's grouping settings.
type Grouping interface {
	// Grouping is the settings now and the similarity they are.
	Grouping() (stories.Grouping, float32)
	// SetGrouping keeps g (checked) and groups the stories again.
	SetGrouping(ctx context.Context, g stories.Grouping) error
	// GeminiAvailable says Gemini can group the stories (the server has a Gemini key).
	GeminiAvailable() bool
}

// GeminiInfo is what stats says about the summary job.
type GeminiInfo struct {
	Model  string
	Budget int
	Status func() summary.Status
}

// grouping is the grouping field: Grouping's settings now.
func (r *Resolver) grouping() *model.Grouping {
	g, threshold := r.Grouping.Grouping()
	method := model.GroupingMethodModel
	if g.ByGemini {
		method = model.GroupingMethodGemini
	}
	return &model.Grouping{Method: method, GeminiAvailable: r.Grouping.GeminiAvailable(), Model: r.StoryModel, Text: model.StoryText(strings.ToUpper(g.Text.String())), Strictness: g.Strictness,
		Days: g.Days, Threshold: math.Round(float64(threshold)*1e4) / 1e4}
}

// summarySettings is the summaries' settings as the API shows them.
func summarySettings(s summary.Settings) *model.SummarySettings {
	return model.SummarySettingsOf(&s)
}
