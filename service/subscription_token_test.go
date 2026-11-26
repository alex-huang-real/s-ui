package service

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/alireza0/s-ui/database"
	"github.com/alireza0/s-ui/database/model"
)

func TestClientSubscriptionTokenSurvivesStaleEdits(t *testing.T) {
	s := lifecycleService(t)
	if err := s.SettingService.SetMaintenance(true); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	payload := func(id uint, name, token string) string {
		return fmt.Sprintf(`{"id":%d,"name":%q,"enable":true,"config":{},"links":[],"inbounds":[],"volume":9007199254740993,"SuBToKeN":%q}`, id, name, token)
	}
	save := func(action, data string) {
		t.Helper()
		if _, err := s.Save("clients", action, json.RawMessage(data), "", "admin", "localhost"); err != nil {
			t.Fatal(err)
		}
	}
	save("new", payload(0, "alice", "supplied-by-caller"))
	var alice model.Client
	if err := db.Where("name = ?", "alice").First(&alice).Error; err != nil {
		t.Fatal(err)
	}
	save("addbulk", "["+payload(0, "alice-copy", alice.SubToken)+","+payload(0, "bob", "supplied-by-caller")+"]")

	var tokens []string
	if err := db.Model(&model.Client{}).Order("id").Pluck("sub_token", &tokens).Error; err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, token := range tokens {
		decoded, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || len(decoded) != 32 || seen[token] {
			t.Fatal("new, copied and bulk-added clients must each get an independent generated token")
		}
		seen[token] = true
	}
	if len(tokens) != 3 {
		t.Fatalf("got %d clients, want 3", len(tokens))
	}

	rotated, err := s.ClientService.ResetSubscriptionToken(alice.Id, "admin")
	if err != nil || rotated == "" || seen[rotated] {
		t.Fatalf("reset did not issue a new token: %v", err)
	}
	staleEdit := payload(alice.Id, "alice-renamed", alice.SubToken)
	save("edit", staleEdit)
	save("editbulk", "["+staleEdit+"]")
	got := reload(t, db, alice.Id)
	if got.Name != "alice-renamed" || got.SubToken != rotated || got.Volume != 9007199254740993 {
		t.Fatal("stale edits did not save the expected fields while retaining the new token")
	}

	var changes []model.Changes
	if err := db.Where("key = ?", "clients").Order("id").Find(&changes).Error; err != nil {
		t.Fatal(err)
	}
	if len(changes) != 5 {
		t.Fatalf("got %d client audit entries, want 5", len(changes))
	}
	for _, change := range changes {
		obj := string(change.Obj)
		if strings.Contains(strings.ToLower(obj), "subtoken") || strings.Contains(obj, alice.SubToken) || strings.Contains(obj, rotated) {
			t.Fatal("subscription token appeared in an audit payload")
		}
		if change.Action == "resetSubToken" {
			if change.Actor != "admin" || obj != fmt.Sprintf(`{"id":%d}`, alice.Id) {
				t.Errorf("unexpected reset audit data: %s, %s", change.Actor, obj)
			}
		} else if !strings.Contains(obj, "9007199254740993") {
			t.Fatal("sanitizing the audit payload lost integer precision")
		}
	}
}
