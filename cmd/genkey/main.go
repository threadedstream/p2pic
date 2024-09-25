package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"os"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
)

var (
	keyPath = flag.String("keyout", "", "file to put key into")
)

func main() {
	flag.Parse()

	if *keyPath == "" {
		panic("empty keyout")
	}

	generateAndMarshalPrivKey(*keyPath)

	fmt.Println("key has been successfully generated and stored into file")
}

func generateAndMarshalPrivKey(keyout string) {
	privKey, _, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		panic(fmt.Errorf("failed to generate priv key: %w", err))
	}

	bs, err := libp2pcrypto.MarshalPrivateKey(privKey)
	if err != nil {
		panic(fmt.Errorf("failed to marshal priv key: %w", err))
	}

	if err = os.WriteFile(keyout, bs, os.ModePerm); err != nil {
		panic(fmt.Errorf("failed to write priv key to a file: %w", err))
	}
}
