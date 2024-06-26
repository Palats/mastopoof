# Proto building
FROM node:20.14 AS protobuild

RUN groupadd --gid 10001 build && useradd --no-log-init --gid build --uid 10001 --create-home build
USER build:build

RUN mkdir /home/build/src
COPY .dockerignore /home/build/src
COPY --chown=build:build proto /home/build/src/proto/
WORKDIR /home/build/src/proto/
RUN npm install
RUN npm run gen


# Frontend building
FROM node:20.14 AS jsbuild

RUN groupadd --gid 10001 build && useradd --no-log-init --gid build --uid 10001 --create-home build
USER build:build

RUN mkdir /home/build/src
COPY .dockerignore /home/build/src
COPY --from=protobuild /home/build/src/proto/ /home/build/src/proto/
COPY --chown=build:build frontend /home/build/src/frontend/
WORKDIR /home/build/src/frontend/
RUN npm install
RUN npm run build


# Backend building
FROM golang:1.22 AS gobuild

RUN groupadd --gid 10001 build && useradd --no-log-init --gid build --uid 10001 --create-home build
USER build:build

RUN mkdir /home/build/src
COPY .dockerignore /home/build/src
COPY --from=jsbuild /home/build/src/proto/ /home/build/src/proto/
COPY --from=jsbuild /home/build/src/frontend/ /home/build/src/frontend/
COPY --chown=build:build backend /home/build/src/backend/
WORKDIR /home/build/src/backend/
RUN go mod download && go mod verify
# Fails to connect, with 404
# RUN go test ./...
# CGO is needed for go-sqlite3
RUN go build -o app

# Running
# Pick the same base image as Go build to make CGO work.
FROM golang:1.22 AS run
# TODO: Simplify and pick some more basic image.
# FROM gcr.io/distroless/static-debian12 as run
# Example: docker run --entrypoint=sh -ti mastopoof
# FROM gcr.io/distroless/static-debian12:debug as run
COPY --from=gobuild /home/build/src/backend/app /app

RUN groupadd --gid 10002 server && useradd --no-log-init --gid server --uid 10002 server
USER server:server

ENTRYPOINT ["/app"]