package resolver

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/enroll"
)

func codeResolver(t *testing.T, publicURL string, mapped func() string) *mutationResolver {
	t.Helper()
	registry, err := enroll.OpenRegistry(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	config := auth.AppConfig{PublicURL: publicURL, Users: []auth.UserConfig{{Username: "hen"}}}
	return &mutationResolver{&Resolver{Config: config, Enrollment: enroll.NewService(nil, registry, time.Now), Now: time.Now, Mapped: mapped}}
}

// The link's server: public_url, else the router's outside address, else none (the app keeps the one it has).
func TestTheEnrollmentLinksServer(t *testing.T) {
	for _, c := range []struct {
		publicURL, mapped, want string
	}{
		{"https://news.example:56399", "https://203.0.113.7:56399", "https://news.example:56399"},
		{"", "https://203.0.113.7:56399", "https://203.0.113.7:56399"},
		{"", "", ""},
	} {
		code, err := codeResolver(t, c.publicURL, func() string { return c.mapped }).CreateEnrollmentCode(context.Background(), "hen")
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if code.ServerURL != nil {
			got = *code.ServerURL
		}
		if got != c.want {
			t.Errorf("public_url %q, mapped %q: %q, want %q", c.publicURL, c.mapped, got, c.want)
		}
	}
}
