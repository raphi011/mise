// Package backup writes the data of a mise installation into one archive
// (docs/plan.md, Operations; restore steps in docs/operations.md).
//
// The archive mirrors the data directory: mise.db, a consistent copy of the
// SQLite database, and images/... . A Postgres database is not included;
// back it up with pg_dump.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/raphi011/mise/internal/store"
)

// DatabaseName is the SQLite database's name in the archive, the default
// name in the data directory.
const DatabaseName = "mise.db"

// Result describes a written archive.
type Result struct {
	// Path is the archive's path.
	Path string
	// Database reports whether the archive holds the database (SQLite only).
	Database bool
	// Images is the number of image files archived.
	Images int
}

// Write archives the database (on SQLite) and dataDir/images into
// outDir/mise-backup-<UTC time>.tar.gz. The archive is written under a
// temporary name and renamed when complete, so a failed or interrupted
// backup never leaves a truncated archive under the final name. It is
// readable by the owner only: it holds password hashes.
func Write(ctx context.Context, db *store.DB, dataDir, outDir string, now time.Time) (res Result, err error) {
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return Result{}, fmt.Errorf("backup: create %s: %w", outDir, err)
	}
	final := filepath.Join(outDir, "mise-backup-"+now.UTC().Format("20060102T150405Z")+".tar.gz")
	if _, err := os.Lstat(final); err == nil {
		return Result{}, fmt.Errorf("backup: %s already exists", final)
	}

	f, err := os.CreateTemp(outDir, ".mise-backup-*.tmp")
	if err != nil {
		return Result{}, fmt.Errorf("backup: %w", err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if db.Dialect() == store.SQLite {
		if err := addDatabase(ctx, tw, db, outDir, now); err != nil {
			return Result{}, err
		}
		res.Database = true
	}
	if res.Images, err = addImages(ctx, tw, filepath.Join(dataDir, "images")); err != nil {
		return Result{}, err
	}
	if err := errors.Join(tw.Close(), gz.Close(), f.Sync(), f.Close()); err != nil {
		return Result{}, fmt.Errorf("backup: write archive: %w", err)
	}
	if err := os.Rename(f.Name(), final); err != nil {
		return Result{}, fmt.Errorf("backup: %w", err)
	}
	res.Path = final
	return res, nil
}

// addDatabase snapshots the SQLite database next to the archive (the data
// may not fit in a temp filesystem) and adds it as DatabaseName.
func addDatabase(ctx context.Context, tw *tar.Writer, db *store.DB, outDir string, now time.Time) error {
	dir, err := os.MkdirTemp(outDir, ".mise-backup-db-*")
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	snap := filepath.Join(dir, DatabaseName)
	if err := db.VacuumInto(ctx, snap); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	info, err := os.Stat(snap)
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	hdr.Name = DatabaseName
	hdr.Mode = 0o600
	hdr.ModTime = now
	return addFile(tw, hdr, snap)
}

// addImages adds the regular files and directories under root as
// images/..., skipping anything else. A missing root adds nothing.
func addImages(ctx context.Context, tw *tar.Writer, root string) (int, error) {
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = "images/" + filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "." {
				hdr.Name = "images"
			}
			hdr.Name += "/"
			return tw.WriteHeader(hdr)
		}
		n++
		return addFile(tw, hdr, path)
	})
	if err != nil {
		return 0, fmt.Errorf("backup: images: %w", err)
	}
	return n, nil
}

func addFile(tw *tar.Writer, hdr *tar.Header, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	// The header's size comes from an earlier stat; a file that changed
	// since then fails the copy instead of corrupting the archive.
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("%s: %w", hdr.Name, err)
	}
	return nil
}
