package resolver

import (
	"time"

	"github.com/chencohensolutions/news-server-core-go/enroll"
	"github.com/chencohensolutions/news-server-core-go/sites"
	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/stories"
)

// This file will not be regenerated automatically.
//
// It serves as dependency injection for your app, add any dependencies you require
// here.

type Resolver struct {
	Enrollment *enroll.Service
	// The sites the server reads.
	Sites []sites.Source
	// The server's build, shown by serverInfo.
	Version string
	// What the server read from the sites.
	Store *store.Store
	// The stories last grouped and when; nil when stories are off.
	Stories func() ([]stories.Story, time.Time)
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
