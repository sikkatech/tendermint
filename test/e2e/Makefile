docker:
	docker build --tag tendermint/e2e-node -f docker/Dockerfile ../..

ci: build/runner
	./build/runner -f networks/ci.toml

app: build/app
build/app:
	go build -o build/app ./app

runner: build/runner
build/runner:
	go build -o build/runner ./runner

.PHONY: app ci docker runner test
