package browser

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/samaidev/samweb/internal/cdp"
)

// cdpCookieFile is the on-disk JSON file for CDP browser cookies.
// Defaults to ~/.samweb/cdp-cookies.json (alongside the proxy cookie jar).
func cdpCookieFile() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".samweb", "cdp-cookies.json")
}

// saveCDPCookies writes CDP browser cookies to disk (atomic temp+rename).
func saveCDPCookies(cookies []cdp.CDPCookie) error {
	if len(cookies) == 0 {
		return nil
	}
	path := cdpCookieFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cookies, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadCDPCookies reads CDP browser cookies from disk.
func loadCDPCookies() ([]cdp.CDPCookie, error) {
	data, err := os.ReadFile(cdpCookieFile())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cookies []cdp.CDPCookie
	if err := json.Unmarshal(data, &cookies); err != nil {
		return nil, err
	}
	return cookies, nil
}
