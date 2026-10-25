package application

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestProxySettingsInheritanceAndDisable(t *testing.T) {
	cache := "private, no-store"
	global := ProxySettings{Compression: &ProxyCompression{Gzip: true}, BodyLimit: &ProxyBodyLimit{Bytes: 1024}, CacheControl: &cache}
	override := ProxySettings{Compression: &ProxyCompression{}, BodyLimit: &ProxyBodyLimit{}}
	effective := override.Resolve(global)
	if effective.Compression.Gzip || effective.BodyLimit.Bytes != 0 || *effective.CacheControl != cache {
		t.Fatalf("incorrect resolution: %#v", effective)
	}
}

func TestProxySettingsInputRoundTrip(t *testing.T) {
	cache := "private, no-store"
	settings := ProxySettings{
		Compression:     &ProxyCompression{Gzip: true, Zstandard: true, Brotli: true, MinimumLength: 512},
		SecurityHeaders: &ProxySecurityHeaders{ContentTypeOptions: "nosniff", FrameOptions: "DENY", ReferrerPolicy: "strict-origin-when-cross-origin", ContentSecurityPolicy: "default-src 'self'", PermissionsPolicy: "geolocation=()", StrictTransportSecurity: "max-age=31536000"},
		BodyLimit:       &ProxyBodyLimit{Bytes: 4096},
		Timeouts:        &ProxyTimeouts{Dial: "3s", ResponseHeader: "30s", Read: "1m", Write: "1m"},
		CacheControl:    &cache,
	}
	encoded, err := json.Marshal(ProxySettingsInputFrom(settings))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"security_headers"`, `"strict_transport_security"`, `"minimum_length"`, `"cache_control"`} {
		if !json.Valid(encoded) || !strings.Contains(string(encoded), want) {
			t.Fatalf("missing %q in %s", want, encoded)
		}
	}
	var input ProxySettingsInput
	if err := json.Unmarshal(encoded, &input); err != nil {
		t.Fatal(err)
	}
	if got := ProxySettingsFromInput(input); !reflect.DeepEqual(got, settings) {
		t.Fatalf("round trip = %#v, want %#v", got, settings)
	}
	if got := ProxySettingsInputFrom(ProxySettings{}); got.Compression != nil || got.SecurityHeaders != nil || got.BodyLimit != nil || got.Timeouts != nil || got.CacheControl != nil {
		t.Fatalf("empty policy must keep every category unset: %#v", got)
	}
}

func TestProxySettingsRejectInvalidInput(t *testing.T) {
	for _, v := range []string{"no-store\nheader X-Evil yes", "{$SECRET}", "{http.request.header.Authorization}", "no-store\x00", "é"} {
		if err := (ProxySettings{CacheControl: &v}).Validate(); !errors.Is(err, ErrInvalidProxySettings) {
			t.Fatalf("accepted header %q: %v", v, err)
		}
	}
	for _, v := range []string{"-1s", "25h", "1s\n}", "infinity"} {
		if err := (ProxySettings{Timeouts: &ProxyTimeouts{Dial: v}}).Validate(); !errors.Is(err, ErrInvalidProxySettings) {
			t.Fatalf("accepted timeout %q: %v", v, err)
		}
	}
	if err := (ProxySettings{BodyLimit: &ProxyBodyLimit{Bytes: -1}}).Validate(); err == nil {
		t.Fatal("accepted negative body limit")
	}
	if err := (ProxySettings{SecurityHeaders: &ProxySecurityHeaders{FrameOptions: "ALLOWALL"}}).Validate(); err == nil {
		t.Fatal("accepted invalid frame options")
	}
}
