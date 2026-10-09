package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"redlaunch/internal/application"
)

type proxySettingsRepository interface {
	ListProxySettings(context.Context) (map[int64]application.ProxySettings, error)
	SaveProxySettings(context.Context, int64, application.ProxySettings) error
}

func (s *Applications) GetProxySettings(ctx context.Context, id int64) (application.ProxySettings, application.ProxySettings, error) {
	if id != 0 {
		if s.detailsRepository == nil {
			return application.ProxySettings{}, application.ProxySettings{}, application.ErrNotFound
		}
		if _, err := s.detailsRepository.Get(ctx, id); err != nil {
			return application.ProxySettings{}, application.ProxySettings{}, err
		}
	}
	repo, ok := s.repository.(proxySettingsRepository)
	if !ok {
		return application.ProxySettings{}, application.ProxySettings{}, errors.New("proxy settings repository is not configured")
	}
	all, err := repo.ListProxySettings(ctx)
	return all[id], all[0], err
}

func (s *Applications) SaveProxySettings(ctx context.Context, id int64, settings application.ProxySettings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	// Hold the application lease before the shared proxy lease, as routing does.
	if id != 0 {
		lease, err := s.acquireApplicationProject(ctx, id)
		if err != nil {
			return err
		}
		defer lease.release()
		if s.detailsRepository == nil {
			return application.ErrNotFound
		}
		if _, err := s.detailsRepository.Get(ctx, id); err != nil {
			return err
		}
	}
	lease, err := s.acquireProxyProject(ctx)
	if err != nil {
		return err
	}
	defer lease.release()
	repo, ok := s.repository.(proxySettingsRepository)
	if !ok {
		return errors.New("proxy settings repository is not configured")
	}
	all, err := repo.ListProxySettings(ctx)
	if err != nil {
		return err
	}
	if err := repo.SaveProxySettings(ctx, id, settings); err != nil {
		return err
	}
	if err := s.refreshProxyConfigurationLocked(ctx); err != nil {
		if rollbackErr := repo.SaveProxySettings(context.WithoutCancel(ctx), id, all[id]); rollbackErr != nil {
			return fmt.Errorf("apply proxy settings: %w (restore settings: %v)", err, rollbackErr)
		}
		return err
	}
	return nil
}

func writeProxyPolicy(b *strings.Builder, p application.ProxySettings, upstream string) {
	if c := p.Compression; c != nil {
		formats := []string{}
		if c.Zstandard {
			formats = append(formats, "zstd")
		}
		if c.Brotli {
			formats = append(formats, "br")
		}
		if c.Gzip {
			formats = append(formats, "gzip")
		}
		if len(formats) > 0 {
			fmt.Fprintf(b, "        encode %s {\n            minimum_length %d\n        }\n", strings.Join(formats, " "), c.MinimumLength)
		}
	}
	if h := p.SecurityHeaders; h != nil {
		for _, header := range [][2]string{{"X-Content-Type-Options", h.ContentTypeOptions}, {"X-Frame-Options", h.FrameOptions}, {"Referrer-Policy", h.ReferrerPolicy}, {"Content-Security-Policy", h.ContentSecurityPolicy}, {"Permissions-Policy", h.PermissionsPolicy}, {"Strict-Transport-Security", h.StrictTransportSecurity}} {
			if header[1] != "" {
				fmt.Fprintf(b, "        header >%s %s\n", header[0], strconv.Quote(header[1]))
			}
		}
	}
	if p.CacheControl != nil && *p.CacheControl != "" {
		fmt.Fprintf(b, "        header >Cache-Control %s\n", strconv.Quote(*p.CacheControl))
	}
	if p.BodyLimit != nil && p.BodyLimit.Bytes > 0 {
		fmt.Fprintf(b, "        request_body {\n            max_size %d\n        }\n", p.BodyLimit.Bytes)
	}
	fmt.Fprintf(b, "        reverse_proxy %s", upstream)
	if t := p.Timeouts; t != nil && (t.Dial != "" || t.ResponseHeader != "" || t.Read != "" || t.Write != "") {
		b.WriteString(" {\n            transport http {\n")
		for _, timeout := range [][2]string{{"dial_timeout", t.Dial}, {"response_header_timeout", t.ResponseHeader}, {"read_timeout", t.Read}, {"write_timeout", t.Write}} {
			if timeout[1] != "" {
				fmt.Fprintf(b, "                %s %s\n", timeout[0], timeout[1])
			}
		}
		b.WriteString("            }\n        }")
	}
	b.WriteString("\n")
}

const brotliProxyDockerfile = `FROM caddy:2.11.4-builder-alpine AS builder
RUN xcaddy build v2.11.4 --with github.com/ueffel/caddy-brotli@dfd1bbe545f74a749a7b0b503468d1c858587420
FROM caddy:2.11.4-alpine
COPY --from=builder /usr/bin/caddy /usr/bin/caddy
`

// Upgrade only the known managed image; never silently replace a custom proxy.
func enableProxyBrotli(directory, contents string) (string, bool, error) {
	if strings.Contains(contents, "    image: redlaunch-caddy:2.11.4-brotli") {
		return contents, false, nil
	}
	original := "    image: caddy:2.11.4-alpine"
	if !strings.Contains(contents, original) {
		return "", false, errors.New("Brotli requires the managed Caddy 2.11.4 image")
	}
	if strings.Contains(contents, "    build:") {
		return "", false, errors.New("Brotli cannot replace a custom proxy build")
	}
	if err := writeManagedFile(filepath.Join(directory, "Dockerfile.brotli.dockerignore"), "*\n", 0o644); err != nil {
		return "", false, err
	}
	if err := writeManagedFile(filepath.Join(directory, "Dockerfile.brotli"), brotliProxyDockerfile, 0o644); err != nil {
		return "", false, err
	}
	return strings.Replace(contents, original, "    image: redlaunch-caddy:2.11.4-brotli\n    pull_policy: build\n    build:\n      context: .\n      dockerfile: Dockerfile.brotli", 1), true, nil
}
