PB = github.com/cmspeedau/gamedepot/internal/proto

.PHONY: build run test vet tailwind proto

build:
	go build -o bin/server ./cmd/server

run:
	go run ./cmd/server

test:
	go test ./...

vet:
	go vet ./...

tailwind:
	tailwindcss -i web/static/input.css -o web/static/app.css --minify

# Regenerate the Ubisoft protocol types. protoc plus protoc-gen-go must be on PATH.
# The generated files are committed, so ordinary builds do not need this. One Go package
# per proto file: the upstream proto packages share message names (Req, Rsp, Upstream),
# which would collide in a single generated package.
proto:
	@mkdir -p internal/proto
	protoc -I proto/ubisoft --go_out=internal/proto --go_opt=module=$(PB) \
	  --go_opt=Mdownload.proto=$(PB)/downloadpb \
	  --go_opt=Mdemux.proto=$(PB)/demuxpb \
	  --go_opt=Mdownload_service.proto=$(PB)/downloadservicepb \
	  --go_opt=Mownership.proto=$(PB)/ownershippb \
	  proto/ubisoft/*.proto
