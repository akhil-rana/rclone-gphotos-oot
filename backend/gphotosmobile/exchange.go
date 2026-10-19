package gphotosmobile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/rclone/rclone/fs/fshttp"
)

const (
	embeddedSetupAuthEndpoint = "https://android.clients.google.com/auth"
	googlePlayServicesSig     = "38918a453d07199354f8b19af05ec6562ced5788"
	googlePhotosPackage       = "com.google.android.apps.photos"
	googlePhotosSig           = "24bb24c05e47e0aefa68a58a766179d9b613a600"
	googlePhotosService       = "oauth2:openid https://www.googleapis.com/auth/mobileapps.native https://www.googleapis.com/auth/photos.native"
)

func exchangeOAuthToken(ctx context.Context, oauthToken string) (string, error) {
	oauthToken = strings.TrimSpace(oauthToken)
	if strings.HasPrefix(oauthToken, "oauth_token=") {
		oauthToken = strings.TrimPrefix(oauthToken, "oauth_token=")
	}

	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	androidID := hex.EncodeToString(raw)

	form := url.Values{
		"accountType":                  {"HOSTED_OR_GOOGLE"},
		"Email":                        {"oauth-token@example.com"},
		"has_permission":               {"1"},
		"add_account":                  {"1"},
		"ACCESS_TOKEN":                 {"1"},
		"Token":                        {oauthToken},
		"service":                      {"ac2dm"},
		"source":                       {"android"},
		"androidId":                    {androidID},
		"device_country":               {"us"},
		"operatorCountry":              {"us"},
		"lang":                         {"en"},
		"sdk_version":                  {"17"},
		"google_play_services_version": {"240913000"},
		"client_sig":                   {googlePlayServicesSig},
		"callerSig":                    {googlePlayServicesSig},
		"droidguard_results":           {"dummy123"},
	}

	client := fshttp.NewClient(ctx)
	// We need to prevent following redirects because the embedded setup auth endpoint shouldn't redirect for this flow
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		embeddedSetupAuthEndpoint,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", "GoogleAuth/1.4")

	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("Google authentication returned HTTP %d", response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if err != nil {
		return "", err
	}

	values := make(map[string]string)
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			values[parts[0]] = parts[1]
		}
	}

	if authError := values["Error"]; authError != "" {
		return "", fmt.Errorf("Google auth error: %s", authError)
	}

	masterToken := values["Token"]
	email := values["Email"]
	if masterToken == "" || email == "" {
		return "", errors.New("invalid response from Google authentication")
	}

	credValues := url.Values{
		"androidId":                    {androidID},
		"app":                          {googlePhotosPackage},
		"callerPkg":                    {googlePhotosPackage},
		"callerSig":                    {googlePhotosSig},
		"client_sig":                   {googlePhotosSig},
		"device_country":               {"us"},
		"Email":                        {email},
		"google_play_services_version": {"240913000"},
		"lang":                         {"en_US"},
		"oauth2_foreground":            {"1"},
		"operatorCountry":              {"us"},
		"sdk_version":                  {"33"},
		"service":                      {googlePhotosService},
		"source":                       {"android"},
		"Token":                        {masterToken},
	}
	return credValues.Encode(), nil
}
