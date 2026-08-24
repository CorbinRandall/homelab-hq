FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/homelab-hq .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates openssh-client tzdata
WORKDIR /app
COPY --from=build /out/homelab-hq /usr/local/bin/homelab-hq
COPY www/ /app/www/
EXPOSE 8888
ENTRYPOINT ["/usr/local/bin/homelab-hq"]
CMD ["-config", "/config/config.json"]
