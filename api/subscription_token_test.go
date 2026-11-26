package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/alireza0/s-ui/database"
	"github.com/alireza0/s-ui/database/model"
	"github.com/alireza0/s-ui/sub"
)

func TestResetSubscriptionTokenRequiresPanelLoginAndRevokesOldLink(t *testing.T) {
	engine := loginEngine(t)
	sub.NewSubHandler(engine.Group("/sub"))
	client := model.Client{
		Name: "alice", Enable: true, SubToken: "old-test-token",
		Config:   json.RawMessage(`{"test":{"name":"alice"}}`),
		Inbounds: json.RawMessage(`[]`), Links: json.RawMessage(`[]`),
	}
	if err := database.GetDB().Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatUint(uint64(client.Id), 10)
	request := func(method, path string, form url.Values, session *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if session != nil {
			req.AddCookie(session)
		}
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec
	}
	const readPath = "/api/clientSubscription"
	const resetPath = "/api/resetSubToken"
	form := url.Values{"id": {id}}
	if got := request(http.MethodGet, readPath+"?id="+id, nil, nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous read: HTTP %d", got.Code)
	}
	if got := request(http.MethodPost, resetPath, form, nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous reset: HTTP %d", got.Code)
	}
	oldLink := "/sub/alice?token=" + client.SubToken
	if got := request(http.MethodHead, oldLink, nil, nil); got.Code != http.StatusOK {
		t.Fatalf("initial token: HTTP %d", got.Code)
	}
	login := request(http.MethodPost, "/api/login", url.Values{"user": {"admin"}, "pass": {"admin"}}, nil)
	session := findSessionCookie(t, login.Result())
	decode := func(rec *httptest.ResponseRecorder, success bool) string {
		t.Helper()
		var result struct {
			Success bool
			Obj     struct{ SubToken string }
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatalf("invalid response: %v", err)
		}
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" || result.Success != success {
			t.Fatalf("wrong response status %d, headers %v, success %t", rec.Code, rec.Header(), result.Success)
		}
		return result.Obj.SubToken
	}
	if token := decode(request(http.MethodGet, readPath+"?id="+id, nil, session), true); token != client.SubToken {
		t.Fatal("authenticated panel did not get the current token")
	}
	for _, invalidID := range []string{"0", "bad", "999999"} {
		decode(request(http.MethodPost, resetPath, url.Values{"id": {invalidID}}, session), false)
	}
	newToken := decode(request(http.MethodPost, resetPath, form, session), true)
	if newToken == "" || newToken == client.SubToken {
		t.Fatal("reset did not return a different token")
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, query := range []string{"", "&format=json", "&format=clash", "&format=happ"} {
			got := request(method, oldLink+query, nil, nil)
			if got.Code != http.StatusNotFound || got.Header().Get("Subscription-Userinfo") != "" {
				t.Fatalf("old token still serves after reset with %s %q: HTTP %d", method, query, got.Code)
			}
			got = request(method, "/sub/alice?token="+newToken+query, nil, nil)
			if got.Code != http.StatusOK || got.Header().Get("Subscription-Userinfo") == "" {
				t.Fatalf("new token does not serve with %s %q: HTTP %d", method, query, got.Code)
			}
		}
	}
	var saved model.Client
	if err := database.GetDB().First(&saved, client.Id).Error; err != nil {
		t.Fatal(err)
	}
	if !saved.Enable || string(saved.Config) != string(client.Config) || string(saved.Links) != string(client.Links) {
		t.Fatal("reset changed the client's status, node configuration or links")
	}
	if got := request(http.MethodGet, "/api/clients?id="+id, nil, session); strings.Contains(got.Body.String(), newToken) {
		t.Fatal("the normal client detail response returned the subscription token")
	}
}
