FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/9router-lite .

FROM alpine:3.22 AS certs
RUN apk --no-cache add ca-certificates

FROM scratch
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/9router-lite /9router-lite
EXPOSE 20129
ENV PORT=20129
ENV DATA_DIR=/data
ENV LITE_MEMORY_LIMIT_MB=24
VOLUME ["/data"]
ENTRYPOINT ["/9router-lite"]
