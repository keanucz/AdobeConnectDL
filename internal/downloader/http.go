package downloader

import (
	"bytes"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// requestOptions configures HTTP request headers and cookies.
type requestOptions struct {
	Cookies    []*http.Cookie
	Referer    string
	AcceptType string // "video" or "" for default
}

const defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
const videoAccept = "video/webm,video/ogg,video/*;q=0.9,application/ogg;q=0.7,audio/*;q=0.6,*/*;q=0.5"

// applyRequestOptions sets common headers and cookies on an HTTP request.
func applyRequestOptions(req *http.Request, opts requestOptions) {
	for _, c := range opts.Cookies {
		req.AddCookie(c)
	}
	if opts.Referer != "" {
		req.Header.Set("Referer", opts.Referer)
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	if opts.AcceptType == "video" {
		req.Header.Set("Accept", videoAccept)
	}
}

// mergeCookies combines session cookie with extra cookies, avoiding duplicates.
func mergeCookies(session string, extra []*http.Cookie) []*http.Cookie {
	seen := map[string]bool{}
	cookies := make([]*http.Cookie, 0, len(extra)+1)

	if session != "" {
		cookies = append(cookies, &http.Cookie{Name: "BREEZESESSION", Value: session})
		seen["BREEZESESSION"] = true
	}

	for _, c := range extra {
		if c == nil || c.Name == "" {
			continue
		}
		key := c.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		cookies = append(cookies, c)
	}

	return cookies
}

// isHTMLResponse checks if a response appears to be HTML.
func isHTMLResponse(headers http.Header, chunk []byte) bool {
	ct := headers.Get("Content-Type")
	if strings.Contains(strings.ToLower(ct), "text/html") {
		return true
	}
	trimmed := bytes.TrimSpace(chunk)
	return len(trimmed) > 0 && trimmed[0] == '<'
}

// isZipSignature checks if bytes start with ZIP file signature.
func isZipSignature(b []byte) bool {
	return len(b) >= 4 && b[0] == 'P' && b[1] == 'K' && b[2] == 0x03 && b[3] == 0x04
}

// findCASRecordingURL extracts the casRecordingURL from JavaScript in the HTML.
// This URL points to the signed streaming endpoint that redirects to the actual MP4.
func findCASRecordingURL(body []byte) string {
	// Look for: var casRecordingURL = 'https://...';
	re := regexp.MustCompile(`var\s+casRecordingURL\s*=\s*'([^']+)'`)
	match := re.FindSubmatch(body)
	if len(match) >= 2 {
		return string(match[1])
	}
	return ""
}

// findVTTFromJS extracts the VTT filename from JavaScript.
func findVTTFromJS(body []byte) string {
	// Look for transcriptFilename variable
	re := regexp.MustCompile(`var\s+transcriptFilename\s*=\s*'([^']+\.vtt)'`)
	match := re.FindSubmatch(body)
	if len(match) >= 2 {
		// Unescape JavaScript \x20 etc
		return unescapeJS(string(match[1]))
	}
	// Fallback: look for .vtt references in paths
	vttRe := regexp.MustCompile(`['"](/[^'"]+\.vtt)['"]`)
	match = vttRe.FindSubmatch(body)
	if len(match) >= 2 {
		return string(match[1])
	}
	return ""
}

// unescapeJS handles simple JavaScript escape sequences like \x20.
func unescapeJS(s string) string {
	re := regexp.MustCompile(`\\x([0-9a-fA-F]{2})`)
	return re.ReplaceAllStringFunc(s, func(m string) string {
		hex := m[2:]
		var b byte
		_, _ = fmt.Sscanf(hex, "%x", &b)
		return string(b)
	})
}

// parseVideoElement extracts <video src="..."> and <track src="..."> from HTML.
func parseVideoElement(body []byte) (videoSrc, vttPath string) {
	z := html.NewTokenizer(bytes.NewReader(body))
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return videoSrc, vttPath
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			switch t.Data {
			case "video":
				for _, a := range t.Attr {
					if a.Key == "src" && a.Val != "" {
						videoSrc = a.Val
					}
				}
			case "track":
				for _, a := range t.Attr {
					if a.Key == "src" && a.Val != "" {
						vttPath = a.Val
					}
				}
			}
		default:
			// Ignore other token types (text, end tag, comment, doctype)
		}
	}
}
