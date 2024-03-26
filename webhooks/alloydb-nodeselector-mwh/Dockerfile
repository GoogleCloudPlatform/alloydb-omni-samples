FROM golang:1.21 AS builder

#Change the working directory
WORKDIR /go/src/app

#Copy the source code to the working directory
COPY . .

RUN go mod download

#Build the Go binary after dependency installation
RUN CGO_ENABLED=0 go build -o /go/bin/alloywebhook

#Using distroless debian for executing the Go binary
FROM gcr.io/distroless/static-debian12

COPY --from=builder /go/bin/alloywebhook /

#Execute
ENTRYPOINT ["/alloywebhook"]