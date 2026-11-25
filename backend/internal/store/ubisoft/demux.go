// Package ubisoft is the Ubisoft Connect storefront adapter.
//
// demux.go is the high-level client over the demux socket: ticket authentication,
// service connections, and request/response matching inside a connection. It mirrors
// the vendored reference implementation (ubisoft-demux-node) byte-for-byte, because
// the demux protocol answers well-formed requests and silently times out on malformed
// ones.
package ubisoft

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/cmspeedau/gamedepot/internal/proto/demuxpb"
)

// Service names reachable via openConnection.
type DemuxServiceName string

const (
	ServiceClientConfiguration DemuxServiceName = "client_configuration_service"
	ServiceCloudSave           DemuxServiceName = "cloudsave_service"
	ServiceDenuvo              DemuxServiceName = "denuvo_service"
	ServiceDownload            DemuxServiceName = "download_service"
	ServiceFriends             DemuxServiceName = "friends_service"
	ServiceOwnership           DemuxServiceName = "ownership_service"
	ServiceParty               DemuxServiceName = "party_service"
	ServicePlaytime            DemuxServiceName = "playtime_service"
	ServiceStore               DemuxServiceName = "store_service"
	ServiceUtility             DemuxServiceName = "utility_service"
)

const demuxRequestTimeout = 25 * time.Second

type connState struct {
	downstreamFactory func() proto.Message
	nextID            uint32
	pending           map[uint32]chan proto.Message
	stampSess         bool
}

// DemuxClient wraps a DemuxSocket with authentication, service connections and
// request/response matching.
type DemuxClient struct {
	socket      *DemuxSocket
	connections map[int]connState
	mu          sync.Mutex
}

// NewDemuxClient constructs an unconnected client.
func NewDemuxClient() *DemuxClient {
	return &DemuxClient{connections: make(map[int]connState)}
}

// Connect opens the socket and authenticates with a Ubi_v1 ticket.
func (c *DemuxClient) Connect(ctx context.Context, ubiTicket string, insecure bool) error {
	sock := NewDemuxSocket(DemuxHost, 20*time.Second, DemuxEvents{
		ConnectionData: c.onConnectionData,
	})
	sock.insecure = insecure
	if err := sock.Connect(ctx); err != nil {
		return err
	}
	c.socket = sock

	rsp, err := sock.Request(ctx, func(id uint32) *demuxpb.Req {
		rid := id
		ticket := ubiTicket
		clientID := "uplay_pc"
		return &demuxpb.Req{
			RequestId: &rid,
			AuthenticateReq: &demuxpb.AuthenticateReq{
				Token:    &demuxpb.Token{UbiTicket: &ticket},
				ClientId: &clientID,
			},
		}
	})
	if err != nil {
		sock.Destroy()
		c.socket = nil
		return err
	}
	auth := rsp.GetAuthenticateRsp()
	if auth == nil || !auth.GetSuccess() {
		sock.Destroy()
		c.socket = nil
		msg := "Ubisoft demux rejected the stored ticket"
		if auth != nil && auth.GetExpired() {
			msg += " (expired - reconnect the account)"
		}
		return fmt.Errorf("ubisoft: %s", msg)
	}
	return nil
}

// OpenConnection opens a service connection and returns its connection id.
func (c *DemuxClient) OpenConnection(ctx context.Context, name DemuxServiceName,
	downstreamFactory func() proto.Message) (int, error) {
	if c.socket == nil {
		return 0, fmt.Errorf("ubisoft: demux client is not connected")
	}
	rsp, err := c.socket.Request(ctx, func(id uint32) *demuxpb.Req {
		rid := id
		sn := string(name)
		return &demuxpb.Req{
			RequestId:         &rid,
			OpenConnectionReq: &demuxpb.OpenConnectionReq{ServiceName: &sn},
		}
	})
	if err != nil {
		return 0, err
	}
	opened := rsp.GetOpenConnectionRsp()
	if opened == nil || !opened.GetSuccess() {
		return 0, fmt.Errorf("ubisoft: demux refused the %s connection", name)
	}
	cid := int(opened.GetConnectionId())
	c.mu.Lock()
	c.connections[cid] = connState{
		downstreamFactory: downstreamFactory,
		nextID:            1,
		pending:           make(map[uint32]chan proto.Message),
		stampSess:         name == ServiceOwnership,
	}
	c.mu.Unlock()
	return cid, nil
}

// ServiceRequest sends one service request inside a connection and awaits its response.
func (c *DemuxClient) ServiceRequest(ctx context.Context, connID int, upstream proto.Message) (proto.Message, error) {
	c.mu.Lock()
	conn, ok := c.connections[connID]
	if !ok || c.socket == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("ubisoft: demux connection %d is not open", connID)
	}
	reqID := conn.nextID
	conn.nextID++
	ch := make(chan proto.Message, 1)
	conn.pending[reqID] = ch
	c.mu.Unlock()

	// stamp request id (and session id for ownership) on the inner request
	if m, ok := upstream.(interface{ ProtoReflect() protoreflect.Message }); ok {
		md := m.ProtoReflect()
		if reqField := md.Descriptor().Fields().ByName("request"); reqField != nil {
			if rv := md.Get(reqField); rv.IsValid() && rv.Message().IsValid() {
				reqMsg := rv.Message()
				if f := reqMsg.Descriptor().Fields().ByName("request_id"); f != nil {
					reqMsg.Set(f, protoreflect.ValueOfUint32(reqID))
				}
				if conn.stampSess {
					if f := reqMsg.Descriptor().Fields().ByName("ubi_session_id"); f != nil {
						reqMsg.Set(f, protoreflect.ValueOfString(uuid.NewString()))
					}
				}
			}
		}
	}

	inner, err := proto.Marshal(upstream)
	if err != nil {
		c.removePending(connID, reqID)
		return nil, fmt.Errorf("ubisoft: marshal service upstream: %w", err)
	}
	prefixed := make([]byte, 4+len(inner))
	binary.BigEndian.PutUint32(prefixed[:4], uint32(len(inner)))
	copy(prefixed[4:], inner)

	cid := uint32(connID)
	if err := c.socket.Push(ctx, &demuxpb.Push{
		Data: &demuxpb.DataMessage{ConnectionId: &cid, Data: prefixed},
	}); err != nil {
		c.removePending(connID, reqID)
		return nil, err
	}

	timer := time.NewTimer(demuxRequestTimeout)
	defer timer.Stop()
	select {
	case rsp := <-ch:
		if rsp == nil {
			return nil, fmt.Errorf("ubisoft: service request %d: connection closed", reqID)
		}
		return rsp, nil
	case <-timer.C:
		c.removePending(connID, reqID)
		return nil, fmt.Errorf("ubisoft: demux service request %d timed out", reqID)
	case <-ctx.Done():
		c.removePending(connID, reqID)
		return nil, ctx.Err()
	}
}

func (c *DemuxClient) removePending(connID int, reqID uint32) {
	c.mu.Lock()
	conn, ok := c.connections[connID]
	c.mu.Unlock()
	if !ok {
		return
	}
	c.mu.Lock()
	delete(conn.pending, reqID)
	c.mu.Unlock()
}

func (c *DemuxClient) onConnectionData(connID uint32, data []byte) {
	if len(data) < 4 {
		return
	}
	length := int(binary.BigEndian.Uint32(data[:4]))
	if len(data) < 4+length {
		return // fragmented
	}
	c.mu.Lock()
	conn, ok := c.connections[int(connID)]
	c.mu.Unlock()
	if !ok {
		return
	}
	msg := conn.downstreamFactory()
	if err := proto.Unmarshal(data[4:4+length], msg); err != nil {
		return
	}
	rspField := msg.ProtoReflect().Descriptor().Fields().ByName("response")
	if rspField == nil {
		return
	}
	rspVal := msg.ProtoReflect().Get(rspField)
	rspMsg, ok := rspVal.Message().Interface().(proto.Message)
	if !ok {
		return
	}
	// locate request id
	idField := rspMsg.ProtoReflect().Descriptor().Fields().ByName("request_id")
	if idField == nil {
		return
	}
	id := uint32(0)
	if v := rspMsg.ProtoReflect().Get(idField); v.IsValid() {
		id = uint32(v.Uint())
	}
	c.mu.Lock()
	ch, ok := conn.pending[id]
	if ok {
		delete(conn.pending, id)
	}
	c.mu.Unlock()
	if ok {
		select {
		case ch <- rspMsg:
		default:
		}
	}
}

// Destroy closes the socket.
func (c *DemuxClient) Destroy() {
	if c.socket != nil {
		c.socket.Destroy()
		c.socket = nil
	}
}
