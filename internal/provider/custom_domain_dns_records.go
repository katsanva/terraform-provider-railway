package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var customDomainDNSRecordAttrTypes = map[string]attr.Type{
	"host_label":     types.StringType,
	"fqdn":           types.StringType,
	"zone":           types.StringType,
	"record_type":    types.StringType,
	"purpose":        types.StringType,
	"required_value": types.StringType,
}

// buildCustomDomainDNSRecords exposes every record Railway asks for, not just
// the first: a wildcard custom domain needs two CNAMEs.
func buildCustomDomainDNSRecords(records []CustomDomainStatusDnsRecordsDNSRecords) types.List {
	elements := make([]attr.Value, 0, len(records))

	for _, record := range records {
		elements = append(elements, types.ObjectValueMust(customDomainDNSRecordAttrTypes, map[string]attr.Value{
			"host_label":     types.StringValue(record.Hostlabel),
			"fqdn":           types.StringValue(record.Fqdn),
			"zone":           types.StringValue(record.Zone),
			"record_type":    types.StringValue(record.RecordType),
			"purpose":        types.StringValue(record.Purpose),
			"required_value": types.StringValue(record.RequiredValue),
		}))
	}

	return types.ListValueMust(types.ObjectType{AttrTypes: customDomainDNSRecordAttrTypes}, elements)
}
