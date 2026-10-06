# Nieuws Hub: static Go binary on distroless (no shell, no package manager), non-root.
# Published images: ghcr.io/digibaro/news-dashboard-docker (linux/amd64, linux/arm64), see .github/workflows.
# Build locally:   docker build -t nieuwsdashboard .
# Run:             see docker-compose.yml.default (copy it to docker-compose.yml)

# The build stage runs on the build machine's own platform and cross-compiles for the target,
# so multi-arch images need no emulation (tests run natively, once).
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go feeds.go panels.go extras.go push.go waste.go sky.go nature.go sports.go amber.go satellite.go wiki.go radiation.go solar.go icons.go world.go hazards.go coast.go daily.go cyber.go main_test.go extras_test.go config.yaml.default VERSION ./
COPY web ./web
COPY testdata ./testdata
# the image is only built when vet and the unit tests pass (including a check of config.yaml.default)
RUN go vet ./... && go test -count=1 ./...
# the version is read from the VERSION file, so a local docker-compose.yml never goes stale
# declared here, after the tests, so the vet/test step is shared by all target platforms
ARG TARGETOS=linux TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.version=$(tr -d ' \n' < VERSION)" -o /out/nieuwsdashboard .

# distroless/static ships CA certificates and tzdata (the binary embeds tzdata as well).
FROM gcr.io/distroless/static:nonroot
LABEL org.opencontainers.image.title="Nieuws Hub" \
      org.opencontainers.image.description="Dutch news, weather and cyber-threat dashboard in one Go binary" \
      org.opencontainers.image.source="https://github.com/digibaro/news-dashboard-docker" \
      org.opencontainers.image.licenses="GPL-3.0-or-later"
WORKDIR /app
COPY --from=build /out/nieuwsdashboard /app/nieuwsdashboard
# built-in default; docker-compose.yml mounts your own config.yaml over it
COPY config.yaml.default /app/config.yaml
ENV NDB_LISTEN=0.0.0.0:8080 \
    NDB_CONFIG=/app/config.yaml \
    GOMEMLIMIT=96MiB
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 CMD ["/app/nieuwsdashboard", "-healthcheck"]
ENTRYPOINT ["/app/nieuwsdashboard"]
