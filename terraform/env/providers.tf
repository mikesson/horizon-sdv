provider "google" {
  project = var.sdv_gcp_project_id
  region  = var.sdv_gcp_region
  zone    = var.sdv_gcp_zone
}

provider "google-beta" {
  project = var.sdv_gcp_project_id
  region  = var.sdv_gcp_region
  zone    = var.sdv_gcp_zone
}
