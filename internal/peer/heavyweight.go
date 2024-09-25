package peer

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/syndtr/goleveldb/leveldb"
	"go.uber.org/zap"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const (
	imageSizeLimit = 2 * 1024 * 1024 // 2MB
)

const (
	postImageProtocolID = "/p2ppic/postimage/1.0.0"
	discoveryNamespace  = "p2pic"
)

type PeerResponseStatus = int

const (
	statusOk PeerResponseStatus = iota
	statusErr
)

var (
	enc = binary.BigEndian
)

type peerResponse struct {
	Status PeerResponseStatus `json:"status"`
	Err    string             `json:"err,omitempty"`
}

type HeavyweightNode struct {
	storage          *leveldb.DB
	logger           *zap.Logger
	privKey          crypto.PrivKey
	neighboringPeers map[string]peer.AddrInfo
	host             host.Host
	ctx              context.Context
}

func NewHeavy(privKey crypto.PrivKey, logger *zap.Logger) (*HeavyweightNode, error) {
	return &HeavyweightNode{
		logger:           logger,
		privKey:          privKey,
		neighboringPeers: make(map[string]peer.AddrInfo),
	}, nil
}

func (n *HeavyweightNode) Run(ctx context.Context, apiPort int) error {
	host, err := libp2p.New(
		libp2p.Identity(n.privKey),
	)
	if err != nil {
		return err
	}

	n.ctx = ctx

	n.host = host
	n.logger.Info(fmt.Sprintf("peer id: %s", n.host.ID()))

	storage, err := leveldb.OpenFile(n.host.ID().String(), nil)
	if err != nil {
		n.logger.Error("failed to open leveldb")
		return err
	}

	n.storage = storage

	n.host.SetStreamHandler(postImageProtocolID, func(stream network.Stream) {
		if err := n.handlePostImage(stream); err != nil {
			n.logger.Error("failed to post image", zap.Error(err))
		}
	})

	go n.runAPIServer(ctx, apiPort)

	service := mdns.NewMdnsService(n.host, discoveryNamespace, n)

	return service.Start()
}

func (n *HeavyweightNode) HandlePeerFound(peerInfo peer.AddrInfo) {
	if _, ok := n.neighboringPeers[peerInfo.ID.String()]; ok {
		return
	}

	if err := n.host.Connect(n.ctx, peerInfo); err != nil {
		n.logger.Error("failed to connect to peer", zap.String("peer_id", peerInfo.ID.String()))
		return
	}

	n.logger.Info("successfully connected to peer", zap.String("peer_id", peerInfo.ID.String()))

	n.neighboringPeers[peerInfo.ID.String()] = peerInfo
}

func (n *HeavyweightNode) runAPIServer(ctx context.Context, port int) {
	router := chi.NewRouter()

	router.Use(
		httpAccessLog(n.logger),
	)

	router.Route("/v1", func(r chi.Router) {
		r.Post("/image/post", n.postImage)
		r.Get("/image/fetch", n.fetchImage)
	})

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: router,
	}

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}

	<-ctx.Done()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		n.logger.Error("failed to shutdown a server")
	}
}

func (n *HeavyweightNode) postImage(w http.ResponseWriter, r *http.Request) {
	limitedReader := http.MaxBytesReader(nil, r.Body, imageSizeLimit)

	image, err := io.ReadAll(limitedReader)
	if err != nil {
		n.throwHTTP(w, http.StatusBadRequest, "p2ppic accepts images up 1MB", err)
		return
	}

	hash, err := n.postImageInternal(image)
	if err != nil {
		n.throwHTTP(w, http.StatusInternalServerError, "failed to store image locally", err)
		return
	}

	n.propagateAcrossPeers(image)

	hashHex := hex.EncodeToString(hash)
	_, _ = w.Write([]byte(hashHex))
}

func (n *HeavyweightNode) fetchImage(w http.ResponseWriter, r *http.Request) {
	imageHashIDHex, err := io.ReadAll(r.Body)
	if err != nil {
		n.throwHTTP(w, http.StatusInternalServerError, "failed to get image by id", err)
		return
	}

	imageHashID, err := hex.DecodeString(string(imageHashIDHex))
	if err != nil {
		n.throwHTTP(w, http.StatusInternalServerError, "failed to get image by id", err)
		return
	}

	//todo: handle image types
	// for now we assume it's jpeg image
	image, err := n.storage.Get(imageHashID, nil)
	if err != nil {
		n.throwHTTP(w, http.StatusInternalServerError, "failed to get image from storage", err)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(image)
}

func (n *HeavyweightNode) handlePostImage(stream network.Stream) error {
	image, err := n.readMessage(stream)
	if err != nil {
		return err
	}

	if _, err := n.postImageInternal(image); err != nil {
		serialized, _ := json.Marshal(peerResponse{
			Status: statusErr,
			Err:    err.Error(),
		})

		return n.writeMessage(stream, serialized)
	}

	serialized, _ := json.Marshal(peerResponse{
		Status: statusOk,
	})

	return n.writeMessage(stream, serialized)
}

func (n *HeavyweightNode) propagateAcrossPeers(image []byte) {
	n.logger.Info("propagating image across peers")
	ctx, cancel := context.WithCancel(n.ctx)
	defer cancel()

	wg := sync.WaitGroup{}
	for _, p := range n.neighboringPeers {
		wg.Add(1)
		go func(p peer.AddrInfo) {
			defer wg.Done()
			if err := n.sendImageToPeer(ctx, p, image); err != nil {
				n.logger.Error("failed to send image to peer", zap.String("peer_id", p.ID.String()), zap.Error(err))
			}
		}(p)
	}

	wg.Wait()
}

func (n *HeavyweightNode) sendImageToPeer(ctx context.Context, p peer.AddrInfo, image []byte) error {
	n.logger.Info("sending image for peer", zap.String("peer_id", p.ID.String()))

	stream, err := n.host.NewStream(ctx, p.ID, postImageProtocolID)
	if err != nil {
		return err
	}

	if err := n.writeMessage(stream, image); err != nil {
		return err
	}

	n.logger.Info("wrote message")

	readMessage, err := n.readMessage(stream)
	// deserialize readMessage into struct and do something about it
	peerResp := peerResponse{}
	if err := json.Unmarshal(readMessage, &peerResp); err != nil {
		return err
	}

	n.logger.Info("read message")

	if peerResp.Status != statusOk {
		return errors.New(peerResp.Err)
	}

	return nil
}

func (n *HeavyweightNode) throwHTTP(w http.ResponseWriter, statusCode int, msg string, err error) {
	if n.logger != nil {
		n.logger.Error(msg, zap.Error(err))
	}
	w.WriteHeader(statusCode)
	_, _ = w.Write([]byte(msg))
}

func (n *HeavyweightNode) postImageInternal(image []byte) ([]byte, error) {
	hash := sha1.Sum(image)
	return hash[:], n.storage.Put(hash[:], image, nil)
}

func (n *HeavyweightNode) writeMessage(s network.Stream, message []byte) error {
	messageLen := uint32(len(message))

	err := binary.Write(s, enc, messageLen)
	if err != nil {
		return err
	}

	nn, err := s.Write(message)
	if err != nil {
		return err
	}
	if nn != int(messageLen) {
		return fmt.Errorf("expected to send %d bytes, but sent %d", messageLen, nn)
	}

	return nil
}

func (n *HeavyweightNode) readMessage(s network.Stream) ([]byte, error) {
	var messageLen uint32
	if err := binary.Read(s, enc, &messageLen); err != nil {
		return nil, err
	}

	buf := make([]byte, messageLen)

	readBytes, err := io.ReadFull(s, buf)
	if err != nil {
		return nil, err
	}

	if readBytes != int(messageLen) {
		return nil, fmt.Errorf("expected to read %d bytes, but read %d", messageLen, readBytes)
	}

	return buf, nil
}

func httpAccessLog(l *zap.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			start := time.Now()
			defer func() {
				l.Info("served",
					zap.String("addr", r.RemoteAddr),
					zap.String("method", r.Method),
					zap.String("path", r.URL.Path),
					zap.String("proto", r.Proto),
					zap.Int("status", ww.Status()),
					zap.Int("size", ww.BytesWritten()),
					zap.String("referer", r.Referer()),
					zap.String("ua", r.UserAgent()),
					zap.Duration("lat", time.Since(start)),
				)
			}()

			next.ServeHTTP(ww, r)
		}

		return http.HandlerFunc(fn)
	}
}
