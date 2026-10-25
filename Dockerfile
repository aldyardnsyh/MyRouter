# syntax=docker/dockerfile:1

# ---- Stage 1: build the React SPA ------------------------------------------
FROM node:22-alpine AS web
WORKDIR /src/web
# Install from the lockfile first so the dependency layer stays cached.
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- Stage 2: compile the single Go binary ---------------------------------
FROM golang:1.23-alpine AS build
WORKDIR /src
# go.mod declares a newer language version than this image ships, so let the
# toolchain switch to the matching release.
ENV GOTOOLCHAIN=auto
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/ ./web/
# Overwrite the committed placeholder dist with the freshly built SPA so the
# go:embed directive in web/embed.go embeds the real assets.
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /myrouter ./cmd/myrouter

# ---- Stage 3: minimal runtime ---------------------------------------------
FROM gcr.io/distroless/static:nonroot
COPY --from=build /myrouter /myrouter
EXPOSE 20128
# WORKDIR creates /data owned by the image USER (nonroot), so the SQLite
# database and master key are writable without a shell to chown them.
WORKDIR /data
VOLUME /data
ENV MYROUTER_DATA=/data
ENTRYPOINT ["/myrouter"]
