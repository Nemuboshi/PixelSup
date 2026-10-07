package ocr

import (
	"net/http"
	"time"
)

func getHTTPClient(timeoutSeconds int) *http.Client {
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultTimeoutSeconds
	}
	if cached, ok := httpClientCache.Load(timeoutSeconds); ok {
		return cached.(*http.Client)
	}
	client := &http.Client{
		Transport: sharedTransport,
		Timeout:   time.Duration(timeoutSeconds) * time.Second,
	}
	actual, _ := httpClientCache.LoadOrStore(timeoutSeconds, client)
	return actual.(*http.Client)
}
