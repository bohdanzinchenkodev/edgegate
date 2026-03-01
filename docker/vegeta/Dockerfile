FROM golang:1.23-alpine AS build
ARG VEGETA_VERSION=v12.13.0
RUN go install github.com/tsenart/vegeta/v12@${VEGETA_VERSION}

FROM alpine:3.20
COPY --from=build /go/bin/vegeta /usr/local/bin/vegeta
ENTRYPOINT ["vegeta"]
