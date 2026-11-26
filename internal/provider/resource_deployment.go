// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var deploymentPermissions = []string{"deployments:read", "deployments:create"}

const defaultDeploymentTimeout = 30 * time.Minute

// cancelTimeout bounds the best-effort cancel of a deployment Terraform
// stopped waiting for; it runs after the operation's own context ended.
const cancelTimeout = 30 * time.Second

var (
	imageRefRE   = regexp.MustCompile(`^\S+$`)
	healthPathRE = regexp.MustCompile(`^/\S*$`)
)

var _ resource.Resource = &deploymentResource{}

type deploymentResource struct{ resourceBase }

func newDeploymentResource() resource.Resource { return &deploymentResource{} }

type deploymentModel struct {
	ID                types.String   `tfsdk:"id"`
	ProjectID         types.Int64    `tfsdk:"project_id"`
	EnvironmentID     types.Int64    `tfsdk:"environment_id"`
	Image             types.String   `tfsdk:"image"`
	Command           types.List     `tfsdk:"command"`
	HealthCheckPath   types.String   `tfsdk:"health_check_path"`
	Triggers          types.Map      `tfsdk:"triggers"`
	WaitForCompletion types.Bool     `tfsdk:"wait_for_completion"`
	Status            types.String   `tfsdk:"status"`
	URL               types.String   `tfsdk:"url"`
	IsCurrent         types.Bool     `tfsdk:"is_current"`
	CreatedAt         types.String   `tfsdk:"created_at"`
	FinishedAt        types.String   `tfsdk:"finished_at"`
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
}

func (r *deploymentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deployment"
}

func (r *deploymentResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Deploys a pre-built Docker image from a registry to an environment of a project " +
			"(`POST /projects/{id}/environments/{env}/deploy/image`), and by default waits until Temps reports the " +
			"deployment completed.\n\n" +
			"Each change of `image`, `command`, `health_check_path` or `triggers` starts a new deployment, which " +
			"replaces the previous one in the environment once it passes its health check. A deployment that fails is reported " +
			"with Temps' classified cause and kept in state as tainted, so the next apply deploys again.\n\n" +
			"Destroying this resource only removes it from state: the deployment keeps serving until another " +
			"deployment replaces it, so a replacement never stops the environment first. Remove the project or " +
			"environment to stop it. A deployment made outside Terraform (from the console, CI or a rollback) " +
			"shows as `is_current = false` and is not undone.\n\n" +
			"Cannot be imported.\n\n" +
			"Required API key permissions: " + permList(deploymentPermissions) + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Numeric deployment ID.", PlanModifiers: keep},
			"project_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of the project.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"environment_id": schema.Int64Attribute{Required: true, MarkdownDescription: "ID of an environment of `project_id`.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"image": schema.StringAttribute{Required: true, PlanModifiers: replace,
				MarkdownDescription: "Image reference, for example `ghcr.io/example-org/app:1.4.2`. Pin a tag or a digest " +
					"(`...@sha256:...`): a moving tag such as `latest` is only pulled again when something here changes " +
					"(use `triggers`).",
				Validators: []validator.String{stringvalidator.RegexMatches(imageRefRE, "must not be empty or contain spaces")}},
			"command": schema.ListAttribute{Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Container command, one argv element per entry, with no shell parsing. Omit it to use " +
					"the project's saved command, or the image's.",
				Validators:    []validator.List{listvalidator.SizeAtLeast(1)},
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()}},
			"health_check_path": schema.StringAttribute{Optional: true, PlanModifiers: replace,
				MarkdownDescription: "HTTP path Temps probes once the container starts, and the environment's uptime " +
					"monitor checks, for example `/healthz`. Omit it to use the project's saved path, or `/`.",
				Validators: []validator.String{stringvalidator.RegexMatches(healthPathRE, "must start with '/' and contain no spaces")}},
			"triggers": schema.MapAttribute{Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Arbitrary values that start a new deployment when they change, for example a build " +
					"number or the digest a moving tag points at.",
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()}},
			"wait_for_completion": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				MarkdownDescription: "Wait until the deployment completes, failing the apply if it fails. `false` returns " +
					"once Temps accepted it. Changing it does not deploy again. Defaults to `true`."},
			"status": schema.StringAttribute{Computed: true, PlanModifiers: keep,
				MarkdownDescription: "Deployment status: `pending`, `running`, `built`, `completed`, `failed`, `cancelled`, ..."},
			"url": schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "URL the deployment is served at."},
			"is_current": schema.BoolAttribute{Computed: true,
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Whether this is the environment's current deployment. `false` once another deployment replaced it."},
			"created_at":  schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "When the deployment was created (RFC 3339)."},
			"finished_at": schema.StringAttribute{Computed: true, MarkdownDescription: "When the deployment finished (RFC 3339), if it has."},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true}),
		},
	}
}

func (m *deploymentModel) fromAPI(d *client.Deployment) {
	m.ID = types.StringValue(formatID(d.ID))
	m.Status = types.StringValue(d.Status)
	m.URL = types.StringValue(d.URL)
	m.IsCurrent = types.BoolValue(d.IsCurrent)
	created := string(d.CreatedAt)
	m.CreatedAt = timeValue(m.CreatedAt, &created)
	m.FinishedAt = timeValue(m.FinishedAt, (*string)(d.FinishedAt))
}

func deploymentSucceeded(status string) bool { return status == "completed" || status == "deployed" }

func deploymentFailed(status string) bool {
	switch status {
	case "failed", "cancelled", "stopped", "paused":
		return true
	}
	return false
}

// failureDetail explains why a deployment did not complete, from Temps'
// classification when it has one.
func failureDetail(d *client.Deployment) string {
	if f := d.Failure; f != nil {
		out := fmt.Sprintf("%s (stage %s, code %s", f.Title, f.Stage, f.Code)
		if f.FailedJob != nil && *f.FailedJob != "" {
			out += ", job " + *f.FailedJob
		}
		out += ")."
		if f.Remediation != "" {
			out += " " + f.Remediation
		}
		return out
	}
	if d.CancelledReason != nil && *d.CancelledReason != "" {
		return *d.CancelledReason
	}
	return "Temps recorded no reason; see the deployment's logs in the console."
}

func (r *deploymentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan deploymentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, defaultDeploymentTimeout)
	resp.Diagnostics.Append(d...)
	command := stringsFromList(ctx, plan.Command, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pid, env, image := plan.ProjectID.ValueInt64(), plan.EnvironmentID.ValueInt64(), plan.Image.ValueString()
	what := fmt.Sprintf("deployment of %s to environment %d of project %d", image, env, pid)
	started, err := r.client.DeployImage(waitCtx, pid, env, client.DeployImageRequest{
		ImageRef: image, HealthCheckPath: strPtr(plan.HealthCheckPath), Command: command,
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Unable to start the "+what, err, deploymentPermissions)
		return
	}
	what = fmt.Sprintf("deployment %d (%s to environment %d of project %d)", started.ID, image, env, pid)

	// Track the deployment right away: from here on a failure leaves it in
	// state (tainted), so the next apply deploys again.
	plan.ID = types.StringValue(formatID(started.ID))
	current := &client.Deployment{ID: started.ID, ProjectID: pid, EnvironmentID: env, Status: started.State, CreatedAt: started.CreatedAt}
	if plan.WaitForCompletion.ValueBool() {
		err = waitFor(waitCtx, what+" to complete", pollInterval, func() (bool, string, error) {
			got, err := r.client.GetDeployment(waitCtx, pid, started.ID)
			if err != nil {
				return false, "", err
			}
			current = got
			return deploymentSucceeded(got.Status) || deploymentFailed(got.Status), got.Status, nil
		})
		if err == nil && deploymentFailed(current.Status) {
			err = errDeploymentFailed
		}
	} else if got, gerr := r.client.GetDeployment(waitCtx, pid, started.ID); gerr == nil {
		current = got
	}
	// Terraform no longer follows the deployment (timeout, interrupt or a
	// failed poll): cancel it rather than leave it to switch the environment
	// after the apply reported a failure. One that already finished is kept
	// for what it is.
	var abandoned string
	if err != nil && !errors.Is(err, errDeploymentFailed) {
		var finished *client.Deployment
		finished, abandoned = r.settleAbandoned(pid, started.ID)
		if finished != nil {
			current = finished
			switch {
			case deploymentSucceeded(finished.Status):
				resp.Diagnostics.AddWarning("Temps "+what+" completed while Terraform stopped following it",
					"Terraform stopped following it ("+err.Error()+"), but it had already completed, so it is kept.")
				err = nil
			case deploymentFailed(finished.Status):
				err = errDeploymentFailed
			default:
				abandoned = fmt.Sprintf("Temps refused to cancel it and reports it %s, so it may still switch the environment. "+
					"Cancel deployment %d in the console if it should not.", finished.Status, started.ID)
			}
		}
	}
	plan.fromAPI(current)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

	switch {
	case err == nil:
	case errors.Is(err, errDeploymentFailed):
		resp.Diagnostics.AddError("Temps "+what+" "+current.Status,
			failureDetail(current)+"\n\nThe deployment stays in state as tainted: fix the cause and apply again to deploy anew.")
	case waitCtx.Err() != nil:
		resp.Diagnostics.AddError("Temps "+what+" did not complete", err.Error()+"\n\n"+abandoned)
	default:
		addAPIError(&resp.Diagnostics, "Unable to follow the "+what, fmt.Errorf("%w\n\n%s", err, abandoned), deploymentPermissions)
	}
}

// errDeploymentFailed marks a wait that ended because the deployment failed.
var errDeploymentFailed = errors.New("deployment failed")

// settleAbandoned cancels a deployment Terraform stopped following and says
// what happened. When Temps refuses because the deployment already finished,
// it returns the deployment as Temps has it now.
func (r *deploymentResource) settleAbandoned(projectID, deploymentID int64) (*client.Deployment, string) {
	ctx, cancel := context.WithTimeout(context.Background(), cancelTimeout)
	defer cancel()
	err := r.client.CancelDeployment(ctx, projectID, deploymentID)
	if err == nil {
		return nil, fmt.Sprintf("Terraform cancelled deployment %d. It stays in state as tainted, so the next apply deploys again; "+
			"raise timeouts.create if it is just slow.", deploymentID)
	}
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		return nil, fmt.Sprintf("Terraform could not cancel it, so it may still switch the environment: %s. "+
			"Cancel deployment %d in the console if it should not.", err, deploymentID)
	}
	got, gerr := r.client.GetDeployment(ctx, projectID, deploymentID)
	if gerr != nil {
		return nil, fmt.Sprintf("Temps refused to cancel it (%s), and reading it back failed: %s. "+
			"Check deployment %d in the console.", strings.TrimSpace(apiErr.Detail), gerr, deploymentID)
	}
	return got, ""
}

func (r *deploymentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state deploymentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := parseID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid temps_deployment ID", err.Error())
		return
	}
	pid := state.ProjectID.ValueInt64()
	d, err := r.client.GetDeployment(ctx, pid, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, fmt.Sprintf("Unable to read deployment %d of project %d", id, pid), err, deploymentPermissions)
		return
	}
	state.fromAPI(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update only runs for wait_for_completion or timeouts, which send nothing;
// every other argument replaces the deployment.
func (r *deploymentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state deploymentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID, plan.Status, plan.URL, plan.IsCurrent = state.ID, state.Status, state.URL, state.IsCurrent
	plan.CreatedAt, plan.FinishedAt = state.CreatedAt, state.FinishedAt
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete leaves the deployment serving: a deployment is history, and
// stopping it here would take the environment down before its replacement
// is up.
func (r *deploymentResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}
