# Build from the repository root:
#   docker build -f examples/distributed/Dockerfile .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
WORKDIR /src/examples
RUN GOWORK=off CGO_ENABLED=0 go build -o /app ./distributed

FROM gcr.io/distroless/static-debian12
COPY --from=build /app /app
EXPOSE 8080
ENTRYPOINT ["/app"]
