package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/solidDoWant/obot-authentik-provider/pkg/client"
)

func TestMembershipHandlerUnavailableIs403NotUnsupported404(t *testing.T) {
	for name, users := range map[string][]map[string]any{
		"missing":     {},
		"deactivated": {{"uuid": testUserUUID, "is_active": false, "groups": []string{}}},
	} {
		t.Run(name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/core/users/" || r.URL.Query().Get("uuid") != testUserUUID {
					t.Errorf("request %s", r.URL)
				}
				if err := json.NewEncoder(w).Encode(map[string]any{"results": users}); err != nil {
					t.Error(err)
				}
			}))
			defer upstream.Close()
			s := server{directory: client.New(upstream.URL, "token", upstream.Client())}
			response := httptest.NewRecorder()
			s.listUserGroups(response, httptest.NewRequest(http.MethodPost, "/obot-list-user-auth-groups", strings.NewReader(testUserUUID+"\n")))
			if response.Code != http.StatusForbidden {
				t.Fatalf("status %d body %s", response.Code, response.Body.String())
			}
		})
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
