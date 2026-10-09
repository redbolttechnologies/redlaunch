package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"redlaunch/internal/application"
)

type proxySettingsService interface {
	GetProxySettings(context.Context, int64) (application.ProxySettings, application.ProxySettings, error)
	SaveProxySettings(context.Context, int64, application.ProxySettings) error
}
type proxySettingsField struct {
	Name, Label, Help, Value, Type string
	Checked                        bool
}
type proxySettingsCategory struct {
	Name, Label string
	Enabled     bool
	Fields      []proxySettingsField
}
type proxySettingsPageData struct {
	ApplicationID                              int64
	ApplicationName, CSRFToken, Error, BackURL string
	Categories                                 []proxySettingsCategory
}

func proxyPolicyCategories(p, global application.ProxySettings) []proxySettingsCategory {
	effective := p.Resolve(global)
	categories := []proxySettingsCategory{{Name: "compression", Label: "Compression", Enabled: p.Compression != nil}, {Name: "security", Label: "Security headers", Enabled: p.SecurityHeaders != nil}, {Name: "body", Label: "Request body limit", Enabled: p.BodyLimit != nil}, {Name: "timeouts", Label: "Upstream timeouts", Enabled: p.Timeouts != nil}, {Name: "cache", Label: "Cache-Control", Enabled: p.CacheControl != nil}}
	c := effective.Compression
	if c == nil {
		c = &application.ProxyCompression{MinimumLength: 512}
	}
	categories[0].Fields = []proxySettingsField{{Name: "gzip", Label: "gzip", Type: "checkbox", Checked: c.Gzip}, {Name: "zstandard", Label: "Zstandard", Type: "checkbox", Checked: c.Zstandard}, {Name: "brotli", Label: "Brotli", Type: "checkbox", Checked: c.Brotli, Help: "Enabling Brotli builds the managed proxy with a pinned compression extension."}, {Name: "minimum_length", Label: "Minimum response size (bytes)", Type: "number", Value: strconv.Itoa(c.MinimumLength), Help: "0 uses Caddy’s 512-byte default. Maximum: 1048576 bytes."}}
	h := effective.SecurityHeaders
	if h == nil {
		h = &application.ProxySecurityHeaders{}
	}
	categories[1].Fields = []proxySettingsField{{Name: "content_type_options", Label: "X-Content-Type-Options", Value: h.ContentTypeOptions, Help: "nosniff or empty"}, {Name: "frame_options", Label: "X-Frame-Options", Value: h.FrameOptions, Help: "DENY, SAMEORIGIN or empty"}, {Name: "referrer_policy", Label: "Referrer-Policy", Value: h.ReferrerPolicy}, {Name: "content_security_policy", Label: "Content-Security-Policy", Value: h.ContentSecurityPolicy}, {Name: "permissions_policy", Label: "Permissions-Policy", Value: h.PermissionsPolicy}, {Name: "strict_transport_security", Label: "Strict-Transport-Security", Value: h.StrictTransportSecurity}}
	limit := int64(0)
	if effective.BodyLimit != nil {
		limit = effective.BodyLimit.Bytes
	}
	categories[2].Fields = []proxySettingsField{{Name: "body_bytes", Label: "Maximum request body size (bytes)", Type: "number", Value: strconv.FormatInt(limit, 10), Help: "0 leaves the body size unlimited. Maximum: 1099511627776 bytes."}}
	t := effective.Timeouts
	if t == nil {
		t = &application.ProxyTimeouts{}
	}
	categories[3].Fields = []proxySettingsField{{Name: "dial_timeout", Label: "Connect timeout", Value: t.Dial}, {Name: "response_header_timeout", Label: "Response header timeout", Value: t.ResponseHeader}, {Name: "read_timeout", Label: "Upstream read timeout", Value: t.Read}, {Name: "write_timeout", Label: "Upstream write timeout", Value: t.Write}}
	for i := range categories[3].Fields {
		categories[3].Fields[i].Help = "Duration such as 30s or 2m; blank uses Caddy defaults. 0s disables the timeout. Maximum: 24h."
	}
	categories[3].Fields[0].Help = "Duration such as 3s; blank or 0s uses Caddy’s 3s connect timeout. Maximum: 24h."
	cache := ""
	if effective.CacheControl != nil {
		cache = *effective.CacheControl
	}
	categories[4].Fields = []proxySettingsField{{Name: "cache_control", Label: "Cache-Control response header", Value: cache, Help: "For example: private, no-store. Blank preserves the upstream header. Applies to all responses for this app; public caching should only be used for public content."}}
	return categories
}

func parseProxyPolicy(r *http.Request) (application.ProxySettings, error) {
	p := application.ProxySettings{}
	number := func(name string) (int64, error) {
		v, err := strconv.ParseInt(r.Form.Get(name), 10, 64)
		if err != nil {
			return 0, application.ErrInvalidProxySettings
		}
		return v, nil
	}
	if r.Form.Get("compression_enabled") == "on" {
		v, err := number("minimum_length")
		if err != nil || v < 0 || v > 1048576 {
			return p, application.ErrInvalidProxySettings
		}
		p.Compression = &application.ProxyCompression{Gzip: r.Form.Get("gzip") == "on", Zstandard: r.Form.Get("zstandard") == "on", Brotli: r.Form.Get("brotli") == "on", MinimumLength: int(v)}
	}
	if r.Form.Get("security_enabled") == "on" {
		p.SecurityHeaders = &application.ProxySecurityHeaders{ContentTypeOptions: r.Form.Get("content_type_options"), FrameOptions: r.Form.Get("frame_options"), ReferrerPolicy: r.Form.Get("referrer_policy"), ContentSecurityPolicy: r.Form.Get("content_security_policy"), PermissionsPolicy: r.Form.Get("permissions_policy"), StrictTransportSecurity: r.Form.Get("strict_transport_security")}
	}
	if r.Form.Get("body_enabled") == "on" {
		v, err := number("body_bytes")
		if err != nil {
			return p, err
		}
		p.BodyLimit = &application.ProxyBodyLimit{Bytes: v}
	}
	if r.Form.Get("timeouts_enabled") == "on" {
		p.Timeouts = &application.ProxyTimeouts{Dial: r.Form.Get("dial_timeout"), ResponseHeader: r.Form.Get("response_header_timeout"), Read: r.Form.Get("read_timeout"), Write: r.Form.Get("write_timeout")}
	}
	if r.Form.Get("cache_enabled") == "on" {
		v := r.Form.Get("cache_control")
		p.CacheControl = &v
	}
	return p, p.Validate()
}

func (h *Handler) proxySettingsPage(w http.ResponseWriter, r *http.Request) {
	manager, ok := h.applicationDetails.(proxySettingsService)
	if !ok {
		http.Error(w, "Proxy settings are not configured.", http.StatusServiceUnavailable)
		return
	}
	id := int64(0)
	data := proxySettingsPageData{BackURL: "/proxy"}
	if r.PathValue("id") != "" {
		var err error
		id, err = strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id < 1 {
			http.NotFound(w, r)
			return
		}
		item, err := h.applicationDetails.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, application.ErrNotFound) {
				http.NotFound(w, r)
			} else {
				http.Error(w, "The application could not be read.", 500)
			}
			return
		}
		data.ApplicationName = item.Name
		data.BackURL = "/applications/" + strconv.FormatInt(id, 10)
	}
	data.ApplicationID = id
	p, global, err := manager.GetProxySettings(r.Context(), id)
	if err != nil {
		http.Error(w, "Proxy settings could not be read.", 500)
		return
	}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		if err := parseBoundedForm(w, r); err != nil {
			http.Error(w, "Invalid settings form.", 400)
			return
		}
		if !h.validRequestCSRF(r) {
			http.Error(w, "This settings page expired. Refresh and try again.", 403)
			return
		}
		submitted, err := parseProxyPolicy(r)
		if err != nil {
			status = http.StatusBadRequest
			data.Error = "Invalid proxy settings. Check the values and try again."
			data.Categories = proxyPolicyCategories(submitted, global)
			// Preserve exactly what was submitted, including invalid numeric values.
			for i := range data.Categories {
				category := &data.Categories[i]
				category.Enabled = r.Form.Get(category.Name+"_enabled") == "on"
				for j := range category.Fields {
					f := &category.Fields[j]
					f.Value = r.Form.Get(f.Name)
					f.Checked = r.Form.Get(f.Name) == "on"
				}
			}
		} else {
			job, created, err := h.proxyActionJobs.createUnique("settings")
			if err != nil || !created {
				http.Error(w, "Another proxy operation is already running. Try again shortly.", http.StatusConflict)
				return
			}
			job.mu.Lock()
			job.closeURL = r.URL.Path
			job.mu.Unlock()
			if err := h.startTrackedJob("proxy-action", func(ctx context.Context) {
				if err := manager.SaveProxySettings(ctx, id, submitted); err != nil {
					job.fail(err)
					h.logger.Error("apply proxy settings", "application_id", id, "error", err)
					return
				}
				job.complete()
			}); err != nil {
				job.fail(err)
				http.Error(w, "The operation system is busy. Try again shortly.", http.StatusServiceUnavailable)
				return
			}
			http.Redirect(w, r, r.URL.Path+"?proxy_action_job="+url.QueryEscape(job.id), http.StatusSeeOther)
			return
		}
	}
	if data.Categories == nil {
		data.Categories = proxyPolicyCategories(p, global)
	}
	data.CSRFToken = h.setCSRFCookie(w, r)
	w.Header().Set("Cache-Control", "no-store")
	page := h.shellPageData(r)
	page.ActivePage = "proxy"
	if id > 0 {
		page.ActivePage = "applications"
	}
	page.ProxySettingsPage = &data
	page.ProxyActionToasts = h.proxyActionToastsForPage(r.URL.Query().Get("proxy_action_job"))
	h.writeTemplateStatus(w, "proxy-settings.html", page, status)
}
