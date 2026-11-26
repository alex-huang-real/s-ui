package sub

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/alireza0/s-ui/database"
	"github.com/alireza0/s-ui/database/model"
	"github.com/alireza0/s-ui/service"

	"github.com/gin-gonic/gin"
)

// The tests in this file pin the subscription side of the anti-probing
// posture discussed in #1316: this HTTP surface stays silent to
// unauthenticated probing. They lock in current behaviour only -- if one
// of them starts failing, a change has altered what an unauthenticated
// scanner can learn from this box, and that change should be deliberate,
// not accidental. The panel API side of the same posture lives in
// api/anti_probing_test.go.

func antiProbingDB(t *testing.T) {
	t.Helper()
	if err := database.InitDB(filepath.Join(t.TempDir(), "test.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if db := database.GetDB(); db != nil {
			if sqlDB, err := db.DB(); err == nil {
				sqlDB.Close()
			}
		}
	})
}

// antiProbingSubEngine builds the subscription server's real router, the
// same engine sub.Server.Start serves.
func antiProbingSubEngine(t *testing.T) (*gin.Engine, string) {
	t.Helper()
	engine, err := NewServer().initRouter()
	if err != nil {
		t.Fatal(err)
	}
	settings := &service.SettingService{}
	subPath, err := settings.GetSubPath()
	if err != nil {
		t.Fatal(err)
	}
	return engine, subPath
}

func antiProbingSeedClients(t *testing.T) {
	t.Helper()
	db := database.GetDB()
	clients := []model.Client{
		{Name: "regression-real", Enable: true, SubToken: "real-test-token"},
		{Name: "regression-off", Enable: false, SubToken: "disabled-test-token"},
		{Name: "regression-other", Enable: true, SubToken: "other-test-token"},
		{Name: "用户 +/%", Enable: true, SubToken: "encoded-name-test-token"},
	}
	for i := range clients {
		clients[i].Config = json.RawMessage(`{}`)
		clients[i].Inbounds = json.RawMessage(`[]`)
		clients[i].Links = json.RawMessage(`[]`)
		if err := db.Create(&clients[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func antiProbingGet(t *testing.T, url string) (int, http.Header, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, body
}

// The subscription server must not identify the software in a Server
// header: that string is a fingerprint scanners index on, and nothing in
// the stack needs to send it. The panel API is checked the same way in
// the api package.
func TestAntiProbingNoServerHeader(t *testing.T) {
	antiProbingDB(t)
	antiProbingSeedClients(t)

	subEngine, subPath := antiProbingSubEngine(t)
	subSrv := httptest.NewServer(subEngine)
	defer subSrv.Close()

	urls := []string{
		subSrv.URL + subPath + "regression-real?token=real-test-token",
		subSrv.URL + subPath + "no-such-client",
	}
	for _, url := range urls {
		_, header, _ := antiProbingGet(t, url)
		if values, ok := header["Server"]; ok {
			t.Errorf("GET %s: Server header present (%v), want none", url, values)
		}
	}
}

func TestSubscriptionRequiresTokenBeforeServingAnyFormat(t *testing.T) {
	antiProbingDB(t)
	antiProbingSeedClients(t)
	engine, subPath := antiProbingSubEngine(t)

	bad := []string{
		"no-such-client",
		"no-such-client?token=real-test-token",
		"regression-real",
		"regression-real?token=",
		"regression-real?token=wrong&format=json",
		"regression-real?token=other-test-token&format=clash",
		"regression-real?format=happ",
		"regression-real?token=real-test-token&token=real-test-token",
		"regression-off?token=disabled-test-token",
	}
	for _, path := range bad {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			req := httptest.NewRequest(method, subPath+path, nil)
			req.Header.Set("User-Agent", "SFA/1.11.0")
			resp := httptest.NewRecorder()
			engine.ServeHTTP(resp, req)
			if resp.Code != http.StatusNotFound || resp.Body.Len() != 0 ||
				resp.Header().Get("Subscription-Userinfo") != "" || resp.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("%s %q: got status %d, headers %v, body %q", method, path, resp.Code, resp.Header(), resp.Body.String())
			}
		}
	}

	for _, path := range []string{
		"regression-real?token=real-test-token",
		"%E7%94%A8%E6%88%B7%20%2B%2F%25?token=encoded-name-test-token",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			resp := httptest.NewRecorder()
			engine.ServeHTTP(resp, httptest.NewRequest(method, subPath+path, nil))
			if resp.Code != http.StatusOK || resp.Header().Get("Subscription-Userinfo") == "" ||
				resp.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("authorized %s %q: status %d, headers %v", method, path, resp.Code, resp.Header())
			}
		}
	}
}

func TestSubscriptionWithCustomPathAndEncodedName(t *testing.T) {
	antiProbingDB(t)
	antiProbingSeedClients(t)
	if err := (&service.SettingService{}).SetSubPath("/入口 列表/"); err != nil {
		t.Fatal(err)
	}
	engine, _ := antiProbingSubEngine(t)
	path := "/%E5%85%A5%E5%8F%A3%20%E5%88%97%E8%A1%A8/%E7%94%A8%E6%88%B7%20%2B%2F%25"
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		resp := httptest.NewRecorder()
		engine.ServeHTTP(resp, httptest.NewRequest(method, path+"?token=encoded-name-test-token", nil))
		if resp.Code != http.StatusOK || resp.Header().Get("Subscription-Userinfo") == "" {
			t.Errorf("%s subscription at custom path: HTTP %d", method, resp.Code)
		}
	}
}
