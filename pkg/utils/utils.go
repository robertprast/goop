package utils

import "net/url"

func MustParseURL(rawURL string) *url.URL {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		panic("Invalid URL: " + rawURL)
	}
	return parsedURL
}
