FROM golang:1.27.3-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY public ./public
RUN CGO_ENABLED=0 go test ./... && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /photo-manager .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /photo-manager /photo-manager
ENV PORT=8080
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/photo-manager"]
