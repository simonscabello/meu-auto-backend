# Build stage.
FROM golang:1.25-alpine AS build

WORKDIR /src

# Dependencies are copied first so a source-only change reuses the cached download layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produces a static binary, which is what lets the runtime image below carry
# no libc at all. -trimpath keeps build machine paths out of the binary.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# Runtime stage.
#
# distroless/static has no shell, no package manager and no utilities — an attacker who
# reaches code execution finds nothing to pivot with. Migrations are embedded in the
# binary, so nothing else needs to be in the image.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/api /api

USER nonroot:nonroot
EXPOSE 8080

ENTRYPOINT ["/api"]
