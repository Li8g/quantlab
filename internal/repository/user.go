// UserRepo is the persistence layer for the users table. Single-account
// systems still go through this repo so credentials are bcrypt-hashed
// and indexed by email like a real multi-user surface — only the seed
// path (cmd/saas --seed-user) is restricted to one row in practice.
package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"quantlab/internal/saas/store"
)

// UserRepo wraps the gorm.DB for users-table lookups + writes.
type UserRepo struct {
	db *gorm.DB
}

// NewUserRepo wraps a *gorm.DB.
func NewUserRepo(db *gorm.DB) *UserRepo {
	return &UserRepo{db: db}
}

// GetByEmail returns the user matching email exactly (case-sensitive —
// emails on the users table are stored as submitted). Returns
// gorm.ErrRecordNotFound when no match exists; callers map that to 401
// without distinguishing "no such user" from "wrong password".
func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*store.User, error) {
	var u store.User
	if err := r.db.WithContext(ctx).Where("email = ?", email).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// Create inserts a User row. Caller has already filled PasswordHash with
// bcrypt(plaintext) and UserID with a ULID. The unique index on email
// is the primary collision guard — a duplicate seed call surfaces here
// as a gorm constraint error.
func (r *UserRepo) Create(ctx context.Context, u *store.User) error {
	if u.Email == "" {
		return errors.New("repository.UserRepo: empty email")
	}
	if u.PasswordHash == "" {
		return errors.New("repository.UserRepo: empty password_hash")
	}
	return r.db.WithContext(ctx).Create(u).Error
}

// UpdateLastLoginAt sets last_login_at to `at` for the given user.
// Best-effort: callers (Login handler) ignore the error so a transient
// DB hiccup doesn't fail an otherwise-valid login.
func (r *UserRepo) UpdateLastLoginAt(ctx context.Context, userID uint, at time.Time) error {
	return r.db.WithContext(ctx).
		Model(&store.User{}).
		Where("id = ?", userID).
		Update("last_login_at", at).Error
}
