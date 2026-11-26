package util

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alireza0/s-ui/logger"
	"github.com/op/go-logging"
)

func TestExternalSubscriptionKeepsCredentialsOutOfLogsAndRedirects(t *testing.T) {
	logger.InitLogger(logging.ERROR)
	const secret = "fixture-credential"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/result", http.StatusFound)
		case "/result":
			if r.Referer() != "" {
				t.Error("external subscription redirect got a Referer")
			}
			_, _ = w.Write([]byte("socks://example.test"))
		default:
			http.Error(w, "not a subscription", http.StatusNotFound)
		}
	}))
	defer server.Close()
	if got := GetExternalLink(server.URL + "/redirect?token=" + secret); got != "socks://example.test" {
		t.Fatalf("unexpected redirect response %q", got)
	}
	if got := GetExternalLink(server.URL + "/old?token=" + secret); got != "" {
		t.Fatal("external subscription returned the error page as contents")
	}
	GetExternalLink("http://[invalid/sub?token=" + secret)
	if strings.Contains(strings.Join(logger.GetLogs(10, "WARNING"), "\n"), secret) {
		t.Fatal("external subscription log included a credential")
	}
}
