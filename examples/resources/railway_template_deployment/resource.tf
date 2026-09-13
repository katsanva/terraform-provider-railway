resource "railway_template_deployment" "postgres" {
  template       = "postgres"
  project_id     = railway_project.example.id
  environment_id = railway_project.example.default_environment.id

  variables = {
    POSTGRES_DB = "app"
  }
}

output "postgres_service_id" {
  value = railway_template_deployment.postgres.service_ids["Postgres"]
}
