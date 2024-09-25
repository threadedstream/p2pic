# p2pic

The idea of project was born during the long trip home. I designed a hypothetical architecture of p2p service that would be able to store images and propagate them across the network. I wrote it down in my notebook and shared it with a handful of subs on my tg channel. And here it is, I finally implemented it in Go. 

# Usage

Before running program, make sure to generate private key. To do that, you need to compile a program inside cmd/genkey/main.go and just run it. Note that each peer will need its own private key.
 
Then you can spin up peers by issuing the following series of commands

```bash
go build cmd/p2pic/main.go

./p2pic --privkey privkey1 --api-port 8000
./p2pic --privkey privkey2 --api-port 8001
./p2pic --privkey privkey3 --api-port 8002
```

Apparently, each peer runs in its own terminal

In order to store an image, you need to hit `/v1/image/post` endpoint, which accepts an image (surprising, huh). Currently, peer responds back once it sent an image to all known peers. The response is hex encoded hash of an image.

To check that image's been successfully propagated, try asking other peer for an image by hitting `/v1/image/fetch` endpoint.

