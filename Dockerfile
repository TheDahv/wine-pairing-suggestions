# Base config and development settings for the Webapp Docker build.
FROM golang:1.24

WORKDIR /usr/src/app

COPY go.mod go.sum ./
RUN go mod download

RUN go install github.com/air-verse/air@latest 

COPY . .
CMD ["air"]