ARG GO_VERSION=1.24
ARG ALPINE_VERSION=3.22

FROM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS build
RUN apk add --no-cache build-base
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/submanager .

FROM alpine:${ALPINE_VERSION} AS runtime
RUN apk add --no-cache ca-certificates tzdata && addgroup -S submanager && adduser -S -G submanager submanager
WORKDIR /app
COPY --from=build /out/submanager /usr/local/bin/submanager
RUN mkdir -p /data && chown submanager:submanager /data
USER submanager
ENV PORT=8080 DB_PATH=/data/submanager.db TZ=Asia/Seoul
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["submanager"]
