FROM golang:1.22.5 AS builder

WORKDIR /app

# Copy go.mod and go.sum files and download dependencies
COPY go.mod go.sum ./
ENV GOPROXY=https://goproxy.cn,direct
RUN go mod download

# Copy the entire project
COPY . .

# Build the project
RUN CGO_ENABLED=0 GOOS=linux go build -o bin/colocation-memory-device-plugin cmd/main.go

FROM alpine:latest

WORKDIR /root/

# Copy the binary from the builder stage
COPY --from=builder /app/bin/colocation-memory-device-plugin .

ENTRYPOINT ["./i-device-plugin"]
