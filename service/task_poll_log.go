package service

import (
	"net/url"
	"regexp"
)

var taskPollMediaURL = regexp.MustCompile(`(?i)https?://[^\s"<>\\]+`)

// Poll diagnostics retain only a media origin. Paths may themselves carry
// credentials; query strings and userinfo must never enter backend logs.
func redactTaskPollURLs(value string) string {
	return taskPollMediaURL.ReplaceAllStringFunc(value, func(raw string) string {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Hostname() == "" {
			return "[redacted-url]"
		}
		return parsed.Scheme + "://" + parsed.Host + "/[redacted]"
	})
}
