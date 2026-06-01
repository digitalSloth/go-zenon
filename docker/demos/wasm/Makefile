.PHONY: build contract demo test clean

build:
	go build -o bin/wasm-cli ./cmd/wasm-cli/
	go build -o bin/wasm-replay ./cmd/wasm-replay/

contract:
	cd contract && sh build.sh

demo: build
	@sh demo.sh

test:
	go test ./wasmtest/... -v -count=1

clean:
	rm -rf bin/
	rm -f contract/demo.wasm
