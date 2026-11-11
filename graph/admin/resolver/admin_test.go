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
	users, err := auth.OpenUsers(filepath.Join(t.TempDir(), "users.json"), config.Users)
	if err != nil {
		t.Fatal(err)
	}
	return &mutationResolver{&Resolver{Config: config, Users: users, Enrollment: enroll.NewService(nil, registry, time.Now), Now: time.Now, Mapped: mapped}}
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
		code, err := codeResolver(t, c.publicURL, func() string { return c.mapped }).CreateEnrollmentCode(context.Background(), "hen", nil)
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

// A new name makes a user (a user unless the role says admin); a name that exists keeps its role unless one is given.
func TestAnEnrollmentCodeMakesItsUser(t *testing.T) {
	r := codeResolver(t, "", nil)
	if code, err := r.CreateEnrollmentCode(context.Background(), "dana", nil); err != nil || code.Role != auth.RoleUser {
		t.Fatalf("%+v %v", code, err)
	}
	if code, err := r.CreateEnrollmentCode(context.Background(), "hen", nil); err != nil || code.Role != auth.RoleAdmin {
		t.Fatalf("config.json's user: %+v %v", code, err)
	}
	admin := auth.RoleAdmin
	if code, err := r.CreateEnrollmentCode(context.Background(), "dana", &admin); err != nil || code.Role != auth.RoleAdmin {
		t.Fatalf("made admin: %+v %v", code, err)
	}
	if _, err := r.CreateEnrollmentCode(context.Background(), "a/b", nil); err == nil {
		t.Fatal("a name with a slash was taken")
	}
}
