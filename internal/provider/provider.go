// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package provider implements the Temps Terraform provider.
package provider

import (
	"context"
	"os"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

const (
	envEndpoint = "TEMPS_ENDPOINT"
	envAPIKey   = "TEMPS_API_KEY"
)

var _ provider.Provider = &tempsProvider{}

type tempsProvider struct {
	version string
}

type providerModel struct {
	Endpoint       types.String `tfsdk:"endpoint"`
	APIKey         types.String `tfsdk:"api_key"`
	RequestTimeout types.Int64  `tfsdk:"request_timeout_seconds"`
	MaxRetries     types.Int64  `tfsdk:"max_retries"`
}

// New returns a provider factory.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &tempsProvider{version: version} }
}

func (p *tempsProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "temps"
	resp.Version = p.version
}

func (p *tempsProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage a [Temps](https://temps.sh) installation as code: projects, environments, " +
			"environment variables, managed services, backups, monitors and status pages.\n\n" +
			"Authenticate with a Temps API key (`tk_...`). Each resource's documentation lists the API key " +
			"permissions it needs; a key with the `admin` role has all of them.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Base URL of the Temps server, for example `https://temps.example.com`. " +
					"`/api` is appended when missing. Can also be set with the `" + envEndpoint + "` environment variable.",
			},
			"api_key": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "Temps API key (`tk_...`), sent as a bearer token and never logged. " +
					"Can also be set with the `" + envAPIKey + "` environment variable.",
			},
			"request_timeout_seconds": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Timeout for a single API request, in seconds. Defaults to `60`.",
				Validators:          []validator.Int64{int64validator.Between(1, 3600)},
			},
			"max_retries": schema.Int64Attribute{
				Optional: true,
				MarkdownDescription: "How many times a request is retried after a refused connection, HTTP 429, " +
					"or (for idempotent requests) HTTP 502-504 and dropped connections, with bounded exponential " +
					"backoff. Defaults to `4`; `0` uses the default, set `-1` to disable retries.",
				Validators: []validator.Int64{int64validator.Between(-1, 10)},
			},
		},
	}
}

func (p *tempsProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.Endpoint.IsUnknown() || cfg.APIKey.IsUnknown() {
		resp.Diagnostics.AddError("Temps provider configuration is not known yet",
			"endpoint and api_key depend on values known only after apply; the provider needs them during plan.")
		return
	}
	endpoint := os.Getenv(envEndpoint)
	if !cfg.Endpoint.IsNull() {
		endpoint = cfg.Endpoint.ValueString()
	}
	apiKey := os.Getenv(envAPIKey)
	if !cfg.APIKey.IsNull() {
		apiKey = cfg.APIKey.ValueString()
	}
	if endpoint == "" {
		resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Missing Temps endpoint",
			"Set the provider's endpoint argument or the "+envEndpoint+" environment variable.")
	}
	if apiKey == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Missing Temps API key",
			"Set the provider's api_key argument or the "+envAPIKey+" environment variable. "+
				"Create a key in the Temps console under Settings > API keys.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	timeout := client.DefaultTimeout
	if !cfg.RequestTimeout.IsNull() {
		timeout = time.Duration(cfg.RequestTimeout.ValueInt64()) * time.Second
	}
	c, err := client.New(client.Config{
		Endpoint:   endpoint,
		APIKey:     apiKey,
		UserAgent:  "terraform-provider-temps/" + p.version + " (+https://registry.terraform.io/providers/gotempsh/temps)",
		Timeout:    timeout,
		MaxRetries: int(cfg.MaxRetries.ValueInt64()),
	})
	if err != nil {
		resp.Diagnostics.AddError("Invalid Temps provider configuration", err.Error())
		return
	}
	tflog.Debug(ctx, "configured Temps client", map[string]any{"endpoint": c.BaseURL()})
	resp.DataSourceData = c
	resp.ResourceData = c
}

func (p *tempsProvider) Resources(_ context.Context) []func() resource.Resource {
	rs := []func() resource.Resource{
		newProjectResource,
		newEnvironmentResource,
		newEnvironmentVariableResource,
		newServiceResource,
		newServiceLinkResource,
		newBackupDestinationResource,
		newBackupScheduleResource,
		newMonitorResource,
		newStatusPageResource,
		newStatusPageDomainResource,
		newCustomDomainResource,
		newWebhookResource,
		newNotificationProviderResource,
		newFeatureFlagResource,
		newFeatureFlagEnvironmentResource,
		newIPAccessPolicyResource,
		newCronMonitorResource,
		newDeploymentTokenResource,
		newTeamResource,
		newProjectAccessResource,
		newProjectGroupResource,
		newProjectGroupMembershipResource,
		newProjectGroupVariableResource,
		newProjectSecretResource,
		newNotificationRouteResource,
		newErrorAlertRuleResource,
		newEdgeRuleResource,
		newAPIKeyResource,
		newInboundFilterResource,
	}
	for _, s := range genericSpecs {
		rs = append(rs, genericFactory(s))
	}
	return rs
}

// genericSpecs are the resources the spec-driven engine (generic.go) serves.
var genericSpecs = []*gspec{
	userSpec,
	userRoleSpec,
	oidcProviderSpec,
	oidcRoleMappingSpec,
	gitProviderSpec,
	settingsSpec,
	globalSecretSpec,
	globalIPRuleSpec,
	dnsProviderSpec,
	dnsProviderDomainSpec,
	domainSpec,
	emailProviderSpec,
	emailDomainSpec,
	emailDomainProjectSpec,
	environmentDomainSpec,
	environmentIngressSpec,
	runtimeHealthSpec,
	internalAccessSpec,
	shieldEntrySpec,
	shieldTokenSpec,
	shieldSettingsSpec,
	httpCheckSpec,
	aiProviderSpec,
	aiProviderModelSpec,
	aiInstanceLimitsSpec,
	aiProjectLimitsSpec,
	projectAgentSpec,
	mcpServerSpec,
	skillSpec,
	accessServiceSpec,
	accessVaultSpec,
	accessBundleSpec,
	ingressConnectorSpec,
	lbRouteSpec,
	networkPolicyRuleSpec,
	deliveryProfileSpec,
	projectDeliverySettingsSpec,
	clusterNetworkSpec,
	wireguardMeshSpec,
	nodeBuildLimitsSpec,
	nodeResourcesSpec,
	workloadVolumeSpec,
	workloadVolumeAttachmentSpec,
	sandboxPlacementSpec,
	kvSpec,
	kvSettingsSpec,
	blobSpec,
	blobSettingsSpec,
	previewGatewaySpec,
	notificationPreferencesSpec,
	statusPageMaintenanceSpec,
	statusPageWebhookSpec,
	funnelSpec,
	surveySpec,
	errorOwnershipRulesSpec,
	errorSavedSearchSpec,
	errorAttachmentQuotaSpec,
	projectMcpServerSpec,
	projectSkillSpec,
	projectAutofixSettingsSpec,
	projectContainerSecuritySpec,
	projectComposeSecuritySpec,
	projectIdenticalBuildsSpec,
	projectDSNSpec,
	environmentWorkflowSettingsSpec,
	projectGroupRouteSpec,
	cloudflareAccessProviderSpec,
	nodePublicIngressSpec,
	ingressHostnameSpec,
	accessVaultLinkSpec,
	telemetrySettingsSpec,
	adminGateSettingsSpec,
	aiModelCatalogSettingsSpec,
	aiCodingAgentSettingsSpec,
	slackNotificationProviderSpec,
	emailNotificationProviderSpec,
	telegramNotificationProviderSpec,
	cloudflareNotificationProviderSpec,
	teamMemberSpec,
	nodeAlertRuleSpec,
	serviceAlertRuleSpec,
	otelAlertRuleSpec,
	otelDashboardSpec,
	analyticsGoalSpec,
	analyticsTrafficFilterSpec,
	projectAnalyticsSettingsSpec,
}

func (p *tempsProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newProjectDataSource,
		newEnvironmentDataSource,
		newServiceDataSource,
		newServiceTypesDataSource,
		newServiceConnectionDataSource,
		newWebhookEventTypesDataSource,
		newProjectGroupDataSource,
	}
}
