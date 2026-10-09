package application

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrInvalidProxySettings = errors.New("invalid proxy settings")

// Nil categories inherit the global category. Empty categories explicitly
// disable it (or retain Caddy's native defaults).
type ProxySettings struct {
	Compression     *ProxyCompression     `json:"compression,omitempty"`
	SecurityHeaders *ProxySecurityHeaders `json:"security_headers,omitempty"`
	BodyLimit       *ProxyBodyLimit       `json:"body_limit,omitempty"`
	Timeouts        *ProxyTimeouts        `json:"timeouts,omitempty"`
	CacheControl    *string               `json:"cache_control,omitempty"`
}
type ProxyCompression struct {
	Gzip          bool
	Zstandard     bool
	Brotli        bool
	MinimumLength int
}
type ProxySecurityHeaders struct {
	ContentTypeOptions      string
	FrameOptions            string
	ReferrerPolicy          string
	ContentSecurityPolicy   string
	PermissionsPolicy       string
	StrictTransportSecurity string
}
type ProxyBodyLimit struct{ Bytes int64 }
type ProxyTimeouts struct {
	Dial           string
	ResponseHeader string
	Read           string
	Write          string
}

func (p ProxySettings) Resolve(global ProxySettings) ProxySettings {
	if p.Compression == nil {
		p.Compression = global.Compression
	}
	if p.SecurityHeaders == nil {
		p.SecurityHeaders = global.SecurityHeaders
	}
	if p.BodyLimit == nil {
		p.BodyLimit = global.BodyLimit
	}
	if p.Timeouts == nil {
		p.Timeouts = global.Timeouts
	}
	if p.CacheControl == nil {
		p.CacheControl = global.CacheControl
	}
	return p
}

func (p ProxySettings) Validate() error {
	invalid := func(message string) error { return fmt.Errorf("%w: %s", ErrInvalidProxySettings, message) }
	if p.Compression != nil && (p.Compression.MinimumLength < 0 || p.Compression.MinimumLength > 1048576) {
		return invalid("compression minimum must be between 0 and 1048576 bytes")
	}
	if p.BodyLimit != nil && (p.BodyLimit.Bytes < 0 || p.BodyLimit.Bytes > 1<<40) {
		return invalid("body limit must be between 0 and 1099511627776 bytes")
	}
	if p.Timeouts != nil {
		for _, v := range []string{p.Timeouts.Dial, p.Timeouts.ResponseHeader, p.Timeouts.Read, p.Timeouts.Write} {
			if v == "" {
				continue
			}
			d, err := time.ParseDuration(v)
			if err != nil || d < 0 || d > 24*time.Hour {
				return invalid("timeouts must be durations between 0s and 24h")
			}
		}
	}
	values := []string{}
	if p.CacheControl != nil {
		values = append(values, *p.CacheControl)
	}
	if h := p.SecurityHeaders; h != nil {
		if h.ContentTypeOptions != "" && h.ContentTypeOptions != "nosniff" {
			return invalid("content type options must be nosniff or empty")
		}
		if h.FrameOptions != "" && h.FrameOptions != "DENY" && h.FrameOptions != "SAMEORIGIN" {
			return invalid("frame options must be DENY, SAMEORIGIN or empty")
		}
		values = append(values, h.ContentTypeOptions, h.FrameOptions, h.ReferrerPolicy, h.ContentSecurityPolicy, h.PermissionsPolicy, h.StrictTransportSecurity)
	}
	for _, value := range values {
		if len(value) > 4096 || strings.ContainsAny(value, "{}") {
			return invalid("header values must be at most 4096 bytes and cannot contain Caddy placeholders")
		}
		for _, c := range value {
			if c < 32 || c > 126 {
				return invalid("header values must contain printable ASCII only")
			}
		}
	}
	return nil
}
