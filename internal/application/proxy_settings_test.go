package application

import (
	"errors"
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
