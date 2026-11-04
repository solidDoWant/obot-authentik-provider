package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/solidDoWant/obot-authentik-provider/pkg/groups"
)

func TestMembershipHandlerReturnsSavedGroups(t *testing.T) {
	store := groups.NewMemory()
	saved := []groups.Group{{ID: "22222222-2222-4222-8222-222222222222", Name: "engineering"}}
	if err := store.SetUserGroups(t.Context(), testUserUUID, saved, time.Now()); err != nil {
		t.Fatal(err)
	}
	s := server{store: store}
	for user, want := range map[string]string{
		testUserUUID: `[{"id":"authentik/22222222-2222-4222-8222-222222222222","name":"engineering"}]`,
		"unknown":    `[]`,
	} {
		response := httptest.NewRecorder()
		s.listUserGroups(response, httptest.NewRequest(http.MethodPost, "/obot-list-user-auth-groups", strings.NewReader(user+"\n")))
		if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != want {
			t.Fatalf("%s: status %d body %s, want %s", user, response.Code, response.Body.String(), want)
		}
	}
}

func TestMembershipHandlerRejectsOversizedBody(t *testing.T) {
	s := server{}
	response := httptest.NewRecorder()
	s.listUserGroups(response, httptest.NewRequest(http.MethodPost, "/obot-list-user-auth-groups", strings.NewReader(strings.Repeat("a", 4097))))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status %d", response.Code)
	}
}

func TestLoggingConfiguration(t *testing.T) {
	for _, enabled := range []string{"", "false", "true", "TRUE"} {
		t.Run(enabled, func(t *testing.T) {
			opts := testOptions()
			opts.LoggingEnabled = enabled
			proxyOpts, err := buildOAuthProxyOptions(opts)
			if err != nil {
				t.Fatal(err)
			}
			expected := strings.EqualFold(enabled, "true")
			if proxyOpts.Logging.AuthEnabled != expected || proxyOpts.Logging.RequestEnabled != expected || proxyOpts.Logging.StandardEnabled != expected {
				t.Fatal("logging flag not applied consistently")
			}
		})
	}
}
