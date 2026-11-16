FROM golang:1.27.3-bookworm AS build
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY public ./public
RUN CGO_ENABLED=0 go test ./... && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /photo-manager .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /photo-manager /photo-manager
ENV PORT=8080
EXPOSE 8080
# With a read-only root filesystem, mount writable /tmp (for example --tmpfs /tmp:mode=1777).
USER 65532:65532
ENTRYPOINT ["/photo-manager"]
