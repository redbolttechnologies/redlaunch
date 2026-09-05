package application

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr error
	}{
		{name: "trims whitespace", value: " Status page ", want: "Status page"},
		{name: "required", value: "   ", wantErr: ErrNameRequired},
		{name: "too long", value: strings.Repeat("a", MaxNameLength+1), wantErr: ErrNameTooLong},
		{name: "control character", value: "status\npage", wantErr: ErrNameInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateName(tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateName() error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("ValidateName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateEmail(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr error
	}{
		{name: "trims and normalizes", value: " Admin@Example.COM ", want: "admin@example.com"},
		{name: "plus address", value: "admin+redlaunch@example.com", want: "admin+redlaunch@example.com"},
		{name: "required", value: "   ", wantErr: ErrEmailRequired},
		{name: "display name rejected", value: "Admin <admin@example.com>", wantErr: ErrEmailInvalid},
		{name: "invalid address", value: "admin example.com", wantErr: ErrEmailInvalid},
		{name: "control character", value: "admin\n@example.com", wantErr: ErrEmailInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateEmail(tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateEmail() error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("ValidateEmail() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateFolderNameRejectsPathInput(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "parent", value: "../outside"},
		{name: "absolute", value: "/tmp/outside"},
		{name: "windows separator", value: `outside\nested`},
		{name: "dot", value: "."},
		{name: "dot dot", value: ".."},
		{name: "control character", value: "app\x00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ValidateFolderName(tt.value); err == nil {
				t.Fatalf("ValidateFolderName(%q) error = nil, want an error", tt.value)
			}
		})
	}

	got, err := ValidateFolderName(" status-page ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "status-page" {
		t.Fatalf("ValidateFolderName() = %q, want %q", got, "status-page")
	}
}

func TestValidateDomainName(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr error
	}{
		{name: "trims and normalizes", value: " Example.COM ", want: "example.com"},
		{name: "single label", value: "localhost", want: "localhost"},
		{name: "required", value: "   ", wantErr: ErrDomainNameRequired},
		{name: "empty label", value: "example..com", wantErr: ErrDomainNameInvalid},
		{name: "leading hyphen", value: "-example.com", wantErr: ErrDomainNameInvalid},
		{name: "trailing hyphen", value: "example-.com", wantErr: ErrDomainNameInvalid},
		{name: "invalid character", value: "example.com/path", wantErr: ErrDomainNameInvalid},
		{name: "too long", value: strings.Repeat("a", MaxDomainNameLength+1), wantErr: ErrDomainNameTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateDomainName(tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateDomainName() error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("ValidateDomainName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateRedlaunchPublicAccess(t *testing.T) {
	got, err := ValidateRedlaunchPublicAccess(RedlaunchPublicAccessInput{
		Enabled: true,
		Domain:  " Admin.Example.COM ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != (RedlaunchPublicAccess{Enabled: true, Domain: "admin.example.com"}) {
		t.Fatalf("ValidateRedlaunchPublicAccess() = %#v, want enabled normalized domain", got)
	}

	if _, err := ValidateRedlaunchPublicAccess(RedlaunchPublicAccessInput{Enabled: true}); !errors.Is(err, ErrRedlaunchPublicDomainRequired) {
		t.Fatalf("ValidateRedlaunchPublicAccess(missing domain) error = %v, want %v", err, ErrRedlaunchPublicDomainRequired)
	}
	if _, err := ValidateRedlaunchPublicAccess(RedlaunchPublicAccessInput{Domain: "bad/domain"}); !errors.Is(err, ErrRedlaunchPublicDomainInvalid) {
		t.Fatalf("ValidateRedlaunchPublicAccess(invalid domain) error = %v, want %v", err, ErrRedlaunchPublicDomainInvalid)
	}
	if got, err := ValidateRedlaunchPublicAccess(RedlaunchPublicAccessInput{}); err != nil || got != (RedlaunchPublicAccess{}) {
		t.Fatalf("ValidateRedlaunchPublicAccess(disabled empty) = (%#v, %v), want zero settings", got, err)
	}
}

func TestValidateRoutingValues(t *testing.T) {
	if got, err := ValidateRoutingSubdomain(" API.V1 "); err != nil || got != "api.v1" {
		t.Fatalf("ValidateRoutingSubdomain() = (%q, %v), want (api.v1, nil)", got, err)
	}
	if got, err := ValidateRoutingSubdomain(""); err != nil || got != "" {
		t.Fatalf("ValidateRoutingSubdomain(empty) = (%q, %v), want empty and nil", got, err)
	}
	for _, value := range []string{"api..example", "-api", "api-", "api example", "api/{host}"} {
		if _, err := ValidateRoutingSubdomain(value); !errors.Is(err, ErrRoutingSubdomainInvalid) {
			t.Errorf("ValidateRoutingSubdomain(%q) error = %v, want %v", value, err, ErrRoutingSubdomainInvalid)
		}
	}

	for _, testCase := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "root", value: " / ", want: "/"},
		{name: "path", value: " /register ", want: "/register"},
		{name: "matcher", value: "/api/*", want: "/api/*"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := ValidateRoutingPath(testCase.value)
			if err != nil {
				t.Fatalf("ValidateRoutingPath() error = %v", err)
			}
			if got != testCase.want {
				t.Fatalf("ValidateRoutingPath() = %q, want %q", got, testCase.want)
			}
		})
	}
	for _, value := range []string{"", "register", "/line\nfeed", "/unsafe{placeholder}", "/space path"} {
		if _, err := ValidateRoutingPath(value); err == nil {
			t.Errorf("ValidateRoutingPath(%q) error = nil, want an error", value)
		}
	}
}

func TestValidatePostgreSQLServiceInputFields(t *testing.T) {
	if got, err := ValidateServiceName(" db-primary "); err != nil || got != "db-primary" {
		t.Fatalf("ValidateServiceName() = (%q, %v), want (db-primary, nil)", got, err)
	}
	if _, err := ValidateServiceName("../db"); !errors.Is(err, ErrServiceNameInvalid) {
		t.Fatalf("ValidateServiceName(traversal) error = %v, want %v", err, ErrServiceNameInvalid)
	}
	if got, err := ValidatePostgresVersion(" 17-alpine "); err != nil || got != "17-alpine" {
		t.Fatalf("ValidatePostgresVersion() = (%q, %v), want (17-alpine, nil)", got, err)
	}
	if _, err := ValidatePostgresVersion("17\npostgres"); !errors.Is(err, ErrPostgresVersionInvalid) {
		t.Fatalf("ValidatePostgresVersion(injection) error = %v, want %v", err, ErrPostgresVersionInvalid)
	}
	if got, err := ValidateDatabaseName(" Status page "); err != nil || got != "Status page" {
		t.Fatalf("ValidateDatabaseName() = (%q, %v), want (Status page, nil)", got, err)
	}
	if _, err := ValidateDatabaseName("status\npage"); !errors.Is(err, ErrDatabaseNameInvalid) {
		t.Fatalf("ValidateDatabaseName(control character) error = %v, want %v", err, ErrDatabaseNameInvalid)
	}
	if _, err := ValidateDatabaseName(`status"page`); !errors.Is(err, ErrDatabaseNameInvalid) {
		t.Fatalf("ValidateDatabaseName(unsafe quote) error = %v, want %v", err, ErrDatabaseNameInvalid)
	}
	if got, err := ValidateDatabaseUser(" appuser "); err != nil || got != "appuser" {
		t.Fatalf("ValidateDatabaseUser() = (%q, %v), want (appuser, nil)", got, err)
	}
	if _, err := ValidateDatabaseUser("app-user"); !errors.Is(err, ErrDatabaseUserInvalid) {
		t.Fatalf("ValidateDatabaseUser(hyphen) error = %v, want %v", err, ErrDatabaseUserInvalid)
	}
	if got, err := ValidateDatabasePassword("pa$$ word"); err != nil || got != "pa$$ word" {
		t.Fatalf("ValidateDatabasePassword() = (%q, %v), want supplied password", got, err)
	}
	if _, err := ValidateDatabasePassword("password\nvalue"); !errors.Is(err, ErrDatabasePasswordInvalid) {
		t.Fatalf("ValidateDatabasePassword(control character) error = %v, want %v", err, ErrDatabasePasswordInvalid)
	}
}

func TestValidateRedisServiceInputFields(t *testing.T) {
	if got, err := ValidateRedisVersion(" 7-alpine "); err != nil || got != "7-alpine" {
		t.Fatalf("ValidateRedisVersion() = (%q, %v), want (7-alpine, nil)", got, err)
	}
	if _, err := ValidateRedisVersion("7\nredis"); !errors.Is(err, ErrRedisVersionInvalid) {
		t.Fatalf("ValidateRedisVersion(injection) error = %v, want %v", err, ErrRedisVersionInvalid)
	}
	if got, err := ValidateRedisPort(" 06379 "); err != nil || got != "6379" {
		t.Fatalf("ValidateRedisPort() = (%q, %v), want (6379, nil)", got, err)
	}
	for _, value := range []string{"", "0", "65536", "6379/tcp", "123456"} {
		if _, err := ValidateRedisPort(value); err == nil {
			t.Errorf("ValidateRedisPort(%q) error = nil, want an error", value)
		}
	}
	if got, err := ValidateRedisPassword("pa$$ word"); err != nil || got != "pa$$ word" {
		t.Fatalf("ValidateRedisPassword() = (%q, %v), want supplied password", got, err)
	}
	if _, err := ValidateRedisPassword("password\nvalue"); !errors.Is(err, ErrRedisPasswordInvalid) {
		t.Fatalf("ValidateRedisPassword(control character) error = %v, want %v", err, ErrRedisPasswordInvalid)
	}
}

func TestValidateImageName(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr error
	}{
		{name: "local registry image", value: " localhost:5000/app:latest ", want: "localhost:5000/app:latest"},
		{name: "external registry image", value: "ghcr.io/example/status-page:v1.2", want: "ghcr.io/example/status-page:v1.2"},
		{name: "implicit latest tag", value: "nginx", want: "nginx"},
		{name: "digest", value: "example/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", want: "example/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{name: "required", value: "   ", wantErr: ErrImageNameRequired},
		{name: "too long", value: strings.Repeat("a", MaxImageNameLength+1), wantErr: ErrImageNameTooLong},
		{name: "path traversal", value: "../outside", wantErr: ErrImageNameInvalid},
		{name: "url", value: "https://registry.example.com/app:latest", wantErr: ErrImageNameInvalid},
		{name: "whitespace", value: "registry.example.com/app:latest tag", wantErr: ErrImageNameInvalid},
		{name: "yaml syntax", value: "registry.example.com/app:latest\nlabels:", wantErr: ErrImageNameInvalid},
		{name: "uppercase repository", value: "Example/App:latest", wantErr: ErrImageNameInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateImageName(tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateImageName() error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("ValidateImageName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDefaultApplicationImageName(t *testing.T) {
	if got := DefaultApplicationImageName(" web "); got != "localhost:5000/web:latest" {
		t.Fatalf("DefaultApplicationImageName(web) = %q, want localhost registry image", got)
	}
	if got := DefaultApplicationImageName("WEB"); got != "localhost:5000/web:latest" {
		t.Fatalf("DefaultApplicationImageName(uppercase service) = %q, want lowercase image repository", got)
	}
	if got := DefaultApplicationImageName("bad/name"); got != "localhost:5000/app:latest" {
		t.Fatalf("DefaultApplicationImageName(invalid) = %q, want fallback image", got)
	}
}

func TestIsSensitiveEnvironmentKey(t *testing.T) {
	for _, key := range []string{"POSTGRES_PASSWORD", "API-TOKEN", "database.url", "AWS_ACCESS_KEY_ID"} {
		if !IsSensitiveEnvironmentKey(key) {
			t.Errorf("IsSensitiveEnvironmentKey(%q) = false, want true", key)
		}
	}
	for _, key := range []string{"POSTGRES_DB", "POSTGRES_USER", "APP_ENV"} {
		if IsSensitiveEnvironmentKey(key) {
			t.Errorf("IsSensitiveEnvironmentKey(%q) = true, want false", key)
		}
	}
}

func TestValidateEnvironmentVariable(t *testing.T) {
	if got, err := ValidateEnvironmentVariableName(" APP_NAME "); err != nil || got != "APP_NAME" {
		t.Fatalf("ValidateEnvironmentVariableName() = (%q, %v), want (APP_NAME, nil)", got, err)
	}
	for _, value := range []string{"", "APP-NAME", "1APP", "../outside", "APP NAME"} {
		if _, err := ValidateEnvironmentVariableName(value); err == nil {
			t.Errorf("ValidateEnvironmentVariableName(%q) error = nil, want an error", value)
		}
	}
	if got, err := ValidateEnvironmentVariableValue("value with spaces # and $ signs"); err != nil || got != "value with spaces # and $ signs" {
		t.Fatalf("ValidateEnvironmentVariableValue() = (%q, %v), want unchanged value", got, err)
	}
	if _, err := ValidateEnvironmentVariableValue("line\nvalue"); !errors.Is(err, ErrEnvironmentVariableValueInvalid) {
		t.Fatalf("ValidateEnvironmentVariableValue(control character) error = %v, want %v", err, ErrEnvironmentVariableValueInvalid)
	}
}
