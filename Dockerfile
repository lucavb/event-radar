FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/event-radar ./cmd/event-radar

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/event-radar /event-radar
USER nonroot:nonroot
ENTRYPOINT ["/event-radar"]
