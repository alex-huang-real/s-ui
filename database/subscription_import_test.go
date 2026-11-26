package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alireza0/s-ui/config"
	"github.com/alireza0/s-ui/database/model"
	"github.com/alireza0/s-ui/logger"
	"github.com/op/go-logging"
)

func TestImportSubscriptionDatabase(t *testing.T) {
	logger.InitLogger(logging.ERROR)
	for _, scenario := range []string{"legacy backup", "failed backfill", "busy live WAL"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("SUI_DB_FOLDER", t.TempDir())
			path := config.GetDBPath()
			uploadPath := filepath.Join(t.TempDir(), "upload.db")
			if err := InitDB(uploadPath); err != nil {
				t.Fatal(err)
			}
			pool, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pool.Close() })
			if err := db.Create(&model.Setting{Key: "version", Value: config.GetVersion()}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&model.Client{Name: "imported", Volume: 42, Up: 7}).Error; err != nil {
				t.Fatal(err)
			}
			if scenario == "failed backfill" {
				err = db.Exec(`CREATE TRIGGER reject_token BEFORE UPDATE OF sub_token ON clients
					BEGIN SELECT RAISE(ABORT, 'test token backfill failure'); END`).Error
			} else {
				err = db.Exec("ALTER TABLE clients DROP COLUMN sub_token").Error
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := pool.Close(); err != nil {
				t.Fatal(err)
			}
			if err := InitDB(path); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if active, err := db.DB(); err == nil {
					_ = active.Close()
				}
			})
			live := model.Client{Name: "live", SubToken: "current-live-token", Volume: 56}
			if err := db.Create(&live).Error; err != nil {
				t.Fatal(err)
			}
			if scenario == "busy live WAL" {
				other, err := sql.Open("sqlite3", path)
				if err != nil {
					t.Fatal(err)
				}
				defer other.Close()
				reader, err := other.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Rollback()
				var volume int64
				if err := reader.QueryRow("SELECT volume FROM clients WHERE id = ?", live.Id).Scan(&volume); err != nil {
					t.Fatal(err)
				}
				pool, err := db.DB()
				if err != nil {
					t.Fatal(err)
				}
				pool.SetMaxOpenConns(1)
				if err := db.Exec("PRAGMA busy_timeout = 20").Error; err != nil {
					t.Fatal(err)
				}
				live.Volume++
				if err := db.Model(&live).UpdateColumn("volume", live.Volume).Error; err != nil {
					t.Fatal(err)
				}
			}
			upload, err := os.Open(uploadPath)
			if err != nil {
				t.Fatal(err)
			}
			defer upload.Close()
			err = importDB(upload)
			var got model.Client
			switch scenario {
			case "legacy backup":
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Where("name = ?", "imported").First(&got).Error; err != nil || len(got.SubToken) != 43 || got.Volume != 42 || got.Up != 7 {
					t.Fatalf("old backup did not import with its data and a new token: %v", err)
				}
				backups, err := filepath.Glob(path + ".backup-*")
				if err != nil || len(backups) != 1 {
					t.Fatalf("expected a single copy of the previous database: %v, %v", backups, err)
				}
				old, err := sql.Open("sqlite3", backups[0])
				if err != nil {
					t.Fatal(err)
				}
				defer old.Close()
				var token string
				if err := old.QueryRow("SELECT sub_token FROM clients WHERE id = ?", live.Id).Scan(&token); err != nil || token != live.SubToken {
					t.Fatalf("the pre-import backup lost the original client's token: %v", err)
				}
			case "failed backfill", "busy live WAL":
				expected := "test token backfill failure"
				if scenario == "busy live WAL" {
					expected = "SQLite checkpoint incomplete"
				}
				if err == nil || !strings.Contains(err.Error(), expected) {
					t.Fatalf("import error = %v, want %q", err, expected)
				}
				if err := db.Where("name = ?", live.Name).First(&got).Error; err != nil || got.SubToken != live.SubToken || got.Volume != live.Volume {
					t.Fatalf("live data was not usable after the failed import: %v", err)
				}
			}
		})
	}
}
