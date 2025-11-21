# tunnel-manager

TODO

# Building & pushing

Most probably the servers are running linux/amd64, but many developers use Macs that run on linux/arm64, 
so build this image for both platforms using the following command:

```sh
docker buildx build --push --platform linux/arm64,linux/amd64 -t mspanc/tunnel-manager .
```

# Usage

## Environment variables

TODO