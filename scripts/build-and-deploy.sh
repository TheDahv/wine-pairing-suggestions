#! /usr/bin/env bash

IMAGE=wine-pairing-suggestions
ACCOUNT_ID=520805538041
AWS_REGION=us-west-2

aws ecr get-login-password --region us-west-2 \
    | docker login --username AWS --password-stdin "$ACCOUNT_ID.dkr.ecr.$AWS_REGION.amazonaws.com"

docker buildx build -f Dockerfile.production -t "$IMAGE:latest" --platform linux/amd64 .

docker tag "$IMAGE:latest" "$ACCOUNT_ID.dkr.ecr.$AWS_REGION.amazonaws.com/wine-pairing-suggestions/webapp:latest"
docker push "$ACCOUNT_ID.dkr.ecr.$AWS_REGION.amazonaws.com/wine-pairing-suggestions/webapp:latest"