package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// SetServiceDeletionVolumes freezes the first selection before any destructive
// step. Subsequent retries must use the saved selection, not new form values.
func (s *Store) SetServiceDeletionVolumes(ctx context.Context, id int64, service string, volumes []string) error {
	if volumes == nil {
		volumes = []string{}
	}
	data, err := json.Marshal(volumes)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE service_deletion_intents SET volumes = ?
		WHERE application_id = ? AND service_name = ? AND volumes IS NULL AND stage = 'schedules'`, string(data), id, service)
	if err != nil {
		return fmt.Errorf("save service deletion volumes: %w", err)
	}
	return nil
}
