package server

import (
	"net/http"
	"net/http/cookiejar"
)

// newJar builds a cookie jar for the test client so session and CSRF cookies
// behave as they do in a browser.
func newJar() http.CookieJar {
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	return jar
}
