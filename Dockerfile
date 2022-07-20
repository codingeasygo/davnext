FROM golang:1.17
WORKDIR /davnext/
ADD . /davnext/
RUN GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -v .
RUN ./davnext --help

FROM alpine:latest  
RUN apk --no-cache add ca-certificates
WORKDIR /var/lib/dav
COPY --from=0 /davnext/davnext /usr/bin/
CMD ["/usr/bin/davnext"]
