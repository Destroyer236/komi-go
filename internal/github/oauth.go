package github

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
)

// DeviceCode is the payload returned by GitHub's device authorization endpoint.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// StartDeviceFlow begins the OAuth device flow. clientID comes from the user's
// GitHub OAuth App; the app deliberately does not ship a client secret.
func (c *Client) StartDeviceFlow(ctx context.Context, clientID, scopes string) (DeviceCode, error) {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return DeviceCode{}, errors.New("请先在设置中填写 GitHub OAuth App 的 client_id")
	}
	if scopes == "" {
		scopes = "public_repo read:user"
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scope", scopes)
	res, err := c.oauthPost(ctx, "/login/device/code", form)
	if err != nil {
		return DeviceCode{}, err
	}
	var code DeviceCode
	if err := json.Unmarshal(res, &code); err != nil {
		return DeviceCode{}, errors.New("设备码响应格式异常")
	}
	if code.DeviceCode == "" {
		return DeviceCode{}, errors.New("GitHub 没有返回设备码，请检查 client_id")
	}
	if code.VerificationURI == "" {
		code.VerificationURI = "https://" + c.webHost() + "/login/device"
	}
	if code.Interval <= 0 {
		code.Interval = 5
	}
	return code, nil
}

// PollDeviceFlow returns accessToken when the user approved the code. pending
// means "keep polling"; slowDown asks the caller to increase the interval.
func (c *Client) PollDeviceFlow(ctx context.Context, clientID, deviceCode string) (token string, pending bool, slowDown bool, err error) {
	form := url.Values{}
	form.Set("client_id", strings.TrimSpace(clientID))
	form.Set("device_code", strings.TrimSpace(deviceCode))
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	res, err := c.oauthPost(ctx, "/login/oauth/access_token", form)
	if err != nil {
		return "", false, false, err
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.Unmarshal(res, &payload); err != nil {
		return "", false, false, errors.New("授权响应格式异常")
	}
	switch payload.Error {
	case "":
		if payload.AccessToken == "" {
			return "", false, false, errors.New("授权响应里没有 token")
		}
		return payload.AccessToken, false, false, nil
	case "authorization_pending":
		return "", true, false, nil
	case "slow_down":
		return "", true, true, nil
	case "expired_token":
		return "", false, false, errors.New("设备码已过期，请重新开始授权")
	case "access_denied":
		return "", false, false, errors.New("授权被拒绝")
	default:
		msg := payload.Description
		if msg == "" {
			msg = payload.Error
		}
		return "", false, false, fmt.Errorf("授权失败：%s", msg)
	}
}

func (c *Client) oauthPost(ctx context.Context, path string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.webBase()+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &Error{Status: 0, Code: "E_OFFLINE", Message: "无法连接 GitHub 授权服务"}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &Error{Status: resp.StatusCode, Code: "E_AUTH", Message: "GitHub 授权服务返回异常"}
	}
	return body, nil
}

func (c *Client) webBase() string {
	if c.web != "" {
		return strings.TrimRight(c.web, "/")
	}
	return "https://github.com"
}

func (c *Client) webHost() string {
	if c.host != "" {
		return c.host
	}
	return "github.com"
}
