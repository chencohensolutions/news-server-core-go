package newsserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A user reads but can't change what the server does for everyone; an admin can.
func TestOnlyAdminsChangeTheServer(t *testing.T) {
	env := newTestEnv(t)
	admin, _ := env.enrollDevice(t, "hen")
	user, _ := env.enrollDevice(t, "dana")

	_, resp := env.query(t, user, `{ me { user role deviceName } sources { id } }`, nil)
	if len(resp.Errors) > 0 || string(resp.Data["me"]) != `{"user":"dana","role":"USER","deviceName":"Pixel"}` {
		t.Fatalf("me: %+v %s", resp.Errors, resp.Data["me"])
	}
	for _, q := range []string{
		`mutation { setSource(id: "siteh", enabled: false) { id } }`,
		`mutation { createInvite(user: "eve") { code } }`,
		`{ users { name } }`,
	} {
		_, resp := env.query(t, user, q, nil)
		if len(resp.Errors) != 1 || resp.Errors[0].Extensions["code"] != "FORBIDDEN" {
			t.Fatalf("%s from a user: %+v", q, resp.Errors)
		}
	}
	if env.api.Settings.Get("siteh").Enabled != true {
		t.Fatal("a user switched a site off")
	}
	_, resp = env.query(t, admin, `mutation { setSource(id: "siteh", enabled: false) { id enabled } }`, nil)
	if len(resp.Errors) > 0 || env.api.Settings.Get("siteh").Enabled {
		t.Fatalf("admin: %+v", resp.Errors)
	}
}

// An admin invites someone new as a user, makes them admin, and removes them, revoking their devices; the last admin
// stays.
func TestAdminsManageUsers(t *testing.T) {
	env := newTestEnv(t)
	admin, _ := env.enrollDevice(t, "hen")
	_, resp := env.query(t, admin, `mutation { createInvite(user: "eve") { code user role link } }`, nil)
	var invite struct{ Code, User, Role, Link string }
	json.Unmarshal(resp.Data["createInvite"], &invite)
	if len(resp.Errors) > 0 || invite.User != "eve" || invite.Role != "USER" || !strings.HasPrefix(invite.Link, "news://enroll?code=") {
		t.Fatalf("createInvite: %+v %s", resp.Errors, resp.Data["createInvite"])
	}
	if p, ok := env.enrollment.Codes.Peek(invite.Code); !ok || p.User != "eve" {
		t.Fatalf("the invite's code isn't eve's: %+v", p)
	}
	eve, _ := env.enrollDevice(t, "eve")

	_, resp = env.query(t, admin, `mutation { setUserRole(user: "eve", role: ADMIN) { name role } }`, nil)
	if len(resp.Errors) > 0 || string(resp.Data["setUserRole"]) != `{"name":"eve","role":"ADMIN"}` {
		t.Fatalf("setUserRole: %+v %s", resp.Errors, resp.Data["setUserRole"])
	}
	_, resp = env.query(t, eve, `{ users { name role devices { name current } } }`, nil)
	if len(resp.Errors) > 0 || !strings.Contains(string(resp.Data["users"]), `{"name":"eve","role":"ADMIN","devices":[{"name":"Pixel","current":true}]}`) {
		t.Fatalf("users: %+v %s", resp.Errors, resp.Data["users"])
	}

	_, resp = env.query(t, admin, `mutation { removeUser(user: "eve") }`, nil)
	if len(resp.Errors) > 0 {
		t.Fatalf("removeUser: %+v", resp.Errors)
	}
	if status, _ := env.query(t, eve, `{ me { user } }`, nil); status != http.StatusUnauthorized {
		t.Fatalf("a removed user's device got %d", status)
	}
	// dana is a user, so hen is the last admin.
	_, resp = env.query(t, admin, `mutation { setUserRole(user: "hen", role: USER) { name } }`, nil)
	if len(resp.Errors) != 1 || resp.Errors[0].Extensions["code"] != "BAD_REQUEST" {
		t.Fatalf("the last admin stepped down: %+v", resp.Errors)
	}
}

// Each user hides their own sites and keeps their own reads, on all their devices.
func TestUsersKeepTheirOwnChoices(t *testing.T) {
	env := newTestEnv(t)
	hen, _ := env.enrollDevice(t, "hen")
	dana, _ := env.enrollDevice(t, "dana")
	danaTablet, _ := env.enrollDevice(t, "dana")

	_, resp := env.query(t, dana, `mutation { setSourceHidden(id: "siteh", hidden: true) { id hidden } }`, nil)
	if len(resp.Errors) > 0 || string(resp.Data["setSourceHidden"]) != `{"id":"siteh","hidden":true}` {
		t.Fatalf("setSourceHidden: %+v %s", resp.Errors, resp.Data["setSourceHidden"])
	}
	_, resp = env.query(t, hen, `{ sources { hidden enabled } }`, nil)
	if string(resp.Data["sources"]) != `[{"hidden":false,"enabled":true}]` {
		t.Fatalf("hen sees dana's choice: %s", resp.Data["sources"])
	}

	_, resp = env.query(t, dana, `mutation { markRead(urls: ["https://www.siteh.example/a"]) }`, nil)
	if len(resp.Errors) > 0 || string(resp.Data["markRead"]) != "1" {
		t.Fatalf("markRead: %+v %s", resp.Errors, resp.Data["markRead"])
	}
	_, resp = env.query(t, danaTablet, `{ readArticles { url } sources { hidden } }`, nil)
	if string(resp.Data["readArticles"]) != `[{"url":"https://www.siteh.example/a"}]` || string(resp.Data["sources"]) != `[{"hidden":true}]` {
		t.Fatalf("dana's other device: %+v %s %s", resp.Errors, resp.Data["readArticles"], resp.Data["sources"])
	}
	_, resp = env.query(t, hen, `{ readArticles { url } }`, nil)
	if string(resp.Data["readArticles"]) != `[]` {
		t.Fatalf("hen sees dana's reads: %s", resp.Data["readArticles"])
	}
}
