FROM golang:1.20 as base

WORKDIR /app

COPY ./log-service/go.mod ./log-service/go.sum ./
RUN go mod download
COPY ./log-service/*.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /log-service .

FROM gcr.io/distroless/static-debian11

COPY --from=base /log-service .

CMD ["/log-service"]
