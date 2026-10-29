FROM golang:1.27.3 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/thousandeyes .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/thousandeyes /usr/local/bin/thousandeyes
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/thousandeyes"]
