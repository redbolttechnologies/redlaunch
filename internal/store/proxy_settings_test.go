package store

import (
	"reflect"
	"testing"

	"redlaunch/internal/application"
)

func TestProxySettingsPersistenceAndCascade(t *testing.T) {
	path := t.TempDir() + "/settings.db"
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	app, err := db.Create(t.Context(), application.Application{Name: "Proxy test", FolderName: "proxy-test"})
	if err != nil {
		t.Fatal(err)
	}
	cache := "private, no-store"
	global := application.ProxySettings{CacheControl: &cache, Compression: &application.ProxyCompression{Gzip: true, MinimumLength: 512}}
	override := application.ProxySettings{Compression: &application.ProxyCompression{}, BodyLimit: &application.ProxyBodyLimit{Bytes: 4096}}
	if err := db.SaveProxySettings(t.Context(), 0, global); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProxySettings(t.Context(), app.ID, override); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProxySettings(t.Context(), 99999, override); err == nil {
		t.Fatal("accepted missing application")
	}
	db.Close()
	db, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	all, err := db.ListProxySettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(all[0], global) || !reflect.DeepEqual(all[app.ID], override) {
		t.Fatalf("settings did not survive reopen: %#v", all)
	}
	if err := db.DeleteApplication(t.Context(), app.ID); err != nil {
		t.Fatal(err)
	}
	all, err = db.ListProxySettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || !reflect.DeepEqual(all[0], global) {
		t.Fatalf("incorrect cascade: %#v", all)
	}
}
