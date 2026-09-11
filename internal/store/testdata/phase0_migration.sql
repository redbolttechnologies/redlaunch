INSERT INTO applications (id, name, folder_name, created_at)
VALUES (7, 'Legacy proxy application', 'proxy', '2026-01-01T00:00:00Z');

INSERT INTO services (
    id, application_id, name, created_at, service_type, image_name,
    postgres_version, database_name, database_user,
    redis_version, redis_port, redis_persist_to_disk
) VALUES
    (11, 7, 'db-primary', '2026-01-01T00:00:01Z', 'postgresql', 'postgres:17', '17', 'primary', 'primary_user', '', '', 0),
    (12, 7, 'db-analytics', '2026-01-01T00:00:02Z', 'postgresql', 'postgres:16', '16', 'analytics', 'analytics_user', '', '', 0);

INSERT INTO domains (id, application_id, name)
VALUES (21, 7, 'example.test');

INSERT INTO routings (
    id, application_id, domain_id, subdomain, path, service_name, service_path
) VALUES (31, 7, 21, 'api', '/', 'db-primary', '/');

INSERT INTO backup_schedules (
    service_id, enabled, schedule_type, hour, minute, weekday,
    retention_days, backup_location, last_backup_at, last_backup_status,
    last_backup_size
) VALUES (11, 1, 'daily', 3, 0, '', 14, '/var/backups/redlaunch/7/11', '', '', 0);
