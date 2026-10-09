package store

import (
	"context"
	"encoding/json"
	"fmt"

	"redlaunch/internal/application"
)

func (s *Store) ListProxySettings(ctx context.Context) (map[int64]application.ProxySettings, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT scope, settings FROM proxy_settings ORDER BY scope`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64]application.ProxySettings)
	for rows.Next() {
		var id int64
		var contents string
		var settings application.ProxySettings
		if err := rows.Scan(&id, &contents); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(contents), &settings); err != nil {
			return nil, fmt.Errorf("decode proxy settings: %w", err)
		}
		if err := settings.Validate(); err != nil {
			return nil, err
		}
		result[id] = settings
	}
	return result, rows.Err()
}

func (s *Store) SaveProxySettings(ctx context.Context, id int64, settings application.ProxySettings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	contents, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	var appID any
	if id > 0 {
		appID = id
	} else if id != 0 {
		return application.ErrNotFound
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO proxy_settings(scope, application_id, settings) VALUES(?,?,?) ON CONFLICT(scope) DO UPDATE SET settings=excluded.settings`, id, appID, string(contents))
	return err
}
