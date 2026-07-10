// Package windows_iso locates official Microsoft Windows retail ISO download
// links, reimplementing the request flow of https://github.com/pbatard/Fido
// (Fido.ps1) in Go.
package windows_iso

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Constants below are lifted verbatim from Fido.ps1, where they identify
// Fido/Rufus to Microsoft's download-connector API.
const (
	orgID      = "y6jn8c31"
	profileID  = "606624d44113"
	instanceID = "560dc9f3-1aa5-4a2f-b63c-9e18f8d0e175"
	referer    = "https://www.microsoft.com/software-download/windows11"

	// userAgent must look like a real browser — Microsoft's download-connector
	// endpoints reject requests from UAs that don't resemble one, and Fido's
	// README notes their servers also react to the UA appearing to be the same
	// Windows version as the one being requested. A trailing product token is
	// safe to append (browsers do this themselves), so it's used here to
	// identify this plugin without breaking that browser-shaped check.
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) " +
		"Chrome/124.0.0.0 Safari/537.36 packer-plugin-windows-utils/1.0"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Config selects a specific Windows (or UEFI Shell) ISO to locate. Version,
// Release, and Edition are always required; Language and Arch are required
// for retail Windows versions (unused for UEFI Shell). This is deliberately
// stricter than Fido's own commandline mode, which silently defaults unset
// fields to "whatever is latest/host-native" — fine for an interactive
// downloader, but not for a reproducible build pipeline where the exact SKU
// selected must be pinned in the config rather than left to drift with
// Microsoft's current release or the build host's architecture.
//
// Locale only affects which display language errors/pages come back in and
// defaults to "en-US". CacheTTL controls how long a resolved result is
// reused from the on-disk cache before being re-fetched live; zero disables
// caching.
type Config struct {
	Version  string
	Release  string
	Edition  string
	Language string
	Arch     string
	Locale   string
	CacheTTL time.Duration
}

// Result is the located download.
type Result struct {
	URL      string
	FileName string
}

type downloadLink struct {
	arch string
	url  string
}

type languageSku struct {
	sessionID string
	skuID     string
}

type languageEntry struct {
	code    string
	display string
	skus    []languageSku
}

// FetchDownloadURL resolves cfg to a single Windows/UEFI-Shell ISO download
// URL, performing the same session handshake and API calls as Fido.ps1. A
// result found in the on-disk cache within cfg.CacheTTL is returned without
// making any network calls; otherwise the live result is cached for next
// time (unless CacheTTL is zero).
func FetchDownloadURL(cfg Config) (*Result, error) {
	if cfg.CacheTTL > 0 {
		if cached, ok := loadCache(cfg); ok {
			return cached, nil
		}
	}

	result, err := resolveDownloadURL(cfg)
	if err != nil {
		return nil, err
	}

	if cfg.CacheTTL > 0 {
		saveCache(cfg, result)
	}
	return result, nil
}

func resolveDownloadURL(cfg Config) (*Result, error) {
	ver, err := findVersion(cfg.Version)
	if err != nil {
		return nil, err
	}
	rel, err := findRelease(ver, cfg.Release)
	if err != nil {
		return nil, err
	}
	ed, err := findEdition(rel, cfg.Edition)
	if err != nil {
		return nil, err
	}

	if strings.HasPrefix(ver.pageType, "UEFI_SHELL") {
		return fetchUEFIShellLink(ver, rel, ed)
	}

	if cfg.Language == "" {
		return nil, fmt.Errorf("language is required for reproducible builds (e.g. \"English International\")")
	}
	if cfg.Arch == "" {
		return nil, fmt.Errorf("arch is required for reproducible builds (e.g. \"x64\", \"x86\", \"ARM64\")")
	}

	locale := cfg.Locale
	if locale == "" {
		locale = "en-US"
	}
	locale = resolveLocale(locale)

	languages, err := collectLanguages(ed.ids, locale)
	if err != nil {
		return nil, err
	}
	if len(languages) == 0 {
		return nil, fmt.Errorf("no languages available for the selected edition")
	}

	lang, err := selectLanguage(languages, cfg.Language)
	if err != nil {
		return nil, err
	}

	var links []downloadLink
	for _, sku := range lang.skus {
		l, err := getDownloadLinks(sku.skuID, sku.sessionID, locale)
		if err != nil {
			return nil, err
		}
		links = append(links, l...)
	}
	if len(links) == 0 {
		return nil, fmt.Errorf("could not retrieve ISO download links")
	}

	link, err := selectArch(links, cfg.Arch)
	if err != nil {
		return nil, err
	}
	return &Result{URL: link.url, FileName: path.Base(strings.SplitN(link.url, "?", 2)[0])}, nil
}

func findVersion(name string) (*winVersion, error) {
	if name == "" {
		return nil, fmt.Errorf("version is required for reproducible builds (one of: %s)", strings.Join(versionNames(), ", "))
	}
	all := make([]winVersion, 0, len(windowsVersions)+len(uefiShellVersions))
	all = append(all, windowsVersions...)
	all = append(all, uefiShellVersions...)
	for i := range all {
		if strings.Contains(strings.ToLower(all[i].name), strings.ToLower(name)) {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("invalid Windows version %q (one of: %s)", name, strings.Join(versionNames(), ", "))
}

func versionNames() []string {
	var names []string
	for _, v := range windowsVersions {
		names = append(names, v.name)
	}
	for _, v := range uefiShellVersions {
		names = append(names, v.name)
	}
	return names
}

// findRelease requires an exact release to be named, or the literal
// "Latest" as an explicit (rather than implicit) opt-in to tracking
// whatever Microsoft currently serves.
func findRelease(ver *winVersion, name string) (*release, error) {
	if name == "" {
		return nil, fmt.Errorf("release is required for reproducible builds (one of: %s, or \"Latest\")", strings.Join(releaseNames(ver), ", "))
	}
	if strings.EqualFold(name, "latest") {
		return &ver.releases[0], nil
	}
	for i := range ver.releases {
		if strings.HasPrefix(strings.ToLower(ver.releases[i].name), strings.ToLower(name)) {
			return &ver.releases[i], nil
		}
	}
	return nil, fmt.Errorf("invalid release %q for %s (one of: %s, or \"Latest\")", name, ver.name, strings.Join(releaseNames(ver), ", "))
}

func releaseNames(ver *winVersion) []string {
	var names []string
	for _, r := range ver.releases {
		names = append(names, r.name)
	}
	return names
}

func findEdition(rel *release, name string) (*edition, error) {
	if name == "" {
		return nil, fmt.Errorf("edition is required for reproducible builds (one of: %s)", strings.Join(editionNames(rel), ", "))
	}
	for i := range rel.editions {
		if strings.Contains(strings.ToLower(rel.editions[i].name), strings.ToLower(name)) {
			return &rel.editions[i], nil
		}
	}
	return nil, fmt.Errorf("invalid edition %q (one of: %s)", name, strings.Join(editionNames(rel), ", "))
}

func editionNames(rel *release) []string {
	var names []string
	for _, ed := range rel.editions {
		names = append(names, ed.name)
	}
	return names
}

func selectLanguage(languages []languageEntry, name string) (*languageEntry, error) {
	for i := range languages {
		if strings.Contains(strings.ToLower(languages[i].code), strings.ToLower(name)) ||
			strings.Contains(strings.ToLower(languages[i].display), strings.ToLower(name)) {
			return &languages[i], nil
		}
	}
	return nil, fmt.Errorf("invalid language %q", name)
}

func selectArch(links []downloadLink, name string) (*downloadLink, error) {
	for i := range links {
		if strings.EqualFold(links[i].arch, name) {
			return &links[i], nil
		}
	}
	return nil, fmt.Errorf("invalid architecture %q", name)
}

// resolveLocale mirrors Fido's Check-Locale: if the locale-specific software
// download page doesn't exist (redirects or errors), fall back to en-US.
func resolveLocale(locale string) string {
	url := "https://www.microsoft.com/" + locale + "/software-download/windows11"
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "en-US"
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "en-US"
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return "en-US"
	}
	return locale
}

// collectLanguages performs the vlscppe/ov-df session handshake and queries
// getskuinformationbyproductedition for each id in editionIDs, merging the
// results by language the way Fido's Get-Windows-Languages does.
func collectLanguages(editionIDs []int, locale string) ([]languageEntry, error) {
	var languages []languageEntry
	find := func(code string) int {
		for i := range languages {
			if languages[i].code == code {
				return i
			}
		}
		return -1
	}

	for _, editionID := range editionIDs {
		sessionID := uuid.NewString()
		if err := checkVlscppeTag(sessionID); err != nil {
			return nil, err
		}
		if err := ovdfHandshake(sessionID); err != nil {
			return nil, err
		}
		skus, err := getSkuInformation(editionID, sessionID, locale)
		if err != nil {
			return nil, err
		}
		for _, sku := range skus {
			i := find(sku.language)
			if i == -1 {
				languages = append(languages, languageEntry{code: sku.language, display: sku.localizedLanguage})
				i = len(languages) - 1
			}
			languages[i].skus = append(languages[i].skus, languageSku{sessionID: sessionID, skuID: sku.id})
		}
	}
	return languages, nil
}

func checkVlscppeTag(sessionID string) error {
	url := fmt.Sprintf("https://vlscppe.microsoft.com/tags?org_id=%s&session_id=%s", orgID, sessionID)
	_, err := httpGetBody(url, nil)
	if err != nil {
		return fmt.Errorf("registering session: %w", err)
	}
	return nil
}

var (
	wRegexp      = regexp.MustCompile(`[?&]w=([A-F0-9]+)`)
	rticksRegexp = regexp.MustCompile(`rticks="\+?(\d+)`)
)

// ovdfHandshake performs the two-step ov-df.microsoft.com "protection" dance
// Fido reverse-engineered: fetch a small JS snippet to extract a 'w' token
// and 'rticks' timestamp, then echo them back with the current epoch time.
func ovdfHandshake(sessionID string) error {
	url := fmt.Sprintf("https://ov-df.microsoft.com/mdt.js?instanceId=%s&PageId=si&session_id=%s", instanceID, sessionID)
	body, err := httpGetBody(url, nil)
	if err != nil {
		return fmt.Errorf("requesting ov-df data: %w", err)
	}
	wMatch := wRegexp.FindSubmatch(body)
	rMatch := rticksRegexp.FindSubmatch(body)
	if wMatch == nil || rMatch == nil {
		return fmt.Errorf("could not extract ov-df data")
	}

	url = fmt.Sprintf("https://ov-df.microsoft.com/?session_id=%s&CustomerId=%s&PageId=si&w=%s&mdt=%d&rticks=%s",
		sessionID, instanceID, string(wMatch[1]), time.Now().UnixMilli(), string(rMatch[1]))
	if _, err := httpGetBody(url, nil); err != nil {
		return fmt.Errorf("completing ov-df handshake: %w", err)
	}
	return nil
}

type skuResult struct {
	language          string
	localizedLanguage string
	id                string
}

type apiError struct {
	Type  int    `json:"Type"`
	Value string `json:"Value"`
}

func getSkuInformation(editionID int, sessionID, locale string) ([]skuResult, error) {
	url := fmt.Sprintf("https://www.microsoft.com/software-download-connector/api/getskuinformationbyproductedition"+
		"?profile=%s&productEditionId=%d&SKU=undefined&friendlyFileName=undefined&Locale=%s&sessionID=%s",
		profileID, editionID, locale, sessionID)

	var resp struct {
		Skus []struct {
			Language          string `json:"Language"`
			LocalizedLanguage string `json:"LocalizedLanguage"`
			ID                string `json:"Id"`
		} `json:"Skus"`
		Errors []apiError `json:"Errors"`
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(2 * time.Second)
		}
		body, err := httpGetBody(url, nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp.Skus, resp.Errors = nil, nil
		if err := json.Unmarshal(body, &resp); err != nil {
			lastErr = fmt.Errorf("parsing SKU response: %w", err)
			continue
		}
		if len(resp.Errors) > 0 {
			lastErr = apiErrorToErr(resp.Errors[0], sessionID, locale)
			continue
		}
		if len(resp.Skus) == 0 {
			lastErr = fmt.Errorf("could not parse languages")
			continue
		}
		results := make([]skuResult, len(resp.Skus))
		for i, s := range resp.Skus {
			results[i] = skuResult{language: s.Language, localizedLanguage: s.LocalizedLanguage, id: s.ID}
		}
		return results, nil
	}
	return nil, lastErr
}

func getDownloadLinks(skuID, sessionID, locale string) ([]downloadLink, error) {
	url := fmt.Sprintf("https://www.microsoft.com/software-download-connector/api/GetProductDownloadLinksBySku"+
		"?profile=%s&productEditionId=undefined&SKU=%s&friendlyFileName=undefined&Locale=%s&sessionID=%s",
		profileID, skuID, locale, sessionID)

	body, err := httpGetBody(url, map[string]string{"Referer": referer})
	if err != nil {
		return nil, err
	}

	var resp struct {
		ProductDownloadOptions []struct {
			DownloadType int    `json:"DownloadType"`
			URI          string `json:"Uri"`
		} `json:"ProductDownloadOptions"`
		Errors []apiError `json:"Errors"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing download links response: %w", err)
	}
	if len(resp.Errors) > 0 {
		return nil, apiErrorToErr(resp.Errors[0], sessionID, locale)
	}
	if len(resp.ProductDownloadOptions) == 0 {
		return nil, fmt.Errorf("could not retrieve ISO download links")
	}

	links := make([]downloadLink, len(resp.ProductDownloadOptions))
	for i, o := range resp.ProductDownloadOptions {
		links[i] = downloadLink{arch: archFromType(o.DownloadType), url: o.URI}
	}
	return links, nil
}

func apiErrorToErr(e apiError, sessionID, locale string) error {
	if e.Type == 9 {
		return fmt.Errorf("%s%s", bannedMessage(locale), sessionID)
	}
	return fmt.Errorf("%s", e.Value)
}

func archFromType(t int) string {
	switch t {
	case 0:
		return "x86"
	case 1:
		return "x64"
	case 2:
		return "ARM64"
	default:
		return "Unknown"
	}
}

var (
	msgPattern        = regexp.MustCompile(`<input id="msg-01" type="hidden" value="(.*?)"/>`)
	htmlTagPattern    = regexp.MustCompile(`<[^>]+>`)
	whitespacePattern = regexp.MustCompile(`\s+`)
	fallbackBannedMsg = "Your IP address has been banned by Microsoft for issuing too many ISO download requests or for " +
		"belonging to a region of the world where sanctions currently apply. Please try again later. " +
		"If you believe this ban to be in error, you can try contacting Microsoft by referring to " +
		"message code 715-123130 and session ID "
)

// bannedMessage mirrors Fido's Get-Code-715-123130-Message: Microsoft's own
// software-download page carries the current wording for this ban message,
// so it's scraped from there with a hardcoded fallback if that fails.
func bannedMessage(locale string) string {
	url := "https://www.microsoft.com/" + locale + "/software-download/windows11"
	body, err := httpGetBody(url, nil)
	if err != nil {
		return fallbackBannedMsg
	}
	m := msgPattern.FindSubmatch(body)
	if m == nil {
		return fallbackBannedMsg
	}
	msg := strings.ReplaceAll(string(m[1]), "&lt;", "<")
	msg = htmlTagPattern.ReplaceAllString(msg, "")
	msg = whitespacePattern.ReplaceAllString(msg, " ")
	if !strings.Contains(msg, "715-123130") {
		return fallbackBannedMsg
	}
	return msg
}

func fetchUEFIShellLink(ver *winVersion, rel *release, ed *edition) (*Result, error) {
	tag := strings.SplitN(rel.name, " ", 2)[0]
	shellVersion := ""
	if parts := strings.SplitN(ver.pageType, " ", 2); len(parts) == 2 {
		shellVersion = parts[1]
	}
	link := fmt.Sprintf("https://github.com/pbatard/UEFI-Shell/releases/download/%[1]s/UEFI-Shell-%[2]s-%[1]s", tag, shellVersion)
	if len(ed.ids) > 0 && ed.ids[0] == 1 {
		link += "-DEBUG.iso"
	} else {
		link += "-RELEASE.iso"
	}
	return &Result{URL: link, FileName: path.Base(link)}, nil
}

func httpGetBody(url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return body, fmt.Errorf("request to %s failed: %s", url, resp.Status)
	}
	return body, nil
}
