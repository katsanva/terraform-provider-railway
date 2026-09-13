package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Khan/genqlient/graphql"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

const (
	templateWorkflowPollInterval = 2 * time.Second
	templateWorkflowTimeout      = 5 * time.Minute
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ resource.Resource = &TemplateDeploymentResource{}
var _ resource.ResourceWithImportState = &TemplateDeploymentResource{}

func NewTemplateDeploymentResource() resource.Resource {
	return &TemplateDeploymentResource{}
}

type TemplateDeploymentResource struct {
	client *graphql.Client
}

type TemplateDeploymentResourceModel struct {
	Id            types.String `tfsdk:"id"`
	Template      types.String `tfsdk:"template"`
	ProjectId     types.String `tfsdk:"project_id"`
	EnvironmentId types.String `tfsdk:"environment_id"`
	Variables     types.Map    `tfsdk:"variables"`
	TemplateId    types.String `tfsdk:"template_id"`
	ServiceIds    types.Map    `tfsdk:"service_ids"`
}

func (r *TemplateDeploymentResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_template_deployment"
}

func (r *TemplateDeploymentResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Deployment of a Railway template into a project environment.\n\n" +
			"Deploying a template is a one-shot operation: any change to this resource replaces it, which deletes the services it created (including their volumes) and deploys the template again. " +
			"The created services are tracked in `service_ids`; destroying the resource deletes them. An existing deployment can be imported; its `variables` are not recoverable, so setting them on an imported resource just records them without a redeploy.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Identifier of the template deployment.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"template": schema.StringAttribute{
				MarkdownDescription: "Code of the template to deploy, e.g. `postgres` or `redis`.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.UTF8LengthAtLeast(1),
				},
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Identifier of the project to deploy the template into.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.RegexMatches(uuidRegex(), "must be an id"),
				},
			},
			"environment_id": schema.StringAttribute{
				MarkdownDescription: "Identifier of the environment to deploy the template into.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.RegexMatches(uuidRegex(), "must be an id"),
				},
			},
			"variables": schema.MapAttribute{
				MarkdownDescription: "Values for the template's variables. Keys are `KEY` (applies to every service defining it) or `<service name>.KEY`. Variables without a value fall back to the template default; required ones without a default cause an error.",
				ElementType:         types.StringType,
				Optional:            true,
				Sensitive:           true,
				PlanModifiers: []planmodifier.Map{
					// Variables of an imported deployment are unknown; adopting them
					// from config must not redeploy.
					mapplanmodifier.RequiresReplaceIf(
						func(ctx context.Context, req planmodifier.MapRequest, resp *mapplanmodifier.RequiresReplaceIfFuncResponse) {
							resp.RequiresReplace = !req.StateValue.IsNull()
						},
						"Replaced when changed after creation.",
						"Replaced when changed after creation.",
					),
				},
			},
			"template_id": schema.StringAttribute{
				MarkdownDescription: "Identifier of the deployed template.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"service_ids": schema.MapAttribute{
				MarkdownDescription: "Identifiers of the services created by the deployment, keyed by service name.",
				ElementType:         types.StringType,
				Computed:            true,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *TemplateDeploymentResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	// Prevent panic if the provider has not been configured.
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*graphql.Client)

	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *graphql.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)

		return
	}

	r.client = client
}

func (r *TemplateDeploymentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data *TemplateDeploymentResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	variables := map[string]string{}

	if !data.Variables.IsNull() {
		resp.Diagnostics.Append(data.Variables.ElementsAs(ctx, &variables, false)...)

		if resp.Diagnostics.HasError() {
			return
		}
	}

	templateResponse, err := getTemplate(ctx, *r.client, data.Template.ValueString())

	if err != nil {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to read template, got error: %s", err))
		return
	}

	template := templateResponse.Template
	config := template.SerializedConfig

	if config == nil {
		resp.Diagnostics.AddError("Unsupported Template", fmt.Sprintf("Template %q has no serialized config and cannot be deployed.", data.Template.ValueString()))
		return
	}

	if err := applyTemplateVariables(config, variables); err != nil {
		resp.Diagnostics.AddError("Invalid Template Variables", err.Error())
		return
	}

	projectId := data.ProjectId.ValueString()
	environmentId := data.EnvironmentId.ValueString()

	before, err := getProjectServices(ctx, *r.client, projectId)

	if err != nil {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to read project services, got error: %s", err))
		return
	}

	existing := map[string]bool{}

	for _, edge := range before.Project.Services.Edges {
		existing[edge.Node.Id] = true
	}

	deployResponse, err := deployTemplate(ctx, *r.client, TemplateDeployV2Input{
		TemplateId:       template.Id,
		SerializedConfig: config,
		ProjectId:        &projectId,
		EnvironmentId:    &environmentId,
	})

	if err != nil {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to deploy template, got error: %s", err))
		return
	}

	tflog.Trace(ctx, "deployed a template")

	workflowId := deployResponse.TemplateDeployV2.WorkflowId
	workflowErr := r.waitForWorkflow(ctx, workflowId)

	// Services may exist even when the workflow failed; track them so that
	// the tainted resource can be destroyed cleanly.
	after, err := getProjectServices(ctx, *r.client, projectId)

	if err != nil {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to read project services after deploy, got error: %s", err))
		return
	}

	// Only services this template defines: a concurrent deployment of another
	// template into the same project would otherwise be attributed to this one
	// (and deleted with it).
	templateServices, _ := config["services"].(map[string]interface{})
	serviceIds := map[string]attr.Value{}

	for _, edge := range after.Project.Services.Edges {
		if existing[edge.Node.Id] {
			continue
		}

		if _, ok := templateServices[edge.Node.TemplateServiceId]; ok {
			serviceIds[edge.Node.Name] = types.StringValue(edge.Node.Id)
		}
	}

	if workflowErr == nil && len(serviceIds) == 0 {
		workflowErr = fmt.Errorf("workflow %s completed but created no service from template %q", workflowId, data.Template.ValueString())
	}

	id := workflowId

	if id == "" {
		id = fmt.Sprintf("%s:%s", environmentId, template.Id)
	}

	data.Id = types.StringValue(id)
	data.TemplateId = types.StringValue(template.Id)
	data.ServiceIds = types.MapValueMust(types.StringType, serviceIds)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)

	if workflowErr != nil {
		resp.Diagnostics.AddError("Template Deployment Failed", workflowErr.Error())
	}
}

func (r *TemplateDeploymentResource) waitForWorkflow(ctx context.Context, workflowId string) error {
	if workflowId == "" {
		return nil
	}

	deadline := time.Now().Add(templateWorkflowTimeout)

	for {
		response, err := getWorkflowStatus(ctx, *r.client, workflowId)

		if err != nil {
			return fmt.Errorf("unable to read workflow status: %w", err)
		}

		switch response.WorkflowStatus.Status {
		case WorkflowStatusComplete:
			return nil
		case WorkflowStatusError:
			return fmt.Errorf("workflow failed: %s", response.WorkflowStatus.Error)
		case WorkflowStatusNotfound:
			return fmt.Errorf("workflow %s not found", workflowId)
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for workflow %s", templateWorkflowTimeout, workflowId)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(templateWorkflowPollInterval):
		}
	}
}

func (r *TemplateDeploymentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data *TemplateDeploymentResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	response, err := getProjectServices(ctx, *r.client, data.ProjectId.ValueString())

	if err != nil {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to read project services, got error: %s", err))
		return
	}

	tracked := map[string]string{}
	resp.Diagnostics.Append(data.ServiceIds.ElementsAs(ctx, &tracked, false)...)

	if resp.Diagnostics.HasError() {
		return
	}

	live := map[string]string{}

	for _, edge := range response.Project.Services.Edges {
		live[edge.Node.Id] = edge.Node.Name
	}

	serviceIds := map[string]attr.Value{}

	for _, id := range tracked {
		if name, ok := live[id]; ok {
			serviceIds[name] = types.StringValue(id)
		}
	}

	if len(serviceIds) == 0 {
		resp.State.RemoveResource(ctx)
		return
	}

	data.ServiceIds = types.MapValueMust(types.StringType, serviceIds)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *TemplateDeploymentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data *TemplateDeploymentResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *TemplateDeploymentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data *TemplateDeploymentResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	serviceIds := map[string]string{}
	resp.Diagnostics.Append(data.ServiceIds.ElementsAs(ctx, &serviceIds, false)...)

	if resp.Diagnostics.HasError() {
		return
	}

	for name, id := range serviceIds {
		if _, err := deleteService(ctx, *r.client, id); err != nil {
			resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to delete service %s (%s) created by template, got error: %s", name, id, err))
		}
	}

	if !resp.Diagnostics.HasError() {
		tflog.Trace(ctx, "deleted a template deployment")
	}
}

func (r *TemplateDeploymentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, ":")

	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError(
			"Unexpected Import Identifier",
			fmt.Sprintf("Expected import identifier with format: project_id:environment_id:template. Got: %q", req.ID),
		)

		return
	}

	projectId, environmentId, code := parts[0], parts[1], parts[2]

	templateResponse, err := getTemplate(ctx, *r.client, code)

	if err != nil {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to read template, got error: %s", err))
		return
	}

	servicesResponse, err := getProjectServices(ctx, *r.client, projectId)

	if err != nil {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to read project services, got error: %s", err))
		return
	}

	templateServices, _ := templateResponse.Template.SerializedConfig["services"].(map[string]interface{})
	serviceIds := map[string]attr.Value{}

	for _, edge := range servicesResponse.Project.Services.Edges {
		if _, ok := templateServices[edge.Node.TemplateServiceId]; ok {
			serviceIds[edge.Node.Name] = types.StringValue(edge.Node.Id)
		}
	}

	if len(serviceIds) == 0 {
		resp.Diagnostics.AddError("Template Deployment Not Found", fmt.Sprintf("No services in project %s were deployed from template %q.", projectId, code))
		return
	}

	data := TemplateDeploymentResourceModel{
		Id:            types.StringValue(fmt.Sprintf("%s:%s", environmentId, templateResponse.Template.Id)),
		Template:      types.StringValue(code),
		ProjectId:     types.StringValue(projectId),
		EnvironmentId: types.StringValue(environmentId),
		Variables:     types.MapNull(types.StringType),
		TemplateId:    types.StringValue(templateResponse.Template.Id),
		ServiceIds:    types.MapValueMust(types.StringType, serviceIds),
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// applyTemplateVariables fills in the `value` of every service variable in a
// template's serialized config. Precedence: `<service name>.KEY`, then `KEY`,
// then the template's default. Required variables left without a value are
// reported together in the returned error.
func applyTemplateVariables(config map[string]interface{}, values map[string]string) error {
	services, _ := config["services"].(map[string]interface{})
	missing := []string{}

	for _, s := range services {
		service, _ := s.(map[string]interface{})
		serviceName, _ := service["name"].(string)
		variables, _ := service["variables"].(map[string]interface{})

		for key, v := range variables {
			variable, ok := v.(map[string]interface{})

			if !ok {
				continue
			}

			defaultValue, _ := variable["defaultValue"].(string)
			isOptional, _ := variable["isOptional"].(bool)

			value, ok := values[serviceName+"."+key]

			if !ok {
				value, ok = values[key]
			}

			if !ok {
				value = defaultValue
				ok = value != ""
			}

			if !ok {
				if !isOptional {
					missing = append(missing, serviceName+"."+key)
				}

				continue
			}

			variable["value"] = value
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("required template variables have no value: %s", strings.Join(missing, ", "))
	}

	return nil
}
