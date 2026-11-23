package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tobocop2/beehave/internal/proc/gitexec"
)

// ownerLoginFile is the record of the credential settings of the owner in the state folder.
const ownerLoginFile = "owner-login.json"

// loginRecord is the content of the record file.
type loginRecord struct {
	Settings []string `json:"settings"`
}

// readLogin loads the owner login from the record in the state folder, or reads it from the git config of the owner and records it when there is no record.
func (w *writers) readLogin(ctx context.Context, stateDir string) error {
	file := filepath.Join(stateDir, ownerLoginFile)

	data, err := os.ReadFile(file) //nolint:gosec // the record is a file of the state folder
	if err == nil {
		return w.loadLogin(file, data)
	}

	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read the record of the owner login: %w", err)
	}

	login, err := gitexec.ReadOwnerLogin(ctx)
	if err != nil {
		return err
	}

	w.login = login

	return recordLogin(file, login)
}

// loadLogin sets the owner login from the content data of the record file.
func (w *writers) loadLogin(file string, data []byte) error {
	var record loginRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return fmt.Errorf("read the record of the owner login %s: %w. Delete the file to read the git config of the owner again", file, err)
	}

	w.login = gitexec.OwnerLoginOf(record.Settings)

	return nil
}

// recordLogin writes the record file once. A relative state folder is not recorded, because the options of the flow refuse it.
func recordLogin(file string, login gitexec.OwnerLogin) error {
	if !filepath.IsAbs(file) {
		return nil
	}

	data, err := json.Marshal(loginRecord{Settings: login.Settings()})
	if err == nil {
		err = os.MkdirAll(filepath.Dir(file), ownerOnly)
	}

	if err == nil {
		err = os.WriteFile(file, data, reportMode)
	}

	if err != nil {
		return fmt.Errorf("write the record of the owner login: %w", err)
	}

	return nil
}
