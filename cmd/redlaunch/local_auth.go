package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"strings"

	"redlaunch/internal/application"
	"redlaunch/internal/auth"
	"redlaunch/internal/config"
	"redlaunch/internal/store"
)

// runLocalUser accepts a single password line over stdin, never a flag/env value.
func runLocalUser(ctx context.Context, args []string, input io.Reader, reset bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("redlaunch local user", flag.ContinueOnError)
	email := flags.String("email", "", "user email address")
	path := flags.String("db-path", cfg.DatabasePath, "SQLite database path")
	passwordStdin := flags.Bool("password-stdin", false, "read password from stdin")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !*passwordStdin {
		return errors.New("use --email ADDRESS --password-stdin; passwords must be supplied through stdin")
	}
	name, err := application.ValidateEmail(*email)
	if err != nil {
		return err
	}
	password, err := readLocalPassword(input)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	database, err := store.Open(ctx, *path)
	if err != nil {
		return err
	}
	defer database.Close()
	if reset {
		return database.ResetLocalPassword(ctx, name, hash)
	}
	return database.CreateLocalUser(ctx, name, hash)
}

func checkAuthenticationReady(ctx context.Context, cfg config.Config, database *store.Store) error {
	if len(cfg.AuthSessionSecret) < 32 {
		return errors.New("AUTH_SESSION_SECRET must contain at least 32 bytes before starting Redlaunch")
	}
	local, err := database.HasLocalUsers(ctx)
	if err != nil {
		return err
	}
	if local {
		return nil
	}
	if cfg.GoogleAuthEnabled() {
		emails, err := database.ListAuthorizedEmails(ctx)
		if err != nil {
			return err
		}
		if len(emails) > 0 {
			return nil
		}
	}
	return errors.New("create a local user or configure Google authentication with an authorized email before starting Redlaunch")
}

func readLocalPassword(input io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(input, 514))
	if err != nil {
		return "", errors.New("could not read password")
	}
	password := strings.TrimSuffix(string(data), "\n")
	if err := auth.ValidatePassword(password); err != nil {
		return "", err
	}
	return password, nil
}

func runValidatePassword(args []string, input io.Reader) error {
	flags := flag.NewFlagSet("validate credentials", flag.ContinueOnError)
	email := flags.String("email", "", "user email address")
	stdin := flags.Bool("password-stdin", false, "read password from stdin")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*stdin || flags.NArg() != 0 {
		return errors.New("use --email ADDRESS --password-stdin")
	}
	if _, err := application.ValidateEmail(*email); err != nil {
		return err
	}
	_, err := readLocalPassword(input)
	return err
}
