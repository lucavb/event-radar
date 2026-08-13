FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/munich-events ./cmd/munich-events

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/munich-events /munich-events
USER nonroot:nonroot
ENTRYPOINT ["/munich-events"]
