package gateway

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// ExtractSiteBackup requires a new output directory. Authentication of the
// entire file finishes before any extracted configuration becomes visible.
func ExtractSiteBackup(inputPath, outputDirectory, password string) error {
	if password == "" || len(password) > 1024 {
		return errors.New("set BACKUP_PASSWORD to the backup password")
	}
	output, err := filepath.Abs(outputDirectory)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(output); err == nil {
		return errors.New("output directory already exists; choose a new directory")
	} else if !os.IsNotExist(err) {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(output), ".gateway-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	source, err := os.Open(inputPath)
	if err != nil {
		return errors.New("cannot open backup file")
	}
	defer source.Close()
	archive, err := os.OpenFile(filepath.Join(stage, "archive.tar"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer archive.Close()
	if err = decryptBackup(archive, source, password); err != nil {
		return err
	}
	if _, err = archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	files := map[string]bool{"database.dump": false, "manifest.json": false, ".env.restore": false, "RESTORE.md": false}
	reader := tar.NewReader(archive)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("invalid backup archive")
		}
		seen, allowed := files[header.Name]
		if !allowed || seen || header.Typeflag != tar.TypeReg || header.Size < 0 || (header.Name != "database.dump" && header.Size > 1<<20) {
			return errors.New("unexpected file in backup archive")
		}
		file, err := os.OpenFile(filepath.Join(stage, header.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(file, reader)
		closeErr := file.Close()
		if copyErr != nil {
			return errors.New("backup archive is incomplete")
		}
		if closeErr != nil {
			return closeErr
		}
		files[header.Name] = true
	}
	for _, found := range files {
		if !found {
			return errors.New("backup archive is missing required files")
		}
	}
	metadata, err := os.ReadFile(filepath.Join(stage, "manifest.json"))
	if err != nil {
		return err
	}
	var manifest backupManifest
	if json.Unmarshal(metadata, &manifest) != nil || manifest.Format != "aigw-backup-v1" {
		return errors.New("unsupported backup format")
	}
	dump, err := os.Open(filepath.Join(stage, "database.dump"))
	if err != nil {
		return err
	}
	var signature [5]byte
	_, err = io.ReadFull(dump, signature[:])
	dump.Close()
	if err != nil || string(signature[:]) != "PGDMP" {
		return errors.New("invalid PostgreSQL dump in backup")
	}
	archive.Close()
	if err = os.Remove(filepath.Join(stage, "archive.tar")); err != nil {
		return err
	}
	// Rename publishes only the fully decrypted and validated directory.
	if _, err = os.Lstat(output); !os.IsNotExist(err) {
		return errors.New("output directory already exists; choose a new directory")
	}
	return os.Rename(stage, output)
}
