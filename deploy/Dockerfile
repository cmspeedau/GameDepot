FROM golang:1.26-alpine AS build
WORKDIR /src
COPY backend/ ./
RUN go build -o /out/server ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/server /usr/local/bin/gamedepot
ENV PORT=4000
ENV DATA=/data
ENV LIBRARY=/library
ENV GAMEDEPOT_AUTO_MIGRATE=true
VOLUME ["/data", "/library"]
EXPOSE 4000
ENTRYPOINT ["gamedepot"]
