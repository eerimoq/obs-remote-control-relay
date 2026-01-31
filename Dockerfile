# Build stage
FROM golang:1.22-alpine AS builder
WORKDIR /build

# Copy backend source and modules
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/*.go ./

# Build static binary
RUN CGO_ENABLED=0 go build -o relay .

# Runtime stage
FROM alpine:3.19
RUN apk --no-cache add ca-certificates
WORKDIR /app

# Layout: binary in backend/, frontend as sibling (main.go serves from "../frontend")
COPY --from=builder /build/relay backend/
COPY frontend frontend

WORKDIR /app/backend
EXPOSE 8080

CMD ["./relay"]
