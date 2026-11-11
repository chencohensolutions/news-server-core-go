package resolver

import (
	"time"

	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/enroll"
)

// This file will not be regenerated automatically.
//
// It serves as dependency injection for your app, add any dependencies you require
// here.

type Resolver struct {
	Config auth.AppConfig
	// The people who may enroll, with their roles.
	Users      *auth.Users
	Enrollment *enroll.Service
	Now        func() time.Time
	// Mapped is the router's outside address for the server's port (https://ip:port), "" when there is none; the
	// enrollment link's server when public_url is empty. Nil: none.
	Mapped func() string
	// Discovery is the running server's fingerprint, DHT key ("" when off) and current addresses; nil before known.
	Discovery func() (fingerprint, dhtKey string, addresses []string)
}
