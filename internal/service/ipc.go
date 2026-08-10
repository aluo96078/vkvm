package service

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

const IPCProtocolVersion uint16 = 1
const maxIPCFrame = 64 << 10

// IPCEnvelope is the minimal authenticated core/companion protocol. The
// transport (Unix socket, XPC wrapper, or named pipe) supplies local ACLs;
// the MAC prevents a compromised same-user client from issuing arbitrary RPCs
// without the short-lived secret provisioned by the core.
type IPCEnvelope struct {
	Version   uint16          `json:"version"`
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	MAC       string          `json:"mac"`
}

func SignIPC(secret []byte, message IPCEnvelope) (IPCEnvelope, error) {
	message.MAC = ""
	data, err := json.Marshal(message)
	if err != nil {
		return IPCEnvelope{}, err
	}
	if len(secret) == 0 {
		return IPCEnvelope{}, errors.New("ipc secret is empty")
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(data)
	message.MAC = hex.EncodeToString(mac.Sum(nil))
	return message, nil
}

func VerifyIPC(secret []byte, message IPCEnvelope) bool {
	provided, err := hex.DecodeString(message.MAC)
	if err != nil || len(provided) != sha256.Size || len(secret) == 0 || message.Version != IPCProtocolVersion {
		return false
	}
	message.MAC = ""
	data, err := json.Marshal(message)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(data)
	return subtle.ConstantTimeCompare(provided, mac.Sum(nil)) == 1
}

type IPCHandler func(context.Context, IPCEnvelope) (IPCEnvelope, error)

// ServeIPC handles newline-delimited, bounded JSON frames over a local
// listener. It is deliberately transport-agnostic so platform wrappers can
// provide a Unix-domain socket or a named-pipe adapter without duplicating
// authentication and version-negotiation logic.
func ServeIPC(ctx context.Context, listener net.Listener, secret []byte, handler IPCHandler) error {
	if listener == nil || len(secret) == 0 || handler == nil {
		return errors.New("invalid ipc server arguments")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var closeOnce sync.Once
	go func() {
		<-ctx.Done()
		closeOnce.Do(func() { _ = listener.Close() })
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			continue
		}
		go serveIPCConn(ctx, conn, secret, handler)
	}
}

func serveIPCConn(ctx context.Context, conn net.Conn, secret []byte, handler IPCHandler) {
	defer conn.Close()
	reader := bufio.NewReaderSize(conn, maxIPCFrame)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return
			}
			if len(line) == 0 {
				return
			}
		}
		if len(line) > maxIPCFrame {
			return
		}
		var request IPCEnvelope
		if json.Unmarshal(line, &request) != nil || !VerifyIPC(secret, request) {
			return
		}
		response, handlerErr := handler(ctx, request)
		if handlerErr != nil {
			response = IPCEnvelope{Version: IPCProtocolVersion, Type: "error", RequestID: request.RequestID, Payload: json.RawMessage(fmt.Sprintf(`{"error":%q}`, handlerErr.Error()))}
		}
		response.Version = IPCProtocolVersion
		signed, err := SignIPC(secret, response)
		if err != nil {
			return
		}
		data, err := json.Marshal(signed)
		if err != nil {
			return
		}
		if _, err := conn.Write(append(data, '\n')); err != nil {
			return
		}
		if err != nil {
			return
		}
	}
}
