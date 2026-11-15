FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux make build && mkdir -p /out && cp bin/modelgauge /out/modelgauge

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/modelgauge /usr/local/bin/modelgauge
ENTRYPOINT ["/usr/local/bin/modelgauge"]
