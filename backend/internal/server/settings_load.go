package server

import (
	"context"
	"errors"

	"github.com/litebase/litebase/internal/database"
	"github.com/litebase/litebase/internal/storage"
)

// ErrSettingNotFound means the setting has never been saved.
var ErrSettingNotFound = errors.New("setting not found")

// LoadGoogleDriveProvider rebuilds the Drive provider from saved settings.
//
// It runs at startup so a connection configured through the dashboard survives
// a restart without the operator reconnecting.
func LoadGoogleDriveProvider(ctx context.Context, mgr *database.Manager, secret string) (*storage.GoogleDrive, error) {
	store := &settingsStore{
		db:     mgr.MetaDB(),
		readDB: mgr.MetaRead(),
		secret: secret,
	}

	var cfg storage.GoogleDriveConfig
	found, err := store.getJSON(ctx, gdriveSettingKey, &cfg)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrSettingNotFound
	}
	return storage.NewGoogleDrive(cfg)
}
