package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/threadedstream/p2ppic/internal/peer"
	"go.uber.org/zap"
)

var (
	privKeyPath = flag.String("privkey", "./privkey", "path to the private key")
	apiPort     = flag.Int("api-port", 8000, "port to bind API server to")
)

func main() {
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL)
	defer cancel()

	logger := mustSetupLogger()

	privKey := mustUnmarshalPrivateKey(logger, *privKeyPath)

	p, err := peer.NewHeavy(privKey, logger)
	if err != nil {
		log.Fatal(err)
	}

	if err := p.Run(ctx, *apiPort); err != nil {
		log.Fatal(err)
	}

	<-ctx.Done()
}

func mustUnmarshalPrivateKey(logger *zap.Logger, privKeyPath string) libp2pcrypto.PrivKey {
	contents, err := os.ReadFile(privKeyPath)
	if err != nil {
		logger.Fatal("failed to read file", zap.Error(err))
	}
	privKey, err := libp2pcrypto.UnmarshalPrivateKey(contents)
	if err != nil {
		logger.Fatal("failed to unmarshal RSA private key", zap.Error(err))
	}
	return privKey
}

func mustSetupLogger() *zap.Logger {
	cfg := zap.NewProductionConfig()
	cfg.DisableStacktrace = true
	cfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)

	logger, err := cfg.Build()
	if err != nil {
		panic(err)
	}

	return logger
}
