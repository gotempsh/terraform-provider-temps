// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var backupDestinationPermissions = []string{"backups:create", "backups:read", "backups:write", "backups:delete"}

// createS3SourceMu serializes destination creation within one Terraform run.
// Temps makes the first destination the default, and two concurrent creates on
// a server without one race for that: one of them fails with a 500.
var createS3SourceMu sync.Mutex

// Bounds for the delete paths that wait on the server.
var (
	// defaultDeleteWait is how long deleting the default destination waits for
	// the other destinations (being destroyed in parallel) to disappear.
	defaultDeleteWait = 2 * time.Minute
	// backupDeleteAttempts bounds retries of a backup delete that fails with a
	// server error during force_destroy.
	backupDeleteAttempts = 4
)

var (
	_ resource.Resource                = &backupDestinationResource{}
	_ resource.ResourceWithImportState = &backupDestinationResource{}
)

type backupDestinationResource struct{ resourceBase }

func newBackupDestinationResource() resource.Resource { return &backupDestinationResource{} }

type backupDestinationModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	BucketName     types.String `tfsdk:"bucket_name"`
	BucketPath     types.String `tfsdk:"bucket_path"`
	Region         types.String `tfsdk:"region"`
	Endpoint       types.String `tfsdk:"endpoint"`
	ForcePathStyle types.Bool   `tfsdk:"force_path_style"`
	UseTLS         types.Bool   `tfsdk:"use_tls"`
	AccessKeyID    types.String `tfsdk:"access_key_id"`
	SecretKey      types.String `tfsdk:"secret_key"`
	IsDefault      types.Bool   `tfsdk:"is_default"`
	VerifyOnApply  types.Bool   `tfsdk:"verify_on_apply"`
	ForceDestroy   types.Bool   `tfsdk:"force_destroy"`
}

// trueOnly rejects an explicit false: Temps always keeps one default
// destination, so "not default" can only be reached by making another one default.
type trueOnly struct{}

func (trueOnly) Description(context.Context) string { return "must be true when set" }
func (v trueOnly) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (trueOnly) ValidateBool(_ context.Context, req validator.BoolRequest, resp *validator.BoolResponse) {
	if known(req.ConfigValue) && !req.ConfigValue.ValueBool() {
		resp.Diagnostics.AddAttributeError(req.Path, "is_default can only be set to true",
			"Temps always has one default backup destination; omit is_default, or set it to true on the destination that should be the default.")
	}
}

func (r *backupDestinationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_backup_destination"
}

func (r *backupDestinationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An S3-compatible bucket Temps writes backups to (AWS S3, MinIO, RustFS, ...).\n\n" +
			"The server never returns the credentials (they read back as `***`), so the provider keeps the configured " +
			"values and cannot detect credential changes made outside Terraform. Destroying a destination that still holds " +
			"backups fails unless `force_destroy = true`, which deletes those backups first.\n\n" +
			"Required API key permissions: " + permList(backupDestinationPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric destination ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name":        schema.StringAttribute{Required: true, MarkdownDescription: "Display name.", Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"bucket_name": schema.StringAttribute{Required: true, MarkdownDescription: "Bucket name. The bucket must already exist."},
			"bucket_path": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString(""),
				MarkdownDescription: "Key prefix inside the bucket. Defaults to the bucket root.",
			},
			"region": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("us-east-1"),
				MarkdownDescription: "Bucket region. Defaults to `us-east-1`, which most S3-compatible servers accept.",
			},
			"endpoint": schema.StringAttribute{
				Optional: true, MarkdownDescription: "Endpoint URL for S3-compatible servers, for example `https://s3.example.com`. Omit for AWS S3.",
			},
			"force_path_style": schema.BoolAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Use path-style addressing (`endpoint/bucket/key`), which most self-hosted servers need.",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"use_tls": schema.BoolAttribute{
				Optional: true, MarkdownDescription: "Whether to connect with TLS. Not returned by the server; the configured value is kept.",
			},
			"access_key_id": schema.StringAttribute{Required: true, Sensitive: true, MarkdownDescription: "Access key ID."},
			"secret_key":    schema.StringAttribute{Required: true, Sensitive: true, MarkdownDescription: "Secret access key."},
			"is_default": schema.BoolAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Make this the default destination (used by schedules without `s3_source_id`). " +
					"Temps makes the first destination the default automatically. Only `true` can be set. " +
					"Temps does not delete the default while other destinations exist: when they are destroyed in the same run, " +
					"the provider waits up to two minutes for them before deleting the default.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
				Validators:    []validator.Bool{trueOnly{}},
			},
			"verify_on_apply": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Check the credentials and bucket before saving, and fail the apply when they do not work.",
			},
			"force_destroy": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "On destroy, permanently delete the backups stored in this destination first. Without it, destroying a destination that holds backups fails.",
			},
		},
	}
}

func (m *backupDestinationModel) request() client.S3SourceRequest {
	return client.S3SourceRequest{
		Name: strPtr(m.Name), BucketName: strPtr(m.BucketName), BucketPath: strPtr(m.BucketPath),
		Region: strPtr(m.Region), Endpoint: strPtr(m.Endpoint), ForcePathStyle: boolPtr(m.ForcePathStyle),
		UseTLS: boolPtr(m.UseTLS), AccessKeyID: strPtr(m.AccessKeyID), SecretKey: strPtr(m.SecretKey),
	}
}

func (m *backupDestinationModel) fromAPI(s *client.S3Source) {
	m.ID = types.StringValue(formatID(s.ID))
	m.Name = types.StringValue(s.Name)
	m.BucketName = types.StringValue(s.BucketName)
	m.BucketPath = types.StringValue(s.BucketPath)
	m.Region = types.StringValue(s.Region)
	m.Endpoint = types.StringPointerValue(s.Endpoint)
	m.ForcePathStyle = types.BoolValue(s.ForcePathStyle != nil && *s.ForcePathStyle)
	m.IsDefault = types.BoolValue(s.IsDefault)
	// Credentials come back masked: keep the configured values. Only a
	// server that actually returns a value (never "***") updates state.
	if s.AccessKeyID != "" && s.AccessKeyID != client.MaskedValue {
		m.AccessKeyID = types.StringValue(s.AccessKeyID)
	}
	if s.SecretKey != "" && s.SecretKey != client.MaskedValue {
		m.SecretKey = types.StringValue(s.SecretKey)
	}
}

func (r *backupDestinationResource) verify(ctx context.Context, m *backupDestinationModel) error {
	if !m.VerifyOnApply.ValueBool() {
		return nil
	}
	res, err := r.client.TestS3Connection(ctx, m.request())
	if err != nil {
		return err
	}
	if !res.OK {
		return fmt.Errorf("the Temps server could not reach bucket %q: %s", m.BucketName.ValueString(), res.Message)
	}
	return nil
}

func (r *backupDestinationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan backupDestinationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.verify(ctx, &plan); err != nil {
		addAPIError(&resp.Diagnostics, "Backup destination "+plan.Name.String()+" failed verification", err, backupDestinationPermissions)
		return
	}
	body := plan.request()
	body.IsDefault = boolPtr(plan.IsDefault)
	createS3SourceMu.Lock()
	s, err := r.client.CreateS3Source(ctx, body)
	createS3SourceMu.Unlock()
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to create Temps backup destination "+plan.Name.String(), err, backupDestinationPermissions)
		return
	}
	plan.fromAPI(s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *backupDestinationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state backupDestinationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_backup_destination ID", err.Error())
		return
	}
	s, err := r.client.GetS3Source(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to read Temps backup destination "+formatID(id), err, backupDestinationPermissions)
		return
	}
	state.fromAPI(s)
	if state.VerifyOnApply.IsNull() { // import
		state.VerifyOnApply = types.BoolValue(false)
	}
	if state.ForceDestroy.IsNull() {
		state.ForceDestroy = types.BoolValue(false)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *backupDestinationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state backupDestinationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_backup_destination ID", err.Error())
		return
	}
	if err := r.verify(ctx, &plan); err != nil {
		addAPIError(&resp.Diagnostics, "Backup destination "+plan.Name.String()+" failed verification", err, backupDestinationPermissions)
		return
	}
	s, err := r.client.UpdateS3Source(ctx, id, plan.request())
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to update Temps backup destination "+formatID(id), err, backupDestinationPermissions)
		return
	}
	if plan.IsDefault.ValueBool() && !s.IsDefault {
		if s, err = r.client.SetDefaultS3Source(ctx, id); err != nil {
			addAPIError(&resp.Diagnostics, "Unable to make Temps backup destination "+formatID(id)+" the default", err, backupDestinationPermissions)
			return
		}
	}
	plan.fromAPI(s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *backupDestinationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state backupDestinationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_backup_destination ID", err.Error())
		return
	}
	if state.ForceDestroy.ValueBool() {
		// Bounded: each round deletes up to a page of backups.
		for round := 0; ; round++ {
			if round == 50 {
				resp.Diagnostics.AddError("Backups remain in Temps backup destination "+formatID(id),
					"force_destroy deleted 50 pages of backups and more remain; run destroy again.")
				return
			}
			backups, err := r.client.ListS3SourceBackups(ctx, id)
			if client.IsNotFound(err) {
				return
			}
			if err != nil {
				addAPIError(&resp.Diagnostics, "Unable to list backups in Temps backup destination "+formatID(id), err, backupDestinationPermissions)
				return
			}
			if len(backups) == 0 {
				break
			}
			for _, b := range backups {
				if err := r.deleteBackup(ctx, b.BackupID); err != nil {
					addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to delete backup %s (state %s) from destination %d", b.BackupID, b.State, id), err, backupDestinationPermissions)
					return
				}
			}
		}
	}
	if err := r.deleteSource(ctx, id); err != nil {
		addAPIError(&resp.Diagnostics, "Unable to delete Temps backup destination "+formatID(id), err, backupDestinationPermissions)
	}
}

// deleteBackup deletes one backup, retrying server errors a few times: the
// backup delete endpoint has failed transiently with a 500 while other
// objects were being destroyed in the same run.
func (r *backupDestinationResource) deleteBackup(ctx context.Context, backupID string) error {
	var err error
	for attempt := 1; attempt <= backupDeleteAttempts; attempt++ {
		err = r.client.DeleteBackup(ctx, backupID)
		if err == nil || client.IsNotFound(err) {
			return nil
		}
		var apiErr *client.APIError
		if !asAPIError(err, &apiErr) || apiErr.StatusCode < http.StatusInternalServerError || attempt == backupDeleteAttempts {
			break
		}
		t := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return errors.Join(err, ctx.Err())
		case <-t.C:
		}
	}
	return err
}

// defaultBlock explains a refused delete: whether the destination holds no
// backups and is the default (so backups are not the reason), and how many
// other destinations exist. Lookup errors count as "not the default": the
// caller then reports the original error.
func (r *backupDestinationResource) defaultBlock(ctx context.Context, id int64) (isDefault bool, others int) {
	if backups, err := r.client.ListS3SourceBackups(ctx, id); err != nil || len(backups) > 0 {
		return false, 0
	}
	sources, err := r.client.ListS3Sources(ctx)
	if err != nil {
		return false, 0
	}
	for _, s := range sources {
		if s.ID == id {
			isDefault = s.IsDefault
		} else {
			others++
		}
	}
	return isDefault, others
}

// deleteSource deletes the destination. Temps refuses to delete the default
// destination while others exist; when those are being destroyed in the same
// run, wait (bounded) for them to go before giving up.
func (r *backupDestinationResource) deleteSource(ctx context.Context, id int64) error {
	waitCtx, cancel := context.WithTimeout(ctx, defaultDeleteWait)
	defer cancel()
	retriedAlone := false
	for {
		err := r.client.DeleteS3Source(ctx, id)
		if err == nil || client.IsNotFound(err) {
			return nil
		}
		var apiErr *client.APIError
		if !asAPIError(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
			return err
		}
		isDefault, others := r.defaultBlock(ctx, id)
		if !isDefault || (others == 0 && retriedAlone) {
			return withHint{err, "If the destination still holds backups, set force_destroy = true (this deletes them) and apply before destroying."}
		}
		if others == 0 {
			// The others went away since the refused delete: retry now, once.
			retriedAlone = true
			continue
		}
		t := time.NewTimer(pollInterval)
		select {
		case <-waitCtx.Done():
			t.Stop()
			if ctx.Err() != nil {
				return errors.Join(err, ctx.Err())
			}
			return withHint{err, fmt.Sprintf("This is the server's default backup destination, and Temps only deletes the default once no other destination exists "+
				"(waited %s for them). Set is_default = true on another temps_backup_destination and apply, or destroy the other destinations first.", defaultDeleteWait)}
		case <-t.C:
		}
	}
}

func (r *backupDestinationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := parseID(req.ID); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
