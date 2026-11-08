package resolver

import (
	"github.com/chencohensolutions/news-server-core-go/enroll"
	"github.com/chencohensolutions/news-server-core-go/sites"
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
}

// APIVersion is serverInfo.api: raised when a change needs newer apps.
const APIVersion = 1
