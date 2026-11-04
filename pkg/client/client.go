// Package client discovers an authentik OAuth2/OpenID provider.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxResponseBytes = 8 << 20

	// issuerPathMarker is where authentik serves each OAuth2/OpenID provider, as
	// /application/o/{application slug}/.
	issuerPathMarker = "/application/o/"
)

// Endpoints are derived from the configured, validated issuer.
type Endpoints struct {
	Issuer      string `json:"issuer"`
	UserInfoURL string `json:"userinfo_endpoint"`
}

// CanonicalIssuer checks that raw is the issuer of an authentik provider and returns it with the
// trailing slash that authentik puts in its issuer.
func CanonicalIssuer(raw string) (string, error) {
	u, err := parseBaseURL(raw)
	if err != nil {
		return "", fmt.Errorf("issuer %w", err)
	}
	path := strings.TrimSuffix(u.EscapedPath(), "/")
	_, slug, found := strings.CutLast(path, issuerPathMarker)
	if !found || slug == "" || strings.Contains(slug, "/") {
		return "", errors.New("issuer must end with /application/o/{application slug}/")
	}
	return u.Scheme + "://" + u.Host + path + "/", nil
}

// parseBaseURL validates an operator-supplied authentik URL.
func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	if u.Scheme == "http" && !strings.EqualFold(u.Hostname(), "localhost") && !net.ParseIP(u.Hostname()).IsLoopback() {
		return nil, errors.New("must use HTTPS except on a loopback host")
	}
	// Reject encoded slashes and dot segments that could change the path.
	for segment := range strings.SplitSeq(u.EscapedPath(), "/") {
		decoded, err := url.PathUnescape(segment)
		if err != nil || decoded == "." || decoded == ".." || strings.Contains(decoded, "/") || strings.Contains(decoded, "\\") {
			return nil, errors.New("contains an unsafe path segment")
		}
	}
	return u, nil
}

// Discover reads the provider's OpenID configuration. The issuer it reports must be the configured
// one, which means the provider must use authentik's default per-provider issuer mode.
func Discover(ctx context.Context, issuer string, httpClient *http.Client) (Endpoints, error) {
	var endpoints Endpoints
	canonical, err := CanonicalIssuer(issuer)
	if err != nil {
		return endpoints, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, canonical+".well-known/openid-configuration", nil)
	if err != nil {
		return endpoints, err
	}
	req.Header.Set("Accept", "application/json")
	response, err := transport(httpClient).Do(req)
	if err != nil {
		return endpoints, fmt.Errorf("discover provider: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return endpoints, fmt.Errorf("provider discovery returned HTTP %d", response.StatusCode)
	}
	if err := Decode(response.Body, &endpoints); err != nil {
		return endpoints, fmt.Errorf("decode provider discovery: %w", err)
	}
	if endpoints.Issuer != canonical {
		return Endpoints{}, fmt.Errorf("discovered issuer %q is not the configured issuer %q; set the provider's Issuer mode to \"Each provider has a different issuer\"", endpoints.Issuer, canonical)
	}
	origin, _ := url.Parse(canonical)
	u, err := url.Parse(endpoints.UserInfoURL)
	if err != nil || u.Scheme != origin.Scheme || u.Host != origin.Host || u.User != nil || u.Fragment != "" {
		return Endpoints{}, errors.New("discovered userinfo endpoint must use the issuer origin")
	}
	return endpoints, nil
}

func transport(c *http.Client) *http.Client {
	if c == nil {
		apiTransport := http.DefaultTransport.(*http.Transport).Clone()
		apiTransport.MaxIdleConnsPerHost = 32
		apiTransport.IdleConnTimeout = 90 * time.Second
		c = &http.Client{Timeout: 15 * time.Second, Transport: apiTransport}
	}
	cloned := *c
	// Every URL is known in advance, so redirects are never followed.
	cloned.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &cloned
}

// Decode limits both response size and trailing JSON; partial payloads are errors.
func Decode(reader io.Reader, out any) error {
	data, err := io.ReadAll(io.LimitReader(reader, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxResponseBytes {
		return errors.New("authentik response exceeds size limit")
	}
	return json.Unmarshal(data, out)
}
