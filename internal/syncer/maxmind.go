package syncer

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MaxMindClient interacts with the MaxMind download API.
type MaxMindClient struct {
	httpClient *http.Client
	baseURL    string
}

func NewMaxMindClient(baseURL string, timeout time.Duration) *MaxMindClient {
	if baseURL == "" {
		baseURL = "https://download.maxmind.com"
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute // Large downloads may take several minutes on slow links
	}

	return &MaxMindClient{
		httpClient: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("stopped after 10 redirects")
				}
				return nil
			},
		},
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

// DownloadDatabase streams the database archive for a given edition.
func (c *MaxMindClient) DownloadDatabase(ctx context.Context, accountID, licenseKey, editionID string, ifModifiedSince *time.Time) (body io.ReadCloser, notModified bool, lastModified time.Time, err error) {
	endpoint := fmt.Sprintf("%s/geoip/databases/%s/download?suffix=tar.gz", c.baseURL, url.PathEscape(editionID))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, time.Time{}, fmt.Errorf("failed to construct request: %w", err)
	}

	// Basic Auth credentials
	req.SetBasicAuth(accountID, licenseKey)

	// Conditional If-Modified-Since header
	if ifModifiedSince != nil && !ifModifiedSince.IsZero() {
		req.Header.Set("If-Modified-Since", ifModifiedSince.UTC().Format(http.TimeFormat))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, false, time.Time{}, fmt.Errorf("maxmind request failed: %w", err)
	}

	if resp.StatusCode == http.StatusNotModified {
		_ = resp.Body.Close()
		return nil, true, time.Time{}, nil
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		_ = resp.Body.Close()
		return nil, false, time.Time{}, fmt.Errorf("maxmind upstream error HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	lmHeader := resp.Header.Get("Last-Modified")
	if lmHeader != "" {
		if parsed, err := http.ParseTime(lmHeader); err == nil {
			lastModified = parsed.UTC()
		}
	}
	if lastModified.IsZero() {
		lastModified = time.Now().UTC()
	}

	return resp.Body, false, lastModified, nil
}

// CheckCredentials performs a lightweight request to verify Account ID and License Key.
func (c *MaxMindClient) CheckCredentials(ctx context.Context, accountID, licenseKey string) error {
	endpoint := fmt.Sprintf("%s/geoip/databases/GeoLite2-Country/download?suffix=tar.gz", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(accountID, licenseKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("invalid account ID or license key (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusNotModified {
		return fmt.Errorf("unexpected response from MaxMind (HTTP %d)", resp.StatusCode)
	}

	return nil
}
