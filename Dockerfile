# Build environment
# -----------------
FROM golang:latest as builder
LABEL stage=builder

ARG DEBIAN_FRONTEND=noninteractive

SHELL ["/bin/bash", "-o", "pipefail", "-c"]
# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y ca-certificates openssl git tzdata && \
  update-ca-certificates && \
  rm -rf /var/lib/apt/lists/*

WORKDIR /src

COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

ARG CACHEBUST=1

COPY internal/ internal/
COPY main.go main.go

# Get the current commit hash
ARG COMMIT_HASH

# Build
RUN CGO_ENABLED=0 GO111MODULE=on go build -a -ldflags "-X main.Build=${COMMIT_HASH}" -o /bin/resources-presenter ./main.go && \
    strip /bin/resources-presenter

# Deployment environment
# ----------------------
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /bin/resources-presenter /bin/resources-presenter

USER nonroot:nonroot

ENTRYPOINT ["/bin/resources-presenter"]
