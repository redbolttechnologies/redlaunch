package service

import (
	"context"
	"errors"
	"redlaunch/internal/application"
	"redlaunch/internal/auth"
)

type UserRepository interface {
	ListUsers(context.Context) ([]application.UserAccount, error)
	CreateUser(context.Context, string, string) (application.UserAccount, error)
	ChangeUserPassword(context.Context, int64, string) error
	DeleteUser(context.Context, int64) error
	AssignUserEmail(context.Context, int64, string) error
}

type UserService struct{ repository UserRepository }

func NewUserService(repository UserRepository) (*UserService, error) {
	if repository == nil {
		return nil, errors.New("user repository is required")
	}
	return &UserService{repository}, nil
}
func (s *UserService) List(ctx context.Context) ([]application.UserAccount, error) {
	return s.repository.ListUsers(ctx)
}
func (s *UserService) Create(ctx context.Context, email, password string) error {
	email, err := application.ValidateEmail(email)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = s.repository.CreateUser(ctx, email, hash)
	return err
}
func (s *UserService) ChangePassword(ctx context.Context, id int64, password string) error {
	if id < 1 {
		return application.ErrUserNotFound
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	return s.repository.ChangeUserPassword(ctx, id, hash)
}
func (s *UserService) Delete(ctx context.Context, id int64) error {
	if id < 1 {
		return application.ErrUserNotFound
	}
	users, err := s.repository.ListUsers(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, user := range users {
		if user.ID == id {
			found = true
		}
	}
	if !found {
		return application.ErrUserNotFound
	}
	if len(users) <= 1 {
		return application.ErrLastUser
	}
	return s.repository.DeleteUser(ctx, id)
}
func (s *UserService) AssignEmail(ctx context.Context, id int64, email string) error {
	if id < 1 {
		return application.ErrUserNotFound
	}
	email, err := application.ValidateEmail(email)
	if err != nil {
		return err
	}
	return s.repository.AssignUserEmail(ctx, id, email)
}
