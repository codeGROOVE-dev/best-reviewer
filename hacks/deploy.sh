#!/bin/bash
# Deploy the current Go app to Google Cloud run
#
# usage:
#   ./hacks/deploy.sh - deploy the app in the current directory
#   ./hacks/deploy.sh cmd/server - deploy the app in cmd/server
set -eux -o pipefail
test -n "${1:-}" && cd "$1"

PROJECT=${GCP_PROJECT:=chat-bot-army}
REGISTRY="${PROJECT}"
REGION="us-central1"

APP_NAME=$(basename "$(go mod graph | head -n 1 | cut -d" " -f1)")
APP_USER="${APP_NAME}@${PROJECT}.iam.gserviceaccount.com"
APP_IMAGE="gcr.io/${REGISTRY}/${APP_NAME}"

gcloud iam service-accounts list --project "${PROJECT}" | grep -q "${APP_USER}" ||
	gcloud iam service-accounts create "${APP_NAME}" --project "${PROJECT}"

export KO_DOCKER_REPO="${APP_IMAGE}"
gcloud run deploy "${APP_NAME}" \
	--image="$(ko publish .)" \
	--region="${REGION}" \
	--service-account="${APP_USER}" \
	--project "${PROJECT}"
