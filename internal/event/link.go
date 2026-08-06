package event

import (
	"fmt"
	"net/url"
	"strings"
)

// linkSchemes is the hardcoded allowlist of URL schemes a link Href may use. It
// is a deliberate safety floor, NOT operator-configurable: an editable scheme
// list is a footgun that reopens javascript:/file:/data: as review links. The
// optional host allowlist (config.Config.ReviewLinkHosts) is the tunable policy
// layer; the scheme floor is not.
var linkSchemes = map[string]bool{"http": true, "https": true}

// ValidateLink checks a link is safe to append to the log. Href must parse as an
// absolute URL whose (case-insensitive) scheme is in the hardcoded allowlist
// {http, https}. When allowedHosts is non-empty, Href's host must also match one
// of them (case-insensitively); an empty allowedHosts admits any host, so the
// host allowlist is opt-in. draiver never fetches the URL — this is validation
// of an opaque hyperlink, not an integration, so no host parsing beyond the
// allowlist membership test happens here.
func ValidateLink(l Link, allowedHosts []string) error {
	if strings.TrimSpace(l.Href) == "" {
		return fmt.Errorf("href is empty")
	}
	u, err := url.Parse(l.Href)
	if err != nil {
		return fmt.Errorf("href %q is not a valid URL: %w", l.Href, err)
	}
	if !u.IsAbs() {
		return fmt.Errorf("href %q is not an absolute URL (a scheme like https:// is required)", l.Href)
	}
	if !linkSchemes[strings.ToLower(u.Scheme)] {
		return fmt.Errorf("href %q has scheme %q; only http and https are allowed", l.Href, u.Scheme)
	}
	if len(allowedHosts) > 0 {
		host := u.Hostname()
		for _, h := range allowedHosts {
			if strings.EqualFold(host, strings.TrimSpace(h)) {
				return nil
			}
		}
		return fmt.Errorf("href host %q is not in the configured review-link host allowlist", host)
	}
	return nil
}
