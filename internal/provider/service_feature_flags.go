package provider

import (
	"context"
	"fmt"

	"github.com/Khan/genqlient/graphql"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Service feature flags (ActiveServiceFeatureFlag, e.g. SKIPPED_BUILDS) are
// toggled one at a time through serviceFeatureFlagAdd / serviceFeatureFlagRemove
// and listed on Service.featureFlags. Hand-written rather than generated so the
// hand-maintained generated.go stays untouched.

type serviceFeatureFlagsResponse struct {
	Service struct {
		FeatureFlags []string `json:"featureFlags"`
	} `json:"service"`
}

func getServiceFeatureFlags(ctx context.Context, client graphql.Client, serviceId string) ([]string, error) {
	req := &graphql.Request{
		OpName: "getServiceFeatureFlags",
		Query: `
query getServiceFeatureFlags ($id: String!) {
	service(id: $id) {
		featureFlags
	}
}
`,
		Variables: &struct {
			Id string `json:"id"`
		}{Id: serviceId},
	}

	var data serviceFeatureFlagsResponse
	resp := &graphql.Response{Data: &data}

	if err := client.MakeRequest(ctx, req, resp); err != nil {
		return nil, err
	}

	return data.Service.FeatureFlags, nil
}

func toggleServiceFeatureFlag(ctx context.Context, client graphql.Client, serviceId string, flag string, enable bool) error {
	mutation := "serviceFeatureFlagRemove"

	if enable {
		mutation = "serviceFeatureFlagAdd"
	}

	req := &graphql.Request{
		OpName: mutation,
		Query: fmt.Sprintf(`
mutation %s ($input: ServiceFeatureFlagToggleInput!) {
	%s(input: $input)
}
`, mutation, mutation),
		Variables: &struct {
			Input struct {
				Flag      string `json:"flag"`
				ServiceId string `json:"serviceId"`
			} `json:"input"`
		}{Input: struct {
			Flag      string `json:"flag"`
			ServiceId string `json:"serviceId"`
		}{Flag: flag, ServiceId: serviceId}},
	}

	var data map[string]bool
	resp := &graphql.Response{Data: &data}

	return client.MakeRequest(ctx, req, resp)
}

// syncServiceFeatureFlags makes the service's flags equal to `desired`.
func syncServiceFeatureFlags(ctx context.Context, client graphql.Client, serviceId string, desired types.Set) error {
	if desired.IsNull() || desired.IsUnknown() {
		return nil
	}

	want := map[string]bool{}

	for _, element := range desired.Elements() {
		if value, ok := element.(types.String); ok {
			want[value.ValueString()] = true
		}
	}

	current, err := getServiceFeatureFlags(ctx, client, serviceId)

	if err != nil {
		return err
	}

	have := map[string]bool{}

	for _, flag := range current {
		have[flag] = true

		if !want[flag] {
			if err := toggleServiceFeatureFlag(ctx, client, serviceId, flag, false); err != nil {
				return fmt.Errorf("removing %s: %w", flag, err)
			}

			tflog.Trace(ctx, "removed a service feature flag", map[string]interface{}{"flag": flag})
		}
	}

	for flag := range want {
		if !have[flag] {
			if err := toggleServiceFeatureFlag(ctx, client, serviceId, flag, true); err != nil {
				return fmt.Errorf("adding %s: %w", flag, err)
			}

			tflog.Trace(ctx, "added a service feature flag", map[string]interface{}{"flag": flag})
		}
	}

	return nil
}

// getAndBuildServiceFeatureFlags reflects the live flags into state. An unset
// attribute means "unmanaged": flags toggled in the dashboard are left alone
// and never surface as drift.
func getAndBuildServiceFeatureFlags(ctx context.Context, client graphql.Client, serviceId string, data *ServiceResourceModel) error {
	if data.FeatureFlags.IsNull() {
		return nil
	}

	current, err := getServiceFeatureFlags(ctx, client, serviceId)

	if err != nil {
		return err
	}

	elements := make([]attr.Value, 0, len(current))

	for _, flag := range current {
		elements = append(elements, types.StringValue(flag))
	}

	data.FeatureFlags = types.SetValueMust(types.StringType, elements)

	return nil
}
