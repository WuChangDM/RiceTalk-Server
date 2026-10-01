package og

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"

	gormrepo "ridgericetalk/internal/infra/gorm"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/repositories"
)

var (
	titleRegex    = regexp.MustCompile(`<meta[^>]+property=["']og:title["'][^>]+content=["']([^"']+)["']`)
	descRegex     = regexp.MustCompile(`<meta[^>]+property=["']og:description["'][^>]+content=["']([^"']+)["']`)
	imageRegex    = regexp.MustCompile(`<meta[^>]+property=["']og:image["'][^>]+content=["']([^"']+)["']`)
	altTitleRegex = regexp.MustCompile(`<title>([^<]+)</title>`)
	altDescRegex  = regexp.MustCompile(`<meta[^>]+name=["']description["'][^>]+content=["']([^"']+)["']`)
)

// Service provides Open Graph metadata fetching and caching
type Service struct {
	db          *gorm.DB
	ogCacheRepo repositories.OGCacheRepository
}

// NewService creates a new OG service with a default GORM-backed
// OGCacheRepository (no cache). Use NewServiceWithRepos to inject a mock
// repository.
func NewService(db *gorm.DB) *Service {
	return &Service{
		db:          db,
		ogCacheRepo: gormrepo.NewGormOGCacheRepository(db),
	}
}

// NewServiceWithRepos creates a new OG service with the given OGCacheRepository.
func NewServiceWithRepos(db *gorm.DB, ogCacheRepo repositories.OGCacheRepository) *Service {
	if ogCacheRepo == nil {
		ogCacheRepo = gormrepo.NewGormOGCacheRepository(db)
	}
	return &Service{db: db, ogCacheRepo: ogCacheRepo}
}

// Preview represents a link preview
type Preview struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Image       string `json:"image"`
}

// FetchPreview fetches or returns cached OG metadata for a URL
func (s *Service) FetchPreview(rawURL string) (*Preview, error) {
	// Validate URL
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("invalid URL")
	}

	// SSRF Protection: block internal/private IPs and localhost
	if isSSRFURL(parsed.Host) {
		return nil, errors.New("SSRF: internal addresses are not allowed")
	}

	// Check cache first (repository handles expiry filter)
	if cached, err := s.ogCacheRepo.GetByURL(context.Background(), rawURL); err == nil {
		return &Preview{
			URL:         cached.URL,
			Title:       cached.Title,
			Description: cached.Description,
			Image:       cached.Image,
		}, nil
	}

	// Fetch from remote
	preview, err := s.fetchFromRemote(rawURL)
	if err != nil {
		return nil, err
	}

	// Save to cache (24 hour expiry). Replace any stale entry for this URL.
	_ = s.ogCacheRepo.DeleteByURL(context.Background(), rawURL)
	_ = s.ogCacheRepo.Create(context.Background(), &model.OGCache{
		ID:          generateID(),
		URL:         rawURL,
		Title:       preview.Title,
		Description: preview.Description,
		Image:       preview.Image,
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	})

	return preview, nil
}

func (s *Service) fetchFromRemote(rawURL string) (*Preview, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "RidgeRiceTalk-LinkPreview/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Limit read to 1MB to prevent memory issues
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, err
	}

	html := string(body)
	preview := &Preview{URL: rawURL}

	// Extract OG tags
	if m := titleRegex.FindStringSubmatch(html); len(m) > 1 {
		preview.Title = unescapeHTML(m[1])
	} else if m := altTitleRegex.FindStringSubmatch(html); len(m) > 1 {
		preview.Title = unescapeHTML(m[1])
	}

	if m := descRegex.FindStringSubmatch(html); len(m) > 1 {
		preview.Description = unescapeHTML(m[1])
	} else if m := altDescRegex.FindStringSubmatch(html); len(m) > 1 {
		preview.Description = unescapeHTML(m[1])
	}

	if m := imageRegex.FindStringSubmatch(html); len(m) > 1 {
		preview.Image = m[1]
	}

	return preview, nil
}

func generateID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func unescapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#39;", "'")
	return s
}

// isSSRFURL checks if a host points to an internal/private address.
// Blocks: localhost variants, 127.x.x.x, 10.x.x.x, 172.16-31.x.x, 192.168.x.x,
// 169.254.x.x (link-local), 0.0.0.0, and any plain IPv6 loopback.
func isSSRFURL(host string) bool {
	// Strip port if present
	if i := strings.LastIndex(host, ":"); i != -1 {
		host = host[:i]
	}
	// IPv6 bracket
	host = strings.Trim(host, "[]")

	host = strings.ToLower(host)

	// Block localhost variants
	if host == "localhost" || host == "localhost.localdomain" ||
		host == "0.0.0.0" || host == "::1" || host == "::" ||
		strings.HasPrefix(host, "127.") ||
		strings.HasPrefix(host, "10.") ||
		strings.HasPrefix(host, "192.168.") ||
		strings.HasPrefix(host, "169.254.") ||
		strings.HasPrefix(host, "fc") ||
		strings.HasPrefix(host, "fd") ||
		strings.HasPrefix(host, "fe80:") ||
		strings.HasPrefix(host, "ff") {
		return true
	}
	// 172.16.0.0/12 range
	if strings.HasPrefix(host, "172.") {
		parts := strings.Split(host, ".")
		if len(parts) >= 2 {
			seg := parts[1]
			if seg >= "16" && seg <= "31" {
				return true
			}
		}
	}
	return false
}
