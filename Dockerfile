# TODO: use non-root users.

# Proto building
FROM node:20.14 AS protobuild

RUN mkdir /src

COPY .dockerignore /src
COPY proto /src/proto/
WORKDIR /src/proto/
RUN npm install
RUN npm run gen


# Frontend building
FROM node:20.14 AS jsbuild

RUN mkdir /src

COPY .dockerignore /src
COPY --from=protobuild /src/proto/ /src/proto/
COPY frontend /src/frontend/
WORKDIR /src/frontend/
RUN npm install
RUN npm run build


# Backend building
FROM golang:1.22 AS gobuild

RUN mkdir /src

COPY .dockerignore /src
COPY --from=jsbuild /src/proto/ /src/proto/
COPY --from=jsbuild /src/frontend/ /src/frontend/
COPY backend /src/backend/
WORKDIR /src/backend/
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
COPY --from=gobuild /src/backend/app /app
ENTRYPOINT ["/app"]