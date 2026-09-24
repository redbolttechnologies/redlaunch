package handler

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"redlaunch/internal/application"
)

func TestServiceSSHTunnelCommands(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		ports    string
		ip       string
		expected []string
	}{
		{
			name:     "single published port",
			ports:    "0.0.0.0:8080->80/tcp",
			ip:       "203.0.113.10",
			expected: []string{"ssh -N -L 8080:localhost:8080 root@203.0.113.10"},
		},
		{
			name:     "dual stack deduplicates host port",
			ports:    "0.0.0.0:8080->80/tcp, :::8080->80/tcp",
			ip:       "203.0.113.10",
			expected: []string{"ssh -N -L 8080:localhost:8080 root@203.0.113.10"},
		},
		{
			name:     "multiple published ports",
			ports:    "0.0.0.0:8080->80/tcp, 0.0.0.0:5432->5432/tcp",
			ip:       "203.0.113.10",
			expected: []string{"ssh -N -L 8080:localhost:8080 root@203.0.113.10", "ssh -N -L 5432:localhost:5432 root@203.0.113.10"},
		},
		{
			name:     "exposed without host binding",
			ports:    "5432/tcp",
			ip:       "203.0.113.10",
			expected: nil,
		},
		{
			name:     "empty ports",
			ports:    "",
			ip:       "203.0.113.10",
			expected: nil,
		},
		{
			name:     "empty ip",
			ports:    "0.0.0.0:8080->80/tcp",
			ip:       "",
			expected: nil,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := serviceSSHTunnelCommands(testCase.ports, testCase.ip); !reflect.DeepEqual(got, testCase.expected) {
				t.Fatalf("serviceSSHTunnelCommands(%q, %q) = %v, want %v", testCase.ports, testCase.ip, got, testCase.expected)
			}
		})
	}
}

func TestServiceDetailsRendersSSHTunnelCommand(t *testing.T) {
	applications := &fakeApplicationService{
		applications: []application.Application{{ID: 5, Name: "Demo", FolderName: "demo"}},
		services:     []application.Service{{ID: 1, ApplicationID: 5, Name: "pgadmin"}},
		serviceDetails: application.ServiceDetails{
			Service: application.Service{
				ID:                 1,
				ApplicationID:      5,
				Name:               "pgadmin",
				Type:               application.ServiceTypeApplication,
				ContainerName:      "redbolt-5-pgadmin",
				ContainerCreatedAt: time.Date(2026, time.August, 30, 7, 25, 29, 0, time.UTC),
				Status:             "Up 9 minutes",
				Ports:              "0.0.0.0:8080->80/tcp",
			},
			LogsAvailable: false,
		},
	}
	web, err := New(nil, applications)
	if err != nil {
		t.Fatal(err)
	}
	web.server.IPAddress = "203.0.113.10"

	recorder := httptest.NewRecorder()
	web.Routes().ServeHTTP(recorder, httptest.NewRequest("GET", "/applications/5/services/pgadmin", nil))
	if recorder.Code != 200 {
		t.Fatalf("GET service details status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	expected := "ssh -N -L 8080:localhost:8080 root@203.0.113.10"
	for _, want := range []string{
		`id="ssh-tunnel-title"`,
		expected,
		`data-copy-value="` + expected + `"`,
		`>Copy</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("GET service details did not render %q: %s", want, body)
		}
	}
}

func TestServiceDetailsOmitsSSHTunnelWithoutPublishedPort(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		ports string
	}{
		{name: "empty", ports: ""},
		{name: "exposed only", ports: "5432/tcp"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			applications := &fakeApplicationService{
				applications: []application.Application{{ID: 5, Name: "Demo", FolderName: "demo"}},
				services:     []application.Service{{ID: 1, ApplicationID: 5, Name: "pgadmin"}},
				serviceDetails: application.ServiceDetails{
					Service:       application.Service{ID: 1, ApplicationID: 5, Name: "pgadmin", Type: application.ServiceTypeApplication, Ports: testCase.ports},
					LogsAvailable: false,
				},
			}
			web, err := New(nil, applications)
			if err != nil {
				t.Fatal(err)
			}
			web.server.IPAddress = "203.0.113.10"

			recorder := httptest.NewRecorder()
			web.Routes().ServeHTTP(recorder, httptest.NewRequest("GET", "/applications/5/services/pgadmin", nil))
			if recorder.Code != 200 {
				t.Fatalf("GET service details status = %d, want 200", recorder.Code)
			}
			body := recorder.Body.String()
			if strings.Contains(body, "ssh-tunnel-title") || strings.Contains(body, "ssh -N -L") {
				t.Fatalf("GET service details rendered an SSH tunnel for ports %q: %s", testCase.ports, body)
			}
		})
	}
}
