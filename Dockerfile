# golang:1 tracks the latest Go release. The image sets GOTOOLCHAIN=local,
# so it must be at least as new as the go line in go.mod.
FROM golang:1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/yormd ./cmd/yormd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/yormd /yormd
EXPOSE 8080
ENTRYPOINT ["/yormd"]
