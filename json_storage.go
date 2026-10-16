package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

var errDamagedJSONFile = errors.New("damaged JSON file")

// Never truncate the live file: publish a fully written, synced sibling instead.
func replaceFile(filename string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(filename), "."+filepath.Base(filename)+"-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name) // Only this call's newly created temporary file.
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, filename)
}

func saveJSONFile(filename string, data []byte) error {
	if !json.Valid(data) {
		return errors.New("refusing to save invalid JSON")
	}
	if old, err := os.ReadFile(filename); err == nil {
		if json.Valid(old) {
			if err := replaceFile(filename+".bak", old, 0600); err != nil {
				return fmt.Errorf("save previous JSON backup: %w", err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return replaceFile(filename, data, 0600)
}

func preserveDamagedJSON(filename string, data []byte) {
	f, err := os.CreateTemp(filepath.Dir(filename), filepath.Base(filename)+".corrupt-*")
	if err != nil {
		log.Printf("cannot preserve damaged %s; original left untouched: %v", filepath.Base(filename), err)
		return
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		log.Printf("cannot finish damaged JSON recovery copy for %s", filepath.Base(filename))
	}
}

// decode must reset its destination before each attempt. A broken primary is
// retained; recovery never silently replaces credentials with default config.
func loadJSONFile(filename string, decode func([]byte) error) error {
	data, readErr := os.ReadFile(filename)
	var decodeErr error
	if readErr == nil {
		decodeErr = decode(data)
		if decodeErr == nil {
			return nil
		}
	}
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if readErr == nil {
		preserveDamagedJSON(filename, data)
	}
	if backup, err := os.ReadFile(filename + ".bak"); err == nil {
		if backupErr := decode(backup); backupErr == nil {
			log.Printf("recovered %s from last valid backup", filepath.Base(filename))
			return nil
		} else if readErr != nil {
			return fmt.Errorf("%w: invalid backup for %s", errDamagedJSONFile, filepath.Base(filename))
		}
	}
	if readErr != nil {
		return readErr
	}
	return fmt.Errorf("%w: %s: %v", errDamagedJSONFile, filepath.Base(filename), decodeErr)
}
