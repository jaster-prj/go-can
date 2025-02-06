# Developing inside DevContainer

Build Container for using with DevContainer development 
```
docker buildx build -t devcontainer-go-dev:latest --build-arg GO_VERSION="1.23.6" --target dev .
```

