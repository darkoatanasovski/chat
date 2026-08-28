package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type apiClient struct {
	baseURL string
	http    *http.Client
}

func newAPIClient(baseURL string) *apiClient {
	return &apiClient{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 10 * time.Second}}
}

func (c *apiClient) do(method, path, token string, body any, out any) error {
	return c.doAuth(method, path, func(req *http.Request) {
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}, body, out)
}

// doBasicAuth authenticates with an App's API key/secret (HTTP Basic) —
// used only by createUser, matching how a business's own backend calls
// POST /users on behalf of its end-users (cmd/api/middleware.go
// requireAppCredentials).
func (c *apiClient) doBasicAuth(method, path, key, secret string, body any, out any) error {
	return c.doAuth(method, path, func(req *http.Request) {
		req.SetBasicAuth(key, secret)
	}, body, out)
}

func (c *apiClient) doAuth(method, path string, setAuth func(*http.Request), body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, string(data))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

type createUserResp struct {
	UserID string `json:"user_id"`
	Token  string `json:"token"`
}

func (c *apiClient) createUser(displayName, region, appKey, appSecret string) (createUserResp, error) {
	var out createUserResp
	err := c.doBasicAuth(http.MethodPost, "/users", appKey, appSecret, map[string]string{
		"display_name": displayName,
		"region":       region,
	}, &out)
	return out, err
}

type createOrgResp struct {
	OrgID int64  `json:"org_id"`
	Token string `json:"token"`
}

type createAppResp struct {
	AppID      int64 `json:"app_id"`
	Credential struct {
		Key    string `json:"key"`
		Secret string `json:"secret"`
	} `json:"credential"`
}

// provisionApp creates a fresh Organization at the given tier and one App
// under it, returning the app's credentials — lets the tool run against any
// tier's limits without depending on manually seeded credentials.
func (c *apiClient) provisionApp(tier string) (key, secret string, err error) {
	var org createOrgResp
	if err := c.do(http.MethodPost, "/organizations", "", map[string]string{
		"name": "loadtest-org-" + uuid.NewString()[:8],
		"tier": tier,
	}, &org); err != nil {
		return "", "", fmt.Errorf("create org: %w", err)
	}
	var app createAppResp
	if err := c.do(http.MethodPost, fmt.Sprintf("/organizations/%d/apps", org.OrgID), org.Token, map[string]string{
		"name": "loadtest-app",
	}, &app); err != nil {
		return "", "", fmt.Errorf("create app: %w", err)
	}
	return app.Credential.Key, app.Credential.Secret, nil
}

type createChannelResp struct {
	ChannelID string `json:"channel_id"`
}

func (c *apiClient) createChannel(token, name string) (createChannelResp, error) {
	var out createChannelResp
	err := c.do(http.MethodPost, "/channels", token, map[string]string{"name": name}, &out)
	return out, err
}

func (c *apiClient) addMember(token, channelID, userID string) error {
	return c.do(http.MethodPost, "/channels/"+channelID+"/members", token, map[string]string{"user_id": userID}, nil)
}

// sendMessage returns (rateLimited, error). A 429 is expected behavior under
// load, not a failure of the tool.
func (c *apiClient) sendMessage(token, channelID, clientMessageID, body string) (rateLimited bool, err error) {
	err = c.do(http.MethodPost, "/channels/"+channelID+"/messages", token, map[string]string{
		"client_message_id": clientMessageID,
		"body":              body,
	}, nil)
	if err != nil && strings.Contains(err.Error(), "status 429") {
		return true, nil
	}
	return false, err
}
