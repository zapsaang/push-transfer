# Stage 1: Build static Go binary.
# The toolchain tag matches the `go` directive in go.mod so the build never has to
# download another toolchain (GOTOOLCHAIN) and stays reproducible.
FROM golang:1.26.8-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app/push-transfer ./cmd/push-transfer

# Stage 2: Minimal non-root runtime environment
FROM alpine:latest AS runner

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -g 10001 -S appgroup && \
    adduser -u 10001 -S appuser -G appgroup

WORKDIR /app

COPY --from=builder /app/push-transfer /app/push-transfer

EXPOSE 8889

USER 10001

ENTRYPOINT ["/app/push-transfer"]
