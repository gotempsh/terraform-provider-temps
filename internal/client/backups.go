// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"context"
	"net/http"
	"net/url"
)

// S3Source is a backup destination. AccessKeyID and SecretKey are always
// masked in responses.
type S3Source struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	BucketName     string  `json:"bucket_name"`
	BucketPath     string  `json:"bucket_path"`
	Region         string  `json:"region"`
	Endpoint       *string `json:"endpoint"`
	ForcePathStyle *bool   `json:"force_path_style"`
	IsDefault      bool    `json:"is_default"`
	AccessKeyID    string  `json:"access_key_id"`
	SecretKey      string  `json:"secret_key"`
}

// S3SourceRequest creates, updates or tests a backup destination. For an
// update, nil fields are unchanged and the credentials may be omitted.
type S3SourceRequest struct {
	Name           *string `json:"name,omitempty"`
	BucketName     *string `json:"bucket_name,omitempty"`
	BucketPath     *string `json:"bucket_path,omitempty"`
	Region         *string `json:"region,omitempty"`
	Endpoint       *string `json:"endpoint,omitempty"`
	ForcePathStyle *bool   `json:"force_path_style,omitempty"`
	UseTLS         *bool   `json:"use_tls,omitempty"`
	IsDefault      *bool   `json:"is_default,omitempty"`
	AccessKeyID    *string `json:"access_key_id,omitempty"`
	SecretKey      *string `json:"secret_key,omitempty"`
}

// ConnectionTest is the result of an S3 connectivity check.
type ConnectionTest struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// CreateS3Source creates a backup destination.
func (c *Client) CreateS3Source(ctx context.Context, req S3SourceRequest) (*S3Source, error) {
	var s S3Source
	return &s, c.do(ctx, http.MethodPost, "/backups/s3-sources", nil, req, &s)
}

// GetS3Source reads a backup destination.
// ListS3Sources returns every backup destination (the endpoint is not paginated).
func (c *Client) ListS3Sources(ctx context.Context) ([]S3Source, error) {
	var out []S3Source
	return out, c.do(ctx, http.MethodGet, "/backups/s3-sources", nil, nil, &out)
}

func (c *Client) GetS3Source(ctx context.Context, id int64) (*S3Source, error) {
	var s S3Source
	return &s, c.do(ctx, http.MethodGet, idPath("/backups/s3-sources/%d", id), nil, nil, &s)
}

// UpdateS3Source partially updates a backup destination.
func (c *Client) UpdateS3Source(ctx context.Context, id int64, req S3SourceRequest) (*S3Source, error) {
	var s S3Source
	return &s, c.do(ctx, http.MethodPatch, idPath("/backups/s3-sources/%d", id), nil, req, &s)
}

// SetDefaultS3Source makes a destination the default one.
func (c *Client) SetDefaultS3Source(ctx context.Context, id int64) (*S3Source, error) {
	var s S3Source
	return &s, c.do(ctx, http.MethodPost, idPath("/backups/s3-sources/%d/set-default", id), nil, nil, &s)
}

// TestS3Connection checks credentials and bucket reachability before saving.
func (c *Client) TestS3Connection(ctx context.Context, req S3SourceRequest) (*ConnectionTest, error) {
	var t ConnectionTest
	return &t, c.do(ctx, http.MethodPost, "/backups/s3-sources/test", nil, req, &t)
}

// DeleteS3Source deletes a destination. The server refuses while backups or
// schedules still reference it.
func (c *Client) DeleteS3Source(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/backups/s3-sources/%d", id), nil, nil, nil)
}

// BackupSummary is one backup stored in a destination.
type BackupSummary struct {
	ID       int64  `json:"id"`
	BackupID string `json:"backup_id"`
	State    string `json:"state"`
}

// ListS3SourceBackups lists the backups stored in a destination (first 100).
func (c *Client) ListS3SourceBackups(ctx context.Context, id int64) ([]BackupSummary, error) {
	var out struct {
		Backups []BackupSummary `json:"backups"`
	}
	q := url.Values{}
	q.Set("page_size", "100")
	return out.Backups, c.do(ctx, http.MethodGet, idPath("/backups/s3-sources/%d/backups", id), q, nil, &out)
}

// DeleteBackup permanently deletes one backup by its UUID.
func (c *Client) DeleteBackup(ctx context.Context, backupUUID string) error {
	return c.do(ctx, http.MethodDelete, "/backups/"+url.PathEscape(backupUUID), nil, nil, nil)
}

// BackupSchedule is a recurring backup job.
type BackupSchedule struct {
	ID                  int64    `json:"id"`
	Name                string   `json:"name"`
	Description         *string  `json:"description"`
	BackupType          string   `json:"backup_type"`
	RetentionPeriod     int64    `json:"retention_period"`
	S3SourceID          int64    `json:"s3_source_id"`
	ScheduleExpression  string   `json:"schedule_expression"`
	Enabled             bool     `json:"enabled"`
	Tags                []string `json:"tags"`
	TargetAllServices   bool     `json:"target_all_services"`
	IncludeControlPlane bool     `json:"include_control_plane"`
	MaxRuntimeSecs      *int64   `json:"max_runtime_secs"`
	// NextRun and LastRun are null until the schedule has a next or a
	// first run.
	NextRun *Timestamp `json:"next_run"`
	LastRun *Timestamp `json:"last_run"`
}

// CreateBackupScheduleRequest is the body of POST /backups/schedules.
type CreateBackupScheduleRequest struct {
	Name                string   `json:"name"`
	Description         *string  `json:"description,omitempty"`
	BackupType          string   `json:"backup_type"`
	RetentionPeriod     int64    `json:"retention_period"`
	S3SourceID          *int64   `json:"s3_source_id,omitempty"`
	ScheduleExpression  string   `json:"schedule_expression"`
	Enabled             bool     `json:"enabled"`
	Tags                []string `json:"tags"`
	ServiceIDs          []int64  `json:"service_ids"`
	TargetAllServices   *bool    `json:"target_all_services,omitempty"`
	IncludeControlPlane *bool    `json:"include_control_plane,omitempty"`
	MaxRuntimeSecs      *int64   `json:"max_runtime_secs,omitempty"`
}

// UpdateBackupScheduleRequest is the body of PATCH /backups/schedules/{id}.
type UpdateBackupScheduleRequest struct {
	Name                *string   `json:"name,omitempty"`
	Description         *string   `json:"description,omitempty"`
	RetentionPeriod     *int64    `json:"retention_period,omitempty"`
	ScheduleExpression  *string   `json:"schedule_expression,omitempty"`
	Enabled             *bool     `json:"enabled,omitempty"`
	Tags                *[]string `json:"tags,omitempty"`
	ServiceIDs          *[]int64  `json:"service_ids,omitempty"`
	TargetAllServices   *bool     `json:"target_all_services,omitempty"`
	IncludeControlPlane *bool     `json:"include_control_plane,omitempty"`
	MaxRuntimeSecs      *int64    `json:"max_runtime_secs,omitempty"`
}

// CreateBackupSchedule creates a schedule.
func (c *Client) CreateBackupSchedule(ctx context.Context, req CreateBackupScheduleRequest) (*BackupSchedule, error) {
	var s BackupSchedule
	return &s, c.do(ctx, http.MethodPost, "/backups/schedules", nil, req, &s)
}

// GetBackupSchedule reads a schedule.
func (c *Client) GetBackupSchedule(ctx context.Context, id int64) (*BackupSchedule, error) {
	var s BackupSchedule
	return &s, c.do(ctx, http.MethodGet, idPath("/backups/schedules/%d", id), nil, nil, &s)
}

// UpdateBackupSchedule partially updates a schedule.
func (c *Client) UpdateBackupSchedule(ctx context.Context, id int64, req UpdateBackupScheduleRequest) (*BackupSchedule, error) {
	var s BackupSchedule
	return &s, c.do(ctx, http.MethodPatch, idPath("/backups/schedules/%d", id), nil, req, &s)
}

// SetBackupScheduleEnabled enables or disables a schedule.
func (c *Client) SetBackupScheduleEnabled(ctx context.Context, id int64, enabled bool) error {
	action := "disable"
	if enabled {
		action = "enable"
	}
	return c.do(ctx, http.MethodPatch, idPath("/backups/schedules/%d/", id)+action, nil, nil, nil)
}

// DeleteBackupSchedule deletes a schedule.
func (c *Client) DeleteBackupSchedule(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, idPath("/backups/schedules/%d", id), nil, nil, nil)
}

// ListBackupScheduleServices lists the service IDs a schedule backs up.
func (c *Client) ListBackupScheduleServices(ctx context.Context, id int64) ([]int64, error) {
	var out []struct {
		ID int64 `json:"id"`
	}
	if err := c.do(ctx, http.MethodGet, idPath("/backups/schedules/%d/services", id), nil, nil, &out); err != nil {
		return nil, err
	}
	ids := make([]int64, len(out))
	for i, s := range out {
		ids[i] = s.ID
	}
	return ids, nil
}

// ScheduleRun is the result of triggering a schedule.
type ScheduleRun struct {
	ScheduleRunID int64 `json:"schedule_run_id"`
}

// RunBackupSchedule triggers a schedule now.
func (c *Client) RunBackupSchedule(ctx context.Context, id int64) (*ScheduleRun, error) {
	var r ScheduleRun
	return &r, c.do(ctx, http.MethodPost, idPath("/backups/schedules/%d/run", id), nil, nil, &r)
}
