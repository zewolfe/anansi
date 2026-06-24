FROM golang:1.22-bookworm AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags="-s -w" \
    -o /out/anansi ./cmd/anansi

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=builder /out/anansi /anansi
COPY --from=builder /src/configs /configs
USER nonroot:nonroot

ENTRYPOINT ["/anansi"]
