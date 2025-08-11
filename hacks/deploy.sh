#!/bin/sh
PROJECT="prod-robot"
REGION="us-central1"
ID="reviewer"

export KO_DOCKER_REPO="gcr.io/${PROJECT}/${ID}"
gcloud run deploy "${ID}" --image="$(ko publish .)" --region "${REGION}" --project "${PROJECT}"

