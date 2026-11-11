package resolver

import (
	"context"
	"errors"

	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/enroll"
	"github.com/chencohensolutions/news-server-core-go/graph/model"
)

// MaxMarkRead is how many urls one markRead may send.
const MaxMarkRead = 500

func roleOf(role string) model.Role {
	if role == auth.RoleUser {
		return model.RoleUser
	}
	return model.RoleAdmin
}

func roleName(role model.Role) string {
	if role == model.RoleAdmin {
		return auth.RoleAdmin
	}
	return auth.RoleUser
}

// userError is what a refused change to user says: BAD_REQUEST for what the caller can fix.
func userError(err error, user string) error {
	switch {
	case errors.Is(err, auth.ErrUnknownUser):
		return badRequest("no user %q", user)
	case errors.Is(err, auth.ErrLastAdmin):
		return badRequest("%v", err)
	}
	return err
}

// user is u with their devices.
func (r *Resolver) user(ctx context.Context, u auth.UserConfig) *model.User {
	out := &model.User{Name: u.Username, Role: roleOf(u.Role), Devices: []*model.Device{}}
	for _, d := range r.Enrollment.Registry.List(u.Username) {
		out.Devices = append(out.Devices, deviceOf(ctx, d))
	}
	return out
}

func deviceOf(ctx context.Context, d enroll.Device) *model.Device {
	return &model.Device{Serial: d.Serial, Name: d.Name, KeyProtection: d.KeyProtection, RegisteredAt: d.RegisteredAt,
		ExpiresAt: d.ExpiresAt, RevokedAt: d.RevokedAt, Current: d.Serial == auth.SerialFromContext(ctx)}
}
