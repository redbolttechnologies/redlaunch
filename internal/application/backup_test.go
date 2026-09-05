package application

import (
	"errors"
	"testing"
)

func TestValidateBackupScheduleInput(t *testing.T) {
	tests := []struct {
		name  string
		input BackupScheduleInput
		want  BackupScheduleInput
	}{
		{
			name:  "hourly",
			input: BackupScheduleInput{Enabled: true, ScheduleType: " HOURLY ", Hour: 23, Minute: 7, RetentionDays: 14},
			want:  BackupScheduleInput{Enabled: true, ScheduleType: BackupScheduleHourly, Hour: 0, Minute: 7, RetentionDays: 14},
		},
		{
			name:  "weekly",
			input: BackupScheduleInput{Enabled: true, ScheduleType: BackupScheduleWeekly, Hour: 3, Minute: 5, Weekday: " SUNDAY ", RetentionDays: 30},
			want:  BackupScheduleInput{Enabled: true, ScheduleType: BackupScheduleWeekly, Hour: 3, Minute: 5, Weekday: "sunday", RetentionDays: 30},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := ValidateBackupScheduleInput(testCase.input)
			if err != nil {
				t.Fatal(err)
			}
			if got != testCase.want {
				t.Fatalf("ValidateBackupScheduleInput() = %#v, want %#v", got, testCase.want)
			}
		})
	}

	for _, testCase := range []struct {
		name    string
		input   BackupScheduleInput
		wantErr error
	}{
		{name: "type", input: BackupScheduleInput{ScheduleType: "monthly", Minute: 0, RetentionDays: 14}, wantErr: ErrBackupScheduleTypeInvalid},
		{name: "hour", input: BackupScheduleInput{ScheduleType: BackupScheduleDaily, Hour: 24, Minute: 0, RetentionDays: 14}, wantErr: ErrBackupHourInvalid},
		{name: "minute", input: BackupScheduleInput{ScheduleType: BackupScheduleDaily, Hour: 3, Minute: 60, RetentionDays: 14}, wantErr: ErrBackupMinuteInvalid},
		{name: "weekday", input: BackupScheduleInput{ScheduleType: BackupScheduleWeekly, Hour: 3, Minute: 0, Weekday: "someday", RetentionDays: 14}, wantErr: ErrBackupWeekdayInvalid},
		{name: "retention", input: BackupScheduleInput{ScheduleType: BackupScheduleDaily, Hour: 3, Minute: 0, RetentionDays: 0}, wantErr: ErrBackupRetentionInvalid},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := ValidateBackupScheduleInput(testCase.input); !errors.Is(err, testCase.wantErr) {
				t.Fatalf("ValidateBackupScheduleInput() error = %v, want %v", err, testCase.wantErr)
			}
		})
	}
}

func TestValidateBackupFileNameRejectsPaths(t *testing.T) {
	for _, value := range []string{"../backup.sql", "backup/other.sql", "backup-foo.txt", "backup-foo.sql\n"} {
		if _, err := ValidateBackupFileName(value); !errors.Is(err, ErrBackupFileNameInvalid) {
			t.Fatalf("ValidateBackupFileName(%q) error = %v, want %v", value, err, ErrBackupFileNameInvalid)
		}
	}
	if got, err := ValidateBackupFileName("backup-20260901-030000.000000000Z.sql"); err != nil || got == "" {
		t.Fatalf("ValidateBackupFileName(valid) = %q, %v", got, err)
	}
}
