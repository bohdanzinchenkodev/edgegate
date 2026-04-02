FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /edgegate ./cmd/edgegatek8s

FROM alpine:3.20
COPY --from=build /edgegate /usr/local/bin/edgegate
ENTRYPOINT ["edgegate"]
CMD ["-conf", "/etc/edgegate/edgegate.yaml"]
