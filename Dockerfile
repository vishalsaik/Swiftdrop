# syntax=docker/dockerfile:1

# Build: one static binary per command. The dashboard is already built and committed in
# web/static, so there is no Node stage; CI checks that it matches web/ui.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/api ./cmd/migrate ./cmd/seed ./cmd/user

# Run: nothing but the binaries, the CA certificates (for Stripe) and a user that is not root.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /app/
# A container listens on all its interfaces; what reaches it is decided by how the port is published.
ENV LISTEN_ADDR=0.0.0.0:8080
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/app/api"]
