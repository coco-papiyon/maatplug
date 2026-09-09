package pricing

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// maxPageSize bounds how much of a response body a scrape will read. These
// are marketing/docs pages, not downloads — anything past this is not a
// pricing page maatplug knows how to parse.
const maxPageSize = 16 << 20 // 16MB

var (
	tagRe   = regexp.MustCompile(`<[^>]+>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// fetchRaw GETs url and returns the response body unmodified.
func fetchRaw(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: unexpected status %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageSize))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// fetchNormalizedText GETs url and returns its body with HTML tags stripped,
// entities unescaped, and whitespace collapsed to single spaces — the shape
// every extractor's regexes are written against.
func fetchNormalizedText(ctx context.Context, url string) (string, error) {
	raw, err := fetchRaw(ctx, url)
	if err != nil {
		return "", err
	}
	return normalizeHTML(raw), nil
}

func normalizeHTML(rawHTML string) string {
	text := tagRe.ReplaceAllString(rawHTML, " ")
	text = html.UnescapeString(text)
	return strings.TrimSpace(spaceRe.ReplaceAllString(text, " "))
}

// uniqueOrderedMatches returns re's first capture group from every match in
// s, deduplicated, keeping each value's first-seen order.
func uniqueOrderedMatches(re *regexp.Regexp, s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		v := m[1]
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// lastMatch returns the last (rightmost) match of re in s, or "" if none.
func lastMatch(re *regexp.Regexp, s string) string {
	matches := re.FindAllString(s, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}
