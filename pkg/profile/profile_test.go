package profile

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	userID = "11111111-1111-4111-8111-111111111111"
)

func TestGetUserInfo(t *testing.T) {
	for _, tc := range []struct {
		name     string
		userinfo map[string]any
		want     *UserInfo
		wantErr  string
	}{
		{
			name:     "full profile",
			userinfo: map[string]any{"sub": userID, "name": "Person", "preferred_username": "person", "picture": "https://img.example/a.png"},
			want:     &UserInfo{ID: userID, Name: "Person", IconURL: "https://img.example/a.png"},
		},
		{
			name:     "username and unsafe picture",
			userinfo: map[string]any{"sub": userID, "preferred_username": "person", "picture": "http://img.example/a.png"},
			want:     &UserInfo{ID: userID, Name: "person"},
		},
		{
			name:     "hashed subject",
			userinfo: map[string]any{"sub": strings.Repeat("a", 64), "email": "person@example.com"},
			want:     &UserInfo{ID: strings.Repeat("a", 64), Name: "person@example.com"},
		},
		{
			name:     "no subject",
			userinfo: map[string]any{"name": "Person"},
			wantErr:  "no subject",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer user-token" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if err := json.NewEncoder(w).Encode(tc.userinfo); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			got, err := GetUserInfo(t.Context(), "user-token", server.URL)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || *got != *tc.want {
				t.Fatalf("user %+v, error %v, want %+v", got, err, tc.want)
			}
		})
	}
}
