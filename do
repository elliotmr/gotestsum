#!/usr/bin/env bash

source .plsdo.sh

binary() {
    mkdir -p dist
    go build -o dist/gotestsum .
}

update-golden() {
    gotestsum -- ./testjson ./internal/junitxml -test.update-golden
}

lint() {
    golangci-lint run -v
}

go-mod-tidy() {
    go mod tidy
    git diff --stat --exit-code go.mod go.sum
}

help[shell]='Run a shell in a golang docker container.

Env vars:

GOLANG_VERSION - the docker image tag used to build the image.
'
shell() {
    local image; image="$(_docker-build-dev)"
    docker run \
        --tty --interactive --rm \
        -v "$PWD:/work" \
        -v ~/.cache/go-build:/root/.cache/go-build \
        -v ~/go/pkg/mod:/go/pkg/mod \
        -w /work \
        "$image" \
        "${@-bash}"
}

_docker-build-dev() {
    set -e
    local idfile=".plsdo/docker-build-dev-image-id-${GOLANG_VERSION-default}"
    local dockerfile=Dockerfile
    local tag=gotest.tools/gotestsum/builder
    if [ -f "$idfile" ] && [ "$dockerfile" -ot "$idfile" ]; then
        cat "$idfile"
        return 0
    fi

    mkdir -p .plsdo
    >&2 docker build \
        --iidfile "$idfile"  \
        --file "$dockerfile" \
        --build-arg "UID=$UID" \
        --build-arg GOLANG_VERSION \
        --target "dev" \
        .plsdo
    cat "$idfile"
}

_plsdo_run "$@"
