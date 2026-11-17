package update

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

func ProviderFeedIndexURL(baseURL string, channel string, platform string, arch string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", errors.New("SDN update feed base URL must use HTTPS")
	}
	for name, value := range map[string]string{
		"channel":  channel,
		"platform": platform,
		"arch":     arch,
	} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, `/\`) {
			return "", fmt.Errorf("invalid update feed %s", name)
		}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/cli-bundle/" + channel + "/" + platform + "/" + arch + "/index.json"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

// SelectProviderCandidate applies the same selection and feed boundary to pull and catch-up.
func SelectProviderCandidate(raw []byte, base FeedBase, selection ProviderFeedSelection) (*ProviderFeedUpdate, error) {
	feed, err := ParseProviderFeed(raw)
	if err != nil {
		return nil, err
	}
	candidate, err := feed.Select(selection)
	if err != nil {
		return nil, err
	}
	if err := candidate.AssertUnderFeed(base); err != nil {
		return nil, err
	}
	return candidate, nil
}
