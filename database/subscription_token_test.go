package database

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/alireza0/s-ui/database/model"
)

func TestSubscriptionTokenMigrationAndBackup(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy.db")
	open := func(path string) {
		t.Helper()
		if err := InitDB(path); err != nil {
			t.Fatal(err)
		}
		pool, err := GetDB().DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pool.Close() })
	}
	closeDB := func() {
		t.Helper()
		pool, err := GetDB().DB()
		if err != nil {
			t.Fatal(err)
		}
		if err := pool.Close(); err != nil {
			t.Fatal(err)
		}
	}
	read := func() []model.Client {
		t.Helper()
		var clients []model.Client
		if err := GetDB().Order("id").Find(&clients).Error; err != nil {
			t.Fatal(err)
		}
		return clients
	}
	open(legacyPath)
	seedEveryTable(t)
	// Recreate the v1.6.8 shape with no subscription token column.
	if err := GetDB().Exec("ALTER TABLE clients DROP COLUMN sub_token").Error; err != nil {
		t.Fatal(err)
	}
	first := read()[0]
	if err := GetDB().Model(&first).UpdateColumns(map[string]any{
		"inbounds": json.RawMessage(`[1]`), "up": 76, "down": 59,
		"total_up": 45, "volume": int64(1<<44 + 1), "expiry": 1860000000,
	}).Error; err != nil {
		t.Fatal(err)
	}
	second := model.Client{Name: "offline-client", Enable: false, Remark: "keep me"}
	if err := GetDB().Omit("SubToken").Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	before := read()
	closeDB()

	open(legacyPath)
	upgraded := read()
	if len(upgraded) != len(before) {
		t.Fatalf("upgrade returned %d clients, want %d", len(upgraded), len(before))
	}
	seen := map[string]bool{}
	for i, client := range upgraded {
		token := client.SubToken
		decoded, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || len(decoded) != 32 || seen[token] {
			t.Fatalf("client %d did not get a distinct 32-byte token", client.Id)
		}
		seen[token] = true
		client.SubToken = ""
		if !reflect.DeepEqual(client, before[i]) {
			t.Errorf("client %d's existing data changed during migration", client.Id)
		}
	}
	for _, table := range schema() {
		var rows int64
		if err := GetDB().Table(table.name).Count(&rows).Error; err != nil || rows == 0 {
			t.Errorf("legacy %s rows missing after migration: %d, %v", table.name, rows, err)
		}
	}
	closeDB()

	open(legacyPath)
	if !reflect.DeepEqual(read(), upgraded) {
		t.Fatal("startup changed existing tokens or client data")
	}
	backup, err := GetDb("")
	if err != nil {
		t.Fatal(err)
	}
	closeDB()
	restoredPath := filepath.Join(dir, "restored.db")
	if err := os.WriteFile(restoredPath, backup, 0o600); err != nil {
		t.Fatal(err)
	}
	open(restoredPath)
	if !reflect.DeepEqual(read(), upgraded) {
		t.Fatal("restoring the backup did not preserve the tokens and client data")
	}
}
