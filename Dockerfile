# Stage 1: Build the Go application
FROM golang:1.24.5-alpine AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download all dependencies. Dependencies will be cached if the go.mod and go.sum files are not changed
RUN go mod download

# Copy the source code from the current directory to the working Directory inside the container
COPY . .

# Build the Go app
# CGO_ENABLED=0 is for static linking
# -o /main specifies the output file name
RUN CGO_ENABLED=0 GOOS=linux go build -o /main .

# Stage 2: Run the application
FROM alpine:latest

WORKDIR /root/

# Copy the pre-built binary from the previous stage
COPY --from=builder /main .

# Copy the profanity words file
COPY eng.txt .

# Expose port 8080 to the outside world
EXPOSE 8080

# Command to run the executable
CMD ["./main"]
