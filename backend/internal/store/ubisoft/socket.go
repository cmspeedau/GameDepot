// Package ubisoft is the Ubisoft Connect storefront adapter.
//
// socket.go holds the byte-level client for Ubisoft's "demux" protocol: a TLS 1.2
// socket to dmx.upc.ubisoft.com:443 carrying big-endian uint32 length-prefixed
// protobuf envelopes (mg.protocol.demux). Every Ubisoft privileged operation
// (session auth, ownership, install configuration, signed download URLs) rides on
// top of this. The shapes are vendored from the YoobieRE/ubisoft-demux-node
// reference implementation; see backend/proto/ubisoft/demux.proto.
package ubisoft

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/cmspeedau/gamedepot/internal/proto/demuxpb"
)

// DemuxAPIVersion is the launcher build number announced on connect. Requests are
// refused without it; Ubisoft answers ClientOutdatedPush (and disconnects) for
// stale builds. Live check Nov 2026: Ubisoft Connect 174.0.13368 is accepted.
const DemuxAPIVersion uint32 = 13368

// DemuxHost is where the demux service listens.
const DemuxHost = "dmx.upc.ubisoft.com"

// DemuxSocket is one long-lived TLS connection to the demux service.
type DemuxSocket struct {
	host     string
	timeout  time.Duration
	insecure bool

	mu      sync.Mutex
	conn    net.Conn
	pending map[uint32]chan *demuxpb.Rsp
	nextID  uint32
	buffer  []byte
	expect  int // 0 means "read the 4-byte length next"
	closed  bool

	events DemuxEvents

	readErr chan error
	done    chan struct{}
}

// DemuxEvents are the callbacks a caller installs before connecting. All of them
// are invoked from the read loop, so they must not block.
type DemuxEvents struct {
	// ConnectionData fires when a service pushes data over an open connection.
	ConnectionData func(connectionID uint32, data []byte)
	// Push fires for any Downstream that is not a reply to a pending request.
	Push func(*demuxpb.Downstream)
	// Close fires once when the socket goes away.
	Close func(error)
}

// NewDemuxSocket constructs an unconnected socket. Call Connect before use.
func NewDemuxSocket(host string, timeout time.Duration, events DemuxEvents) *DemuxSocket {
	if host == "" {
		host = DemuxHost
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &DemuxSocket{
		host:    host,
		timeout: timeout,
		events:  events,
		pending: make(map[uint32]chan *demuxpb.Rsp),
		readErr: make(chan error, 1),
		done:    make(chan struct{}),
	}
}

// Connect opens the TLS 1.2 socket and sends the mandatory clientVersion push as
// the first message on the wire. It blocks until the handshake completes.
func (d *DemuxSocket) Connect(ctx context.Context) error {
	d.mu.Lock()
	if d.conn != nil && !d.closed {
		d.mu.Unlock()
		return nil
	}
	d.mu.Unlock()

	dialer := &net.Dialer{Timeout: d.timeout}
	tlsCfg := &tls.Config{
		ServerName:         d.host,
		MinVersion:         tls.VersionTLS12,
		MaxVersion:         tls.VersionTLS12,
		InsecureSkipVerify: d.insecure,
	}
	addr := d.host
	if !strings.Contains(addr, ":") {
		addr = addr + ":443"
	}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("ubisoft: tls handshake to %s: %w", d.host, err)
	}

	d.mu.Lock()
	d.conn = conn
	d.closed = false
	d.buffer = nil
	d.expect = 0
	d.pending = make(map[uint32]chan *demuxpb.Rsp)
	d.nextID = 1
	d.readErr = make(chan error, 1)
	d.done = make(chan struct{})
	d.mu.Unlock()

	// The clientVersion push must be the first message on the wire.
	ver := DemuxAPIVersion
	if err := d.writeUpstream(&demuxpb.Upstream{
		Push: &demuxpb.Push{ClientVersion: &demuxpb.ClientVersionPush{Version: &ver}},
	}); err != nil {
		d.Destroy()
		return fmt.Errorf("ubisoft: clientVersion push: %w", err)
	}

	go d.readLoop()
	return nil
}

// Request sends a top-level request and awaits the matching Rsp, or ctx/timeout.
func (d *DemuxSocket) Request(ctx context.Context, build func(requestID uint32) *demuxpb.Req) (*demuxpb.Rsp, error) {
	d.mu.Lock()
	if d.conn == nil || d.closed {
		d.mu.Unlock()
		return nil, fmt.Errorf("ubisoft: socket is not connected")
	}
	id := d.nextID
	d.nextID++
	ch := make(chan *demuxpb.Rsp, 1)
	d.pending[id] = ch
	d.mu.Unlock()

	req := build(id)
	if req.RequestId == nil {
		rid := id
		req.RequestId = &rid
	}
	upstream := &demuxpb.Upstream{Request: req}

	if err := d.writeUpstream(upstream); err != nil {
		d.mu.Lock()
		delete(d.pending, id)
		d.mu.Unlock()
		return nil, err
	}

	timer := time.NewTimer(d.timeout)
	defer timer.Stop()
	select {
	case rsp := <-ch:
		return rsp, nil
	case <-timer.C:
		d.mu.Lock()
		delete(d.pending, id)
		d.mu.Unlock()
		return nil, fmt.Errorf("ubisoft: demux request %d timed out", id)
	case <-ctx.Done():
		d.mu.Lock()
		delete(d.pending, id)
		d.mu.Unlock()
		return nil, ctx.Err()
	case err := <-d.readErr:
		d.mu.Lock()
		delete(d.pending, id)
		d.mu.Unlock()
		return nil, err
	}
}

// Push sends a fire-and-forget push (service payloads ride on these).
func (d *DemuxSocket) Push(ctx context.Context, push *demuxpb.Push) error {
	return d.writeUpstream(&demuxpb.Upstream{Push: push})
}

// Destroy closes the socket and fails any pending requests.
func (d *DemuxSocket) Destroy() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	conn := d.conn
	d.conn = nil
	pending := d.pending
	d.pending = make(map[uint32]chan *demuxpb.Rsp)
	d.mu.Unlock()

	if conn != nil {
		conn.Close()
	}
	for _, ch := range pending {
		// best effort: signal closure via a nil-ish sentinel by closing channel
		// (callers select on readErr too). We close channels to unblock waiters.
		// Guard against double-close.
		func() {
			defer func() { recover() }()
			close(ch)
		}()
	}
	if d.events.Close != nil {
		d.events.Close(fmt.Errorf("ubisoft: socket closed"))
	}
	select {
	case d.done <- struct{}{}:
	default:
	}
}

func (d *DemuxSocket) writeUpstream(upstream *demuxpb.Upstream) error {
	d.mu.Lock()
	conn := d.conn
	d.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("ubisoft: socket is not connected")
	}
	body, err := proto.Marshal(upstream)
	if err != nil {
		return fmt.Errorf("ubisoft: marshal upstream: %w", err)
	}
	frame := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(body)))
	copy(frame[4:], body)
	_, err = conn.Write(frame)
	if err != nil {
		return fmt.Errorf("ubisoft: write frame: %w", err)
	}
	return nil
}

func (d *DemuxSocket) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := d.conn.Read(buf)
		if n > 0 {
			d.handleData(buf[:n])
		}
		if err != nil {
			if err != io.EOF {
				d.failAll(fmt.Errorf("ubisoft: read: %w", err))
			} else {
				d.failAll(fmt.Errorf("ubisoft: socket closed"))
			}
			d.Destroy()
			return
		}
	}
}

func (d *DemuxSocket) handleData(chunk []byte) {
	d.mu.Lock()
	d.buffer = append(d.buffer, chunk...)
	for {
		if d.expect == 0 {
			if len(d.buffer) < 4 {
				break
			}
			d.expect = int(binary.BigEndian.Uint32(d.buffer[:4]))
			d.buffer = d.buffer[4:]
			if d.expect < 0 || d.expect > 16<<20 {
				d.mu.Unlock()
				d.failAll(fmt.Errorf("ubisoft: invalid frame length %d", d.expect))
				d.Destroy()
				return
			}
		}
		if len(d.buffer) < d.expect {
			break
		}
		frame := append([]byte(nil), d.buffer[:d.expect]...)
		d.buffer = d.buffer[d.expect:]
		d.expect = 0
		d.mu.Unlock()
		d.handleFrame(frame)
		d.mu.Lock()
	}
	d.mu.Unlock()
}

func (d *DemuxSocket) handleFrame(frame []byte) {
	var downstream demuxpb.Downstream
	if err := proto.Unmarshal(frame, &downstream); err != nil {
		d.failAll(fmt.Errorf("ubisoft: unmarshal downstream: %w", err))
		d.Destroy()
		return
	}
	if rsp := downstream.GetResponse(); rsp != nil {
		id := rsp.GetRequestId()
		d.mu.Lock()
		ch, ok := d.pending[id]
		if ok {
			delete(d.pending, id)
		}
		d.mu.Unlock()
		if ok {
			select {
			case ch <- rsp:
			default:
			}
			return
		}
	}
	if push := downstream.GetPush(); push != nil {
		if data := push.GetData(); data != nil {
			if cb := d.events.ConnectionData; cb != nil {
				cb(data.GetConnectionId(), data.GetData())
			}
			return
		}
	}
	if cb := d.events.Push; cb != nil {
		cb(&downstream)
	}
}

func (d *DemuxSocket) failAll(err error) {
	d.mu.Lock()
	pending := d.pending
	d.pending = make(map[uint32]chan *demuxpb.Rsp)
	d.mu.Unlock()
	for _, ch := range pending {
		func() {
			defer func() { recover() }()
			// sending on a buffered channel of size 1 is safe; if already full, skip
			select {
			case ch <- nil:
			default:
			}
		}()
	}
	// Signal waiters via readErr too.
	select {
	case d.readErr <- err:
	default:
	}
}
