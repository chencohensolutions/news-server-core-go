package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// The roles a user has. An admin changes what the server reads, groups and summarizes for everyone, invites people
// and revokes devices; a user reads, and keeps only their own choices (UserPrefs).
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// IsAdmin says whether u may change the server's settings and its users.
func (u *UserConfig) IsAdmin() bool { return u != nil && u.Role == RoleAdmin }

var (
	ErrUnknownUser = errors.New("unknown user")
	// ErrLastAdmin keeps the server from losing its last admin, who alone can make others.
	ErrLastAdmin = errors.New("the server's last admin can't be removed or made a user")
)

const maxUsernameLength = 64

// Users are the people whose devices may enroll, with their roles, kept in users_file. config.json's users are added
// the first time the server sees them (as admins unless they say "role": "user"); after that the file is what
// counts, so a role changed in the app stays changed.
type Users struct {
	mu    sync.RWMutex
	path  string
	users []UserConfig
}

// OpenUsers reads path (none yet is no users) and adds seed's users it doesn't have.
func OpenUsers(path string, seed []UserConfig) (*Users, error) {
	u := &Users{path: path}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, &u.users); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	added := false
	for _, s := range seed {
		if u.find(s.Username) >= 0 {
			continue
		}
		if s.Role == "" {
			s.Role = RoleAdmin
		}
		u.users = append(u.users, s)
		added = true
	}
	for i := range u.users {
		if u.users[i].Role != RoleUser {
			u.users[i].Role = RoleAdmin
		}
	}
	if added {
		if err := u.save(); err != nil {
			return nil, err
		}
	}
	return u, nil
}

// Find is the user named username, a copy, or nil.
func (u *Users) Find(username string) *UserConfig {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if i := u.find(username); i >= 0 {
		user := u.users[i]
		return &user
	}
	return nil
}

// List is every user, by name.
func (u *Users) List() []UserConfig {
	u.mu.RLock()
	defer u.mu.RUnlock()
	out := append([]UserConfig(nil), u.users...)
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

// Add adds a user with role, or returns the one there is (its role unchanged) and false.
func (u *Users) Add(username, role string) (UserConfig, bool, error) {
	username, err := CleanUsername(username)
	if err != nil {
		return UserConfig{}, false, err
	}
	if role != RoleAdmin && role != RoleUser {
		return UserConfig{}, false, fmt.Errorf("unknown role %q", role)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if i := u.find(username); i >= 0 {
		return u.users[i], false, nil
	}
	user := UserConfig{Username: username, Role: role}
	u.users = append(u.users, user)
	if err := u.save(); err != nil {
		u.users = u.users[:len(u.users)-1]
		return UserConfig{}, false, err
	}
	return user, true, nil
}

// SetRole changes username's role; the last admin stays one.
func (u *Users) SetRole(username, role string) (UserConfig, error) {
	if role != RoleAdmin && role != RoleUser {
		return UserConfig{}, fmt.Errorf("unknown role %q", role)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	i := u.find(username)
	if i < 0 {
		return UserConfig{}, ErrUnknownUser
	}
	if u.users[i].Role == RoleAdmin && role != RoleAdmin && u.admins() == 1 {
		return UserConfig{}, ErrLastAdmin
	}
	old := u.users[i].Role
	u.users[i].Role = role
	if err := u.save(); err != nil {
		u.users[i].Role = old
		return UserConfig{}, err
	}
	return u.users[i], nil
}

// Remove takes username off the list, so none of their certificates is let in again; the last admin stays.
func (u *Users) Remove(username string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	i := u.find(username)
	if i < 0 {
		return ErrUnknownUser
	}
	if u.users[i].Role == RoleAdmin && u.admins() == 1 {
		return ErrLastAdmin
	}
	saved := u.users
	u.users = append(append([]UserConfig(nil), u.users[:i]...), u.users[i+1:]...)
	if err := u.save(); err != nil {
		u.users = saved
		return err
	}
	return nil
}

func (u *Users) find(username string) int {
	for i := range u.users {
		if u.users[i].Username == username {
			return i
		}
	}
	return -1
}

func (u *Users) admins() int {
	n := 0
	for _, user := range u.users {
		if user.Role == RoleAdmin {
			n++
		}
	}
	return n
}

// save writes the file whole, through a temporary file, so a crash never leaves half of it.
func (u *Users) save() error {
	if u.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(u.users, "", "  ")
	if err != nil {
		return err
	}
	tmp := u.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, u.path)
}

// CleanUsername checks a new user's name: it becomes the common name of their certificates and a key in the
// database, so it is short, one line, and has no characters a file name or a certificate can't take.
func CleanUsername(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("a user needs a name")
	}
	if utf8.RuneCountInString(name) > maxUsernameLength {
		return "", fmt.Errorf("a user's name has at most %d characters", maxUsernameLength)
	}
	if strings.ContainsAny(name, "\\/:*?\"<>|,=+;") || strings.IndexFunc(name, func(r rune) bool { return r < ' ' }) >= 0 {
		return "", fmt.Errorf("%q: a user's name can't have \\ / : * ? \" < > | , = + ; or control characters", name)
	}
	return name, nil
}
