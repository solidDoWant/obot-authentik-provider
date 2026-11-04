package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCanonicalIssuer(t *testing.T) {
	for raw, want := range map[string]string{
		"https://auth.example/application/o/obot/":      "https://auth.example/application/o/obot/",
		" https://auth.example/application/o/obot ":     "https://auth.example/application/o/obot/",
		"https://auth.example/sso/application/o/obot/":  "https://auth.example/sso/application/o/obot/",
		"http://localhost:9000/application/o/obot/":     "http://localhost:9000/application/o/obot/",
		"https://auth.example/":                         "",
		"https://auth.example/application/o/":           "",
		"https://auth.example/application/o/obot/x/":    "",
		"http://auth.example/application/o/obot/":       "",
		"https://user@auth.example/application/o/obot/": "",
		"https://auth.example/application/o/obot/?a=b":  "",
		"https://auth.example/application/o/%2e%2e/":    "",
	} {
		got, err := CanonicalIssuer(raw)
		if want == "" && err == nil || want != "" && (err != nil || got != want) {
			t.Errorf("%q: got %q, error %v, want %q", raw, got, err, want)
		}
	}
}

func TestDiscover(t *testing.T) {
	var server *httptest.Server
	issuer := func() string { return server.URL + "/application/o/obot/" }
	discovered := map[string]any{}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/application/o/obot/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(discovered); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	discovered = map[string]any{"issuer": issuer(), "userinfo_endpoint": server.URL + "/application/o/userinfo/"}
	endpoints, err := Discover(t.Context(), strings.TrimSuffix(issuer(), "/"), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if endpoints.Issuer != issuer() || endpoints.UserInfoURL != server.URL+"/application/o/userinfo/" {
		t.Fatalf("endpoints %+v", endpoints)
	}

	// Global issuer mode reports the host as the issuer, which oauth2-proxy can't discover.
	discovered = map[string]any{"issuer": server.URL + "/", "userinfo_endpoint": server.URL + "/application/o/userinfo/"}
	if _, err := Discover(t.Context(), issuer(), server.Client()); err == nil || !strings.Contains(err.Error(), "Issuer mode") {
		t.Fatalf("global issuer mode: error %v", err)
	}

	discovered = map[string]any{"issuer": issuer(), "userinfo_endpoint": "https://elsewhere.example/userinfo/"}
	if _, err := Discover(t.Context(), issuer(), server.Client()); err == nil {
		t.Fatal("accepted a userinfo endpoint on another origin")
	}
}
