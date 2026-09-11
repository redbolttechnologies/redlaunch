// Command redlaunch runs the Redlaunch web application.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"redlaunch/internal/application"
	redlaunchauth "redlaunch/internal/auth"
	"redlaunch/internal/compose"
	"redlaunch/internal/config"
	"redlaunch/internal/handler"
	"redlaunch/internal/metrics"
	"redlaunch/internal/service"
	"redlaunch/internal/store"
	"redlaunch/internal/systemd"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "backup-run":
			err = runBackup(ctx, os.Args[2:])
		case "auth-add-email", "add-authorized-email":
			err = runAddAuthorizedEmail(ctx, os.Args[2:])
		case "compose-project-name":
			err = runComposeProjectName(os.Args[2:], os.Stdout)
		default:
			err = run(ctx)
		}
	} else {
		err = run(ctx)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runComposeProjectName(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("redlaunch compose-project-name", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	directory := flags.String("directory", "", "managed project directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*directory) == "" {
		return errors.New("compose project directory is required")
	}
	if flags.NArg() != 0 {
		return errors.New("compose-project-name accepts no positional arguments")
	}
	_, err := fmt.Fprintln(output, compose.ProjectName(*directory))
	return err
}

func run(ctx context.Context) error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.GoogleAuthEnabled() {
		return errors.New("Google authentication must be configured before starting Redlaunch")
	}

	setupService, err := service.NewSetupService(cfg.ProjectsRoot, compose.CommandRunner{})
	if err != nil {
		return fmt.Errorf("create setup service: %w", err)
	}
	if err := setupService.Initialize(); err != nil {
		return fmt.Errorf("initialize application directories: %w", err)
	}

	database, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("open application database: %w", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			logger.Error("close application database", "error", err)
		}
	}()

	applications, err := service.NewApplications(database, cfg.ProjectsRoot, compose.CommandRunner{})
	if err != nil {
		return fmt.Errorf("create application service: %w", err)
	}
	githubActions, err := service.NewGitHubActionsService(
		cfg.ProjectsRoot,
		database,
		applications,
		setupService,
		compose.CommandRunner{},
		service.CommandSSHKeyGenerator{},
	)
	if err != nil {
		return fmt.Errorf("create GitHub Actions service: %w", err)
	}
	systemdManager, err := systemd.NewManagerWithScope(cfg.SystemdUnitDirectory, cfg.SystemdBinary, cfg.SystemdScope)
	if err != nil {
		return fmt.Errorf("create systemd manager: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve Redlaunch executable: %w", err)
	}
	executablePrefix := []string(nil)
	if cfg.BackupContainerName != "" {
		executablePrefix = []string{cfg.BackupDockerBinary, "exec", cfg.BackupContainerName}
	}
	backupManager, err := service.NewBackupService(database, service.BackupConfig{
		ProjectsRoot:     cfg.ProjectsRoot,
		BackupRoot:       cfg.BackupRoot,
		DatabasePath:     cfg.DatabasePath,
		Executable:       executable,
		ExecutablePrefix: executablePrefix,
		Runner:           compose.CommandRunner{},
		Scheduler:        systemdManager,
	})
	if err != nil {
		return fmt.Errorf("create backup service: %w", err)
	}

	googleAuth, err := redlaunchauth.New(redlaunchauth.Config{
		ClientID:      cfg.GoogleClientID,
		ClientSecret:  cfg.GoogleClientSecret,
		RedirectURL:   cfg.GoogleRedirectURL,
		SessionSecret: cfg.AuthSessionSecret,
		CookieSecure:  cfg.AuthCookieSecure,
	}, database)
	if err != nil {
		return fmt.Errorf("create Google authentication service: %w", err)
	}

	dependencies := []any{setupService, applications, backupManager, githubActions, metrics.New()}
	dependencies = append(dependencies, googleAuth)
	web, err := handler.New(logger, dependencies...)
	if err != nil {
		return fmt.Errorf("create web handler: %w", err)
	}
	defer func() {
		jobsCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		if err := web.Shutdown(jobsCtx); err != nil {
			logger.Error("shutdown background jobs", "error", err)
		}
	}()

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           web.Routes(),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("server listening", "addr", cfg.HTTPAddr)
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		serverShutdownCtx, cancelServerShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		serverShutdownErr := server.Shutdown(serverShutdownCtx)
		cancelServerShutdown()
		jobsShutdownCtx, cancelJobsShutdown := context.WithTimeout(context.Background(), 35*time.Second)
		jobsShutdownErr := web.Shutdown(jobsShutdownCtx)
		cancelJobsShutdown()
		if serverShutdownErr != nil || jobsShutdownErr != nil {
			var shutdownErr error
			if serverShutdownErr != nil {
				shutdownErr = fmt.Errorf("shutdown HTTP server: %w", serverShutdownErr)
			}
			if jobsShutdownErr != nil {
				shutdownErr = errors.Join(shutdownErr, fmt.Errorf("shutdown background jobs: %w", jobsShutdownErr))
			}
			return shutdownErr
		}
		return nil
	}
}

func runAddAuthorizedEmail(ctx context.Context, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	flags := flag.NewFlagSet("redlaunch auth-add-email", flag.ContinueOnError)
	email := flags.String("email", "", "Google email address to authorize")
	databasePath := flags.String("db-path", cfg.DatabasePath, "SQLite database path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*email) == "" && flags.NArg() == 1 {
		*email = flags.Arg(0)
	}
	normalizedEmail, err := application.ValidateEmail(*email)
	if err != nil {
		return fmt.Errorf("authorize email: %w", err)
	}
	database, err := store.Open(ctx, *databasePath)
	if err != nil {
		return fmt.Errorf("open application database: %w", err)
	}
	defer database.Close()
	if err := database.AddAuthorizedEmail(ctx, normalizedEmail); err != nil {
		if errors.Is(err, application.ErrAuthorizedEmailAlreadyExists) {
			fmt.Printf("Authorized email already exists: %s\n", normalizedEmail)
			return nil
		}
		return err
	}
	fmt.Printf("Authorized email added: %s\n", normalizedEmail)
	return nil
}

func runBackup(ctx context.Context, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	flags := flag.NewFlagSet("redlaunch backup-run", flag.ContinueOnError)
	applicationID := flags.Int64("application-id", 0, "managed application ID")
	serviceID := flags.Int64("service-id", 0, "managed service ID")
	databasePath := flags.String("db-path", cfg.DatabasePath, "SQLite database path")
	projectsRoot := flags.String("projects-root", cfg.ProjectsRoot, "managed projects root")
	backupRoot := flags.String("backup-root", cfg.BackupRoot, "managed backup root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *applicationID < 1 || *serviceID < 1 {
		return errors.New("application-id and service-id must be positive")
	}
	database, err := store.Open(ctx, *databasePath)
	if err != nil {
		return fmt.Errorf("open application database: %w", err)
	}
	defer database.Close()
	backups, err := service.NewBackupService(database, service.BackupConfig{
		ProjectsRoot: *projectsRoot,
		BackupRoot:   *backupRoot,
		DatabasePath: *databasePath,
		Runner:       compose.CommandRunner{},
	})
	if err != nil {
		return fmt.Errorf("create backup service: %w", err)
	}
	if _, err := backups.RunScheduledBackup(ctx, *applicationID, *serviceID); err != nil {
		return err
	}
	return nil
}
