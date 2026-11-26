package database

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alireza0/s-ui/cmd/migration"
	"github.com/alireza0/s-ui/config"
	"github.com/alireza0/s-ui/logger"
	"github.com/alireza0/s-ui/util/common"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var importMu sync.Mutex

func GetDb(exclude string) ([]byte, error) {
	excluded := make(map[string]bool)
	for _, name := range strings.Split(exclude, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			excluded[name] = true
		}
	}

	// CreateTemp, not a hand-built timestamp: two downloads in the same period
	// used to render the same name and delete the file from under each other.
	dir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, config.GetName()+"-backup-*.db")
	if err != nil {
		return nil, err
	}
	dbPath := tmp.Name()
	// SQLite opens the path itself; this handle only reserves the name.
	tmp.Close()
	defer os.Remove(dbPath)

	backupDb, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		return nil, err
	}
	backupClosed := false
	closeBackup := func() {
		if backupClosed {
			return
		}
		backupClosed = true
		if sqlDB, e := backupDb.DB(); e == nil {
			_ = sqlDB.Close()
		}
	}
	defer closeBackup()

	// Same list InitDB migrates, so no table can be live but unbacked-up.
	if err = backupDb.AutoMigrate(schemaModels()...); err != nil {
		return nil, err
	}

	for _, t := range schema() {
		if excluded[t.name] {
			continue
		}
		if err := t.copyRows(db, backupDb); err != nil {
			return nil, common.NewErrorf("backing up %s: %v", t.name, err)
		}
	}

	// Fold the WAL in, or the bytes read below miss whatever is still in it.
	if err = backupDb.Exec("PRAGMA wal_checkpoint(TRUNCATE);").Error; err != nil {
		return nil, err
	}
	closeBackup()

	return os.ReadFile(dbPath)
}

func ImportDB(file multipart.File) error {
	importMu.Lock()
	defer importMu.Unlock()
	if err := importDB(file); err != nil {
		return err
	}
	if err := SendSighup(); err != nil {
		return common.NewErrorf("Error restarting app: %v", err)
	}
	return nil
}

func importDB(file multipart.File) error {
	// Check if the file is a SQLite database
	isValidDb, err := IsSQLiteDB(file)
	if err != nil {
		return common.NewErrorf("Error checking db file format: %v", err)
	}
	if !isValidDb {
		return common.NewError("Invalid db file format")
	}

	// Reset the file reader to the beginning
	if _, err = file.Seek(0, 0); err != nil {
		return common.NewErrorf("Error resetting file reader: %v", err)
	}

	dbPath := config.GetDBPath()

	// Everything up to the rename works on the upload alone, and the live pool
	// stays open: closing it here means a failed import breaks every later
	// query with "sql: database is closed" until someone restarts the service.
	tempFile, err := os.CreateTemp(filepath.Dir(dbPath), filepath.Base(dbPath)+".import-*")
	if err != nil {
		return common.NewErrorf("Error creating temporary db file: %v", err)
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)
	_, err = io.Copy(tempFile, file)
	closeErr := tempFile.Close()
	if err != nil {
		return common.NewErrorf("Error saving db: %v", err)
	}
	if closeErr != nil {
		return common.NewErrorf("Error closing uploaded db: %v", closeErr)
	}

	// The header check above passes for any SQLite file at all, including a
	// browser profile. Importing one of those replaces the live database and
	// then crashes the migration, into a systemd restart loop.
	if err = validateImport(tempPath); err != nil {
		return err
	}

	fallbackFile, err := os.CreateTemp(filepath.Dir(dbPath), filepath.Base(dbPath)+".backup-"+time.Now().Format("20060102-150405")+"-*")
	if err != nil {
		return common.NewErrorf("Error reserving backup name: %v", err)
	}
	fallbackPath := fallbackFile.Name()
	reserved := true
	defer func() {
		if reserved {
			_ = os.Remove(fallbackPath)
		}
	}()
	if err = fallbackFile.Close(); err != nil {
		return common.NewErrorf("Error closing backup placeholder: %v", err)
	}
	if err = closeDBForImport(dbPath); err != nil {
		return common.NewErrorf("Error preparing current db for import: %v", err)
	}

	if err = os.Rename(dbPath, fallbackPath); err != nil {
		return errors.Join(fmt.Errorf("back up current database: %w", err), reopen(dbPath))
	}
	reserved = false

	if err = os.Rename(tempPath, dbPath); err != nil {
		return errors.Join(fmt.Errorf("move uploaded database: %w", err), restoreFallback(dbPath, fallbackPath))
	}

	if err = migration.MigrateDb(); err != nil {
		return errors.Join(fmt.Errorf("migrate uploaded database: %w", err), restoreFallback(dbPath, fallbackPath))
	}
	if err = InitDB(dbPath); err != nil {
		return errors.Join(fmt.Errorf("initialize uploaded database: %w", err), restoreFallback(dbPath, fallbackPath))
	}

	logger.Info("database imported; previous database kept at ", fallbackPath)

	return nil
}

func closeDBForImport(path string) error {
	pool, err := db.DB()
	if err != nil {
		return err
	}
	var busy, pages, checkpointed int
	if err := db.Raw("PRAGMA wal_checkpoint(TRUNCATE)").Row().Scan(&busy, &pages, &checkpointed); err != nil {
		return err
	}
	if busy != 0 || pages != checkpointed {
		return fmt.Errorf("SQLite checkpoint incomplete (%d of %d pages)", checkpointed, pages)
	}
	if err := pool.Close(); err != nil {
		return errors.Join(err, reopen(path))
	}
	// Another SQLite connection can keep the sidecars alive. Do not move the
	// main file or delete its WAL in that case.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			if err == nil {
				err = fmt.Errorf("SQLite sidecar still present: %s", suffix)
			}
			return errors.Join(err, reopen(path))
		}
	}
	return nil
}

// validateImport rejects a SQLite file that is not an s-ui database, before it
// can replace the live one.
func validateImport(path string) error {
	candidate, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		return common.NewErrorf("Error checking db: %v", err)
	}
	defer func() {
		if sqlDB, e := candidate.DB(); e == nil {
			_ = sqlDB.Close()
		}
	}()

	// settings carries the schema version and session secret; without clients
	// and inbounds it is not a panel backup whatever else it holds.
	for _, required := range []string{"settings", "clients", "inbounds"} {
		if !candidate.Migrator().HasTable(required) {
			return common.NewErrorf("Not an s-ui database: table %q is missing", required)
		}
	}
	return nil
}

// removeSidecars drops the -wal and -shm files belonging to path; they describe
// the database being replaced, not the one taking its place.
func removeSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// reopen restores the connection pool after an import gave up.
func reopen(path string) error {
	if err := OpenDB(path); err != nil {
		return fmt.Errorf("reopen previous database: %w", err)
	}
	return nil
}

func restoreFallback(dbPath, fallbackPath string) error {
	if err := removeSidecars(dbPath); err != nil {
		return fmt.Errorf("cannot restore the previous database from %q while imported sidecars exist: %w", fallbackPath, err)
	}
	if err := os.Rename(fallbackPath, dbPath); err != nil {
		return fmt.Errorf("restore previous database from %q: %w", fallbackPath, err)
	}
	return reopen(dbPath)
}

func IsSQLiteDB(file io.Reader) (bool, error) {
	signature := []byte("SQLite format 3\x00")
	buf := make([]byte, len(signature))
	// ReadFull: a single Read may return short and compare a partial buffer.
	if _, err := io.ReadFull(file, buf); err != nil {
		return false, err
	}
	return bytes.Equal(buf, signature), nil
}

func SendSighup() error {
	// Get the current process
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		return err
	}

	// Send SIGHUP to the current process
	go func() {
		time.Sleep(3 * time.Second)
		if runtime.GOOS == "windows" {
			err = process.Kill()
		} else {
			err = process.Signal(syscall.SIGHUP)
		}
		if err != nil {
			logger.Error("send signal SIGHUP failed:", err)
		}
	}()
	return nil
}
