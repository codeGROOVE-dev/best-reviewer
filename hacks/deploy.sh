#!/bin/sh
GCP_PROJECT="prod-robot"
GCP_REGION="us-central1"
APP_ID="reviewer"

# exit if any step fails
set -eux -o pipefail

# The Google Artifact Registry repository to create
export KO_DOCKER_REPO="gcr.io/${GCP_PROJECT}/${APP_ID}"

# Publish the code at . to $KO_DOCKER_REPO
IMAGE="$(ko publish .)"

# Deploy the newly built binary to Google Cloud Run
gcloud run deploy "${APP_ID}" --image="${IMAGE}" --region "${GCP_REGION}" --project "${GCP_PROJECT}"
