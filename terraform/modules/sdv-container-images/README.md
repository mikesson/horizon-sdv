# sdv-container-images

Terraform builds selected container images and pushes them to Google Artifact Registry via the `kreuzwerker/docker` provider.

## Module inputs

See [`variables.tf`](variables.tf). Optional per image:

- `context_path` — absolute path to Docker build context
- `dockerfile_path` — absolute path to Dockerfile (when not `Dockerfile` inside the context)
- `platform` — e.g. `linux/amd64` for GKE-compatible builds from Apple Silicon

Default images use `images/<directory>/<image_name>/` as context and `Dockerfile` in that folder.
