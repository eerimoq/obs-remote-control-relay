FROM golang:1.22.0-alpine as builder

RUN go install golang.org/dl/go1.22.0@latest \
    && go1.22.0 download

WORKDIR /build

COPY backend/go.mod .
COPY backend/go.sum .
COPY backend/main.go .
COPY . .

RUN go build -o ./app -tags timetzdata -trimpath -ldflags="-s -w" .

FROM gcr.io/distroless/base-debian12

COPY --from=builder /build/app /app/app
COPY frontend/* /frontend/

EXPOSE 8080
ENTRYPOINT ["/app/app", "-address", "0.0.0.0:9999"]