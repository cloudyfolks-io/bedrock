FROM --platform=$BUILDPLATFORM node:24-bookworm-slim AS ui
WORKDIR /src/ui
RUN corepack enable
COPY ui/ ./
RUN pnpm install --frozen-lockfile
RUN pnpm --filter login build

FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/internal/authn/server/ui/dist /src/internal/authn/server/ui/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags "-X github.com/cloudyfolks-io/bedrock/internal/cli.Version=${VERSION}" -o /bedrock ./cmd/bedrock

FROM gcr.io/distroless/static:nonroot
COPY --from=build /bedrock /bedrock
COPY dist/release /release
ENTRYPOINT ["/bedrock"]
