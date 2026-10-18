// Package tado implements OAuth and read-only V3/V3+ and X discovery.
package tado

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cpanato/tado-bridge/internal/credentials"
	"github.com/cpanato/tado-bridge/internal/domain"
)

// Published by tado in its device-code authentication guide; not a secret.
const clientID = "1bb50063-6b0c-4d11-bd99-387f4a91cc46"

type Client struct {
	http                       *http.Client
	authURL, apiURL, xAPIURL   string
	now                        func() time.Time
	wait                       func(context.Context, time.Duration) error
	OAuthRequests, APIRequests int
}

func New() *Client {
	return &Client{
		http:    &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		authURL: "https://login.tado.com/oauth2", apiURL: "https://my.tado.com/api/v2",
		xAPIURL: "https://hops.tado.com",
		now:     time.Now, wait: wait,
	}
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type responseError struct {
	status int
	code   string
}

func (e *responseError) Error() string { return fmt.Sprintf("upstream HTTP %d", e.status) }

// Upstream bodies and transport URLs may contain secrets; never include them in errors.
func (c *Client) request(ctx context.Context, method, endpoint, bearer string, form url.Values, dst any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return errors.New("cannot construct upstream request")
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if form != nil {
		c.OAuthRequests++
	} else {
		c.APIRequests++
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("upstream request failed (network error or timeout)")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var payload struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload)
		return &responseError{status: resp.StatusCode, code: payload.Error}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(dst); err != nil {
		return errors.New("invalid upstream JSON response")
	}
	return nil
}

type deviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

func (c *Client) token(ctx context.Context, form url.Values, previousRefresh string) (credentials.Token, error) {
	var response tokenResponse
	form.Set("client_id", clientID)
	started := c.now()
	if err := c.request(ctx, http.MethodPost, c.authURL+"/token", "", form, &response); err != nil {
		return credentials.Token{}, err
	}
	if response.RefreshToken == "" {
		response.RefreshToken = previousRefresh
	}
	if response.AccessToken == "" || response.RefreshToken == "" || response.ExpiresIn <= 0 || !strings.EqualFold(response.TokenType, "Bearer") {
		return credentials.Token{}, errors.New("incomplete or unsupported OAuth token response")
	}
	return credentials.Token{AccessToken: response.AccessToken, RefreshToken: response.RefreshToken, ExpiresAt: started.Add(time.Duration(response.ExpiresIn) * time.Second).UTC()}, nil
}

func (c *Client) Login(ctx context.Context, show func(uri, code string) error) (credentials.Token, error) {
	var device deviceCode
	started := c.now()
	if err := c.request(ctx, http.MethodPost, c.authURL+"/device_authorize", "", url.Values{"client_id": {clientID}, "scope": {"offline_access"}}, &device); err != nil {
		return credentials.Token{}, err
	}
	uri, err := url.Parse(device.VerificationURI)
	if err != nil || uri.Scheme != "https" || uri.Host != "login.tado.com" || uri.User != nil || device.DeviceCode == "" || device.UserCode == "" || device.ExpiresIn <= 0 {
		return credentials.Token{}, errors.New("invalid device authorization response")
	}
	verification, err := device.verificationURL()
	if err != nil {
		return credentials.Token{}, err
	}
	if err := show(verification, device.UserCode); err != nil {
		return credentials.Token{}, err
	}
	ctx, cancel := context.WithDeadline(ctx, started.Add(time.Duration(device.ExpiresIn)*time.Second))
	defer cancel()
	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		if err := c.wait(ctx, interval); err != nil {
			return credentials.Token{}, err
		}
		token, err := c.token(ctx, url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {device.DeviceCode}}, "")
		if err == nil {
			return token, nil
		}
		var upstream *responseError
		if !errors.As(err, &upstream) || upstream.status != http.StatusBadRequest {
			return credentials.Token{}, err
		}
		switch upstream.code {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return credentials.Token{}, errors.New("device authorization denied")
		case "expired_token":
			return credentials.Token{}, errors.New("device authorization expired; run auth login again")
		default:
			return credentials.Token{}, err
		}
	}
}

// Prefer the provider's complete URL, preserving parameters needed by its login
// page. The bare verification URI may reject requests without a client ID.
func (device deviceCode) verificationURL() (string, error) {
	verification := device.VerificationURIComplete
	if verification == "" {
		verification = device.VerificationURI
	}
	uri, err := url.Parse(verification)
	if err != nil || uri.Scheme != "https" || uri.Host != "login.tado.com" || uri.User != nil || uri.Fragment != "" {
		return "", errors.New("invalid device verification URL")
	}
	query := uri.Query()
	if query.Get("client_id") == "" {
		query.Set("client_id", clientID)
	}
	if query.Get("user_code") == "" {
		query.Set("user_code", device.UserCode)
	}
	uri.RawQuery = query.Encode()
	return uri.String(), nil
}

func (c *Client) Refresh(ctx context.Context, token credentials.Token) (credentials.Token, error) {
	return c.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token.RefreshToken}}, token.RefreshToken)
}

// Discover deliberately copies only stable IDs and display names, excluding account
// email, addresses, device serials and all other upstream metadata.
func (c *Client) Discover(ctx context.Context, token credentials.Token) (domain.Discovery, error) {
	result := domain.Discovery{Provider: "tado", Homes: []domain.Home{}}
	var me struct {
		Homes []struct {
			ID   json.Number `json:"id"`
			Name string      `json:"name"`
		} `json:"homes"`
	}
	if err := c.request(ctx, http.MethodGet, c.apiURL+"/me", token.AccessToken, nil, &me); err != nil {
		return result, err
	}
	if me.Homes == nil {
		return result, errors.New("upstream response has no homes list")
	}
	for _, home := range me.Homes {
		if home.ID == "" {
			return result, errors.New("upstream home has no ID")
		}
		zones, err := c.discoverZones(ctx, token.AccessToken, string(home.ID))
		if err != nil {
			return result, err
		}
		result.Homes = append(result.Homes, domain.Home{ID: string(home.ID), Name: home.Name, Zones: zones})
	}
	return result, nil
}

func (c *Client) discoverZones(ctx context.Context, bearer, homeID string) ([]domain.Zone, error) {
	homePath := "/homes/" + url.PathEscape(homeID)
	var home struct {
		Generation string `json:"generation"`
	}
	if err := c.request(ctx, http.MethodGet, c.apiURL+homePath, bearer, nil, &home); err != nil {
		return nil, err
	}
	zones := []domain.Zone{}
	if home.Generation == "LINE_X" {
		var response struct {
			Rooms []struct {
				ID   json.Number `json:"roomId"`
				Name string      `json:"roomName"`
			} `json:"rooms"`
		}
		if err := c.request(ctx, http.MethodGet, c.xAPIURL+homePath+"/roomsAndDevices", bearer, nil, &response); err != nil {
			return nil, err
		}
		if response.Rooms == nil {
			return nil, errors.New("upstream X response has no rooms list")
		}
		for _, room := range response.Rooms {
			if room.ID == "" {
				return nil, errors.New("upstream room has no ID")
			}
			zones = append(zones, domain.Zone{ID: string(room.ID), Name: room.Name})
		}
		return zones, nil
	}
	var response []struct {
		ID   json.Number `json:"id"`
		Name string      `json:"name"`
	}
	if err := c.request(ctx, http.MethodGet, c.apiURL+homePath+"/zones", bearer, nil, &response); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, errors.New("upstream response has no zones list")
	}
	for _, zone := range response {
		if zone.ID == "" {
			return nil, errors.New("upstream zone has no ID")
		}
		zones = append(zones, domain.Zone{ID: string(zone.ID), Name: zone.Name})
	}
	return zones, nil
}
