package ubisoft

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/cmspeedau/gamedepot/internal/proto/demuxpb"
)

func testTLSCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestDemuxSocketRoundTrip(t *testing.T) {
	cert := testTLSCert(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		readFrame := func() []byte {
			hdr := make([]byte, 4)
			if _, err := io.ReadFull(conn, hdr); err != nil {
				return nil
			}
			n := int(binary.BigEndian.Uint32(hdr))
			b := make([]byte, n)
			if _, err := io.ReadFull(conn, b); err != nil {
				return nil
			}
			return b
		}
		// clientVersion push
		_ = readFrame()
		// request
		reqFrame := readFrame()
		var up demuxpb.Upstream
		_ = proto.Unmarshal(reqFrame, &up)
		rid := up.GetRequest().GetRequestId()
		rsp := &demuxpb.Rsp{RequestId: &rid}
		ds := &demuxpb.Downstream{Response: rsp}
		body, _ := proto.Marshal(ds)
		frame := make([]byte, 4+len(body))
		binary.BigEndian.PutUint32(frame[:4], uint32(len(body)))
		copy(frame[4:], body)
		conn.Write(frame)
	}()

	addr := ln.Addr().String()
	sock := NewDemuxSocket(addr, 5*time.Second, DemuxEvents{})
	sock.insecure = true
	if err := sock.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sock.Destroy()

	rsp, err := sock.Request(context.Background(), func(id uint32) *demuxpb.Req {
		rid := id
		return &demuxpb.Req{RequestId: &rid}
	})
	if err != nil {
		t.Fatal(err)
	}
	if rsp.GetRequestId() == 0 {
		t.Fatal("response carried no request id")
	}
}

func TestDemuxSocketPushDelivered(t *testing.T) {
	cert := testTLSCert(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	pushCh := make(chan *demuxpb.Downstream, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		readFrame := func() []byte {
			hdr := make([]byte, 4)
			if _, err := io.ReadFull(conn, hdr); err != nil {
				return nil
			}
			n := int(binary.BigEndian.Uint32(hdr))
			b := make([]byte, n)
			if _, err := io.ReadFull(conn, b); err != nil {
				return nil
			}
			return b
		}
		_ = readFrame() // clientVersion push
		// send a push downstream
		push := &demuxpb.Push{KeepAlive: &demuxpb.KeepAlivePush{}}
		ds := &demuxpb.Downstream{Push: push}
		body, _ := proto.Marshal(ds)
		frame := make([]byte, 4+len(body))
		binary.BigEndian.PutUint32(frame[:4], uint32(len(body)))
		copy(frame[4:], body)
		conn.Write(frame)
		time.Sleep(100 * time.Millisecond)
	}()

	addr := ln.Addr().String()
	gotPush := make(chan struct{}, 1)
	sock := NewDemuxSocket(addr, 5*time.Second, DemuxEvents{
		Push: func(d *demuxpb.Downstream) { gotPush <- struct{}{} },
	})
	sock.insecure = true
	if err := sock.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sock.Destroy()

	select {
	case <-gotPush:
	case <-time.After(2 * time.Second):
		t.Fatal("push not delivered")
	}
	_ = pushCh
	_ = fmt.Sprintf
}
