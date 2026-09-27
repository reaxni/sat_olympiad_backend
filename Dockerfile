FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /server ./cmd/server && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /admin ./cmd/admin

FROM alpine:3.22
RUN adduser -D -H -u 10001 app
WORKDIR /app
USER app
COPY --from=build /server /server
COPY --from=build /admin /admin
COPY migrations /app/migrations
EXPOSE 8080
ENTRYPOINT ["/server"]
