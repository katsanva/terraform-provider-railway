package provider

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func templateConfigFixture() map[string]interface{} {
	return map[string]interface{}{
		"services": map[string]interface{}{
			"svc-1": map[string]interface{}{
				"name": "Postgres",
				"variables": map[string]interface{}{
					"POSTGRES_DB":       map[string]interface{}{"defaultValue": "railway", "isOptional": false},
					"POSTGRES_PASSWORD": map[string]interface{}{"defaultValue": "", "isOptional": false},
					"PGOPTIONAL":        map[string]interface{}{"isOptional": true},
					"LEGACY":            nil,
				},
			},
			"svc-2": map[string]interface{}{
				"name": "Worker",
				"variables": map[string]interface{}{
					"POSTGRES_DB": map[string]interface{}{"defaultValue": "", "isOptional": false},
				},
			},
		},
	}
}

func variableValue(config map[string]interface{}, service string, key string) (string, bool) {
	variables := config["services"].(map[string]interface{})[service].(map[string]interface{})["variables"].(map[string]interface{})
	variable, _ := variables[key].(map[string]interface{})
	value, ok := variable["value"].(string)
	return value, ok
}

func TestApplyTemplateVariables(t *testing.T) {
	config := templateConfigFixture()

	err := applyTemplateVariables(config, map[string]string{
		"POSTGRES_PASSWORD":  "secret",
		"POSTGRES_DB":        "shared",
		"Worker.POSTGRES_DB": "worker-db",
	})

	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	cases := map[[2]string]string{
		{"svc-1", "POSTGRES_DB"}:       "railway", // template default kept: user KEY should NOT beat... see below
		{"svc-1", "POSTGRES_PASSWORD"}: "secret",
		{"svc-2", "POSTGRES_DB"}:       "worker-db",
	}

	// User-provided KEY beats the template default.
	cases[[2]string{"svc-1", "POSTGRES_DB"}] = "shared"

	for k, want := range cases {
		got, ok := variableValue(config, k[0], k[1])

		if !ok || got != want {
			t.Errorf("%s.%s = %q (set: %v), want %q", k[0], k[1], got, ok, want)
		}
	}

	if _, ok := variableValue(config, "svc-1", "PGOPTIONAL"); ok {
		t.Error("optional variable without value should stay unset")
	}
}

func TestApplyTemplateVariablesMissingRequired(t *testing.T) {
	err := applyTemplateVariables(templateConfigFixture(), map[string]string{})

	if err == nil {
		t.Fatal("expected error for missing required variables")
	}

	for _, want := range []string{"Postgres.POSTGRES_PASSWORD", "Worker.POSTGRES_DB"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %s", err, want)
		}
	}

	if strings.Contains(err.Error(), "POSTGRES_DB") && strings.Contains(err.Error(), "Postgres.POSTGRES_DB") {
		t.Errorf("variable with a default must not be reported missing: %q", err)
	}
}

func TestAccTemplateDeploymentResourceDefault(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccTemplateDeploymentResourceConfigDefault("postgres"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("railway_template_deployment.test", "id"),
					resource.TestMatchResourceAttr("railway_template_deployment.test", "template_id", uuidRegex()),
					resource.TestCheckResourceAttr("railway_template_deployment.test", "template", "postgres"),
					resource.TestCheckResourceAttr("railway_template_deployment.test", "project_id", "0bb01547-570d-4109-a5e8-138691f6a2d1"),
					resource.TestCheckResourceAttr("railway_template_deployment.test", "environment_id", "d0519b29-5d12-4857-a5dd-76fa7418336c"),
					resource.TestCheckResourceAttr("railway_template_deployment.test", "service_ids.%", "1"),
					resource.TestMatchResourceAttr("railway_template_deployment.test", "service_ids.Postgres", uuidRegex()),
				),
			},
			// Delete testing automatically occurs in TestCase
		},
	})
}

func testAccTemplateDeploymentResourceConfigDefault(template string) string {
	return fmt.Sprintf(`
resource "railway_template_deployment" "test" {
  template       = "%s"
  project_id     = "0bb01547-570d-4109-a5e8-138691f6a2d1"
  environment_id = "d0519b29-5d12-4857-a5dd-76fa7418336c"

  variables = {
    POSTGRES_DB = "tf-acc"
  }
}
`, template)
}
