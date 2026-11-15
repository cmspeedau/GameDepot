.PHONY: build run test vet tailwind

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
