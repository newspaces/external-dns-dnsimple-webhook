FROM golang:1.24-alpine AS build

WORKDIR /src
COPY go.mod ./
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/external-dns-dnsimple-webhook .

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/external-dns-dnsimple-webhook /external-dns-dnsimple-webhook

USER 65532:65532
ENTRYPOINT ["/external-dns-dnsimple-webhook"]
