# Builds the Purchase Desk API into a small image.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app && mkdir -p /data/files && chown app /data/files
USER app
COPY --from=build /out/server /usr/local/bin/server
ENV TZ=Asia/Kolkata
EXPOSE 8080
ENTRYPOINT ["server"]
