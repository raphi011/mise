package backup_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raphi011/mise/internal/backup"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

var at = time.Date(2026, 11, 9, 12, 0, 0, 0, time.FixedZone("CET", 3600))

const wantName = "mise-backup-20261109T110000Z.tar.gz"

// writeImage puts a fake image file into dataDir like images.Store does.
func writeImage(t *testing.T, dataDir string) (rel string, data []byte) {
	t.Helper()
	rel = filepath.Join("images", "0198a3f2-0000-7000-8000-000000000001", strings.Repeat("ab", 32)+"-full.jpg")
	data = []byte("\xff\xd8 not really a jpeg")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dataDir, rel)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, rel), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return rel, data
}

// extract unpacks a .tar.gz into dir and returns the regular files' names.
func extract(t *testing.T, archive, dir string) []string {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var files []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, filepath.FromSlash(hdr.Name))
		if !strings.HasPrefix(path, dir+string(filepath.Separator)) {
			t.Fatalf("entry %q escapes the target", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o750); err != nil {
				t.Fatal(err)
			}
		case tar.TypeReg:
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			files = append(files, hdr.Name)
		default:
			t.Fatalf("entry %q: unexpected type %c", hdr.Name, hdr.Typeflag)
		}
	}
	return files
}

func TestWrite(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := context.Background()
		h := storetest.NewHousehold(t, db, "Home")
		dataDir, outDir := t.TempDir(), filepath.Join(t.TempDir(), "backups")
		rel, img := writeImage(t, dataDir)

		res, err := backup.Write(ctx, db, dataDir, outDir, at)
		if err != nil {
			t.Fatal(err)
		}
		if res.Path != filepath.Join(outDir, wantName) || res.Images != 1 {
			t.Errorf("got %+v", res)
		}
		sqlite := db.Dialect() == store.SQLite
		if res.Database != sqlite {
			t.Errorf("Database = %v on %v", res.Database, db.Dialect())
		}
		info, err := os.Stat(res.Path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("archive mode = %v, want 0600", perm)
		}
		if left, _ := filepath.Glob(filepath.Join(outDir, ".mise-backup-*")); len(left) > 0 {
			t.Errorf("temp files left behind: %v", left)
		}

		restored := t.TempDir()
		files := extract(t, res.Path, restored)
		got, err := os.ReadFile(filepath.Join(restored, rel))
		if err != nil || !bytes.Equal(got, img) {
			t.Errorf("image: %q, %v", got, err)
		}
		hasDB := false
		for _, f := range files {
			hasDB = hasDB || f == backup.DatabaseName
		}
		if hasDB != sqlite {
			t.Fatalf("archive files %v on %v", files, db.Dialect())
		}
		if !sqlite {
			return
		}

		// The restored database opens, is fully migrated and holds the data.
		rdb, err := store.Open(ctx, "sqlite://"+filepath.Join(restored, backup.DatabaseName))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rdb.Close() }()
		if pending, err := rdb.MigrationsPending(ctx); err != nil || pending {
			t.Errorf("MigrationsPending = %v, %v", pending, err)
		}
		var name string
		if err := rdb.System().Read().Get(ctx, &name, `SELECT name FROM households WHERE id = ?`, h.ID); err != nil || name != "Home" {
			t.Errorf("household: %q, %v", name, err)
		}
	})
}

func TestWriteWithoutImages(t *testing.T) {
	db := storetest.SQLite(t)
	res, err := backup.Write(context.Background(), db, t.TempDir(), t.TempDir(), at)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Database || res.Images != 0 {
		t.Errorf("got %+v", res)
	}
}

func TestWriteRefusesToOverwrite(t *testing.T) {
	db := storetest.SQLite(t)
	outDir := t.TempDir()
	existing := filepath.Join(outDir, wantName)
	if err := os.WriteFile(existing, []byte("older"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Write(context.Background(), db, t.TempDir(), outDir, at); err == nil {
		t.Fatal("Write succeeded over an existing archive")
	}
	if got, _ := os.ReadFile(existing); string(got) != "older" {
		t.Errorf("existing archive changed: %q", got)
	}
}

func TestWriteRefusesUnmigratedDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "mise.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	outDir := t.TempDir()
	if _, err := backup.Write(ctx, db, t.TempDir(), outDir, at); !errors.Is(err, backup.ErrNoSchema) {
		t.Fatalf("Write = %v, want ErrNoSchema", err)
	}
	if left, _ := os.ReadDir(outDir); len(left) > 0 {
		t.Errorf("files left in %s: %v", outDir, left)
	}
}

// The server may be uploading or sweeping images during a backup.
func TestWriteSkipsTempAndVanishedImages(t *testing.T) {
	db := storetest.SQLite(t)
	dataDir := t.TempDir()
	rel, _ := writeImage(t, dataDir)
	dir := filepath.Dir(filepath.Join(dataDir, rel))
	for _, name := range []string{".upload-123", "gone-full.jpg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gone := filepath.Join(dir, "gone-full.jpg")
	backup.SetBeforeOpen(t, func(path string) {
		if path == gone {
			if err := os.Remove(path); err != nil {
				t.Error(err)
			}
		}
	})

	res, err := backup.Write(context.Background(), db, dataDir, t.TempDir(), at)
	if err != nil {
		t.Fatal(err)
	}
	if res.Images != 1 {
		t.Errorf("Images = %d, want 1", res.Images)
	}
	files := extract(t, res.Path, t.TempDir())
	want := []string{backup.DatabaseName, filepath.ToSlash(rel)}
	if strings.Join(files, ",") != strings.Join(want, ",") {
		t.Errorf("archive files %v, want %v", files, want)
	}
}
