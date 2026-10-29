# syntax=docker/dockerfile:1

FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/hafleet ./cmd/hafleet

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/hafleet /hafleet
ENV HAFLEET_DATA_DIR=/data \
    HAFLEET_LISTEN=:8080
VOLUME ["/data"]
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/hafleet"]
CMD ["serve"]
