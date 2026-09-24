FROM node:24-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# golang:1 tracks the latest Go release. The image sets GOTOOLCHAIN=local,
# so it must be at least as new as the go line in go.mod.
FROM golang:1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/yormd ./cmd/yormd
# distroless has no shell to mkdir with, so the upload dir is made here.
RUN mkdir -p /out/data/uploads

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/yormd /yormd
COPY --from=web /web/dist /web
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV YORM_WEB_DIR=/web YORM_UPLOAD_DIR=/data/uploads
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/yormd"]
