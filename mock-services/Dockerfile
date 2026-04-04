FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 go build -o /mock-server .

FROM alpine:3.21
RUN apk --no-cache add ca-certificates
COPY --from=builder /mock-server /mock-server
EXPOSE 9001 9002 9003
CMD ["/mock-server"]
