// Package ubisoft is the Ubisoft Connect storefront adapter.
//
// acquisition.go drives the demux-based acquisition flow: authenticate, list owned
// games, mint download credentials and signed CDN URLs, then fetch the install
// manifest and its slices. It mirrors the vendored reference implementation.
package ubisoft

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/cmspeedau/gamedepot/internal/proto/downloadservicepb"
	"github.com/cmspeedau/gamedepot/internal/proto/ownershippb"
)

// UbisoftOwnedGame is the normalised per-game facts from the ownership init reply.
type UbisoftOwnedGame struct {
	ProductID      uint32
	DownloadID     uint32
	LatestManifest string // 40 hex upper, or ""
	BranchID       uint32
	Configuration  string
	Title          string
	SpaceID        string
	Launches       []UbisoftLaunchDocument
	Owned          bool
}

// UbisoftDownloadCredentials unlock download_service.
type UbisoftDownloadCredentials struct {
	BranchID       uint32
	Expiration     uint64
	Signature      string
	OwnershipToken string
}

// UbisoftAcquisitionClient turns an account ticket into signed CDN URLs.
type UbisoftAcquisitionClient struct {
	client          *DemuxClient
	ownershipConnID int
}

// ConnectUbisoft opens the demux socket and authenticates with a Ubi_v1 ticket.
func ConnectUbisoft(ctx context.Context, ubiTicket string, insecure bool) (*UbisoftAcquisitionClient, error) {
	client := NewDemuxClient()
	if err := client.Connect(ctx, ubiTicket, insecure); err != nil {
		return nil, err
	}
	connID, err := client.OpenConnection(ctx, ServiceOwnership, func() proto.Message {
		return &ownershippb.Downstream{}
	})
	if err != nil {
		client.Destroy()
		return nil, err
	}
	return &UbisoftAcquisitionClient{client: client, ownershipConnID: connID}, nil
}

func (a *UbisoftAcquisitionClient) Destroy() {
	if a.client != nil {
		a.client.Destroy()
	}
}

// ListOwnedGames initialises the ownership service and returns the account's games.
func (a *UbisoftAcquisitionClient) ListOwnedGames(ctx context.Context) ([]UbisoftOwnedGame, error) {
	ga := uint32(7)
	fs := false
	upstream := &ownershippb.Upstream{
		Request: &ownershippb.Req{
			InitializeReq: &ownershippb.InitializeReq{
				ProtoVersion: &ga,
				UseStaging:   &fs,
			},
		},
	}
	// fix: GetAssociations is *bool
	_ = upstream
	upstream = &ownershippb.Upstream{
		Request: &ownershippb.Req{
			InitializeReq: &ownershippb.InitializeReq{
				GetAssociations: boolPtr(true),
				ProtoVersion:    uint32Ptr(7),
				UseStaging:      boolPtr(false),
			},
		},
	}
	rsp, err := a.client.ServiceRequest(ctx, a.ownershipConnID, upstream)
	if err != nil {
		return nil, err
	}
	orr := rsp.(*ownershippb.Rsp)
	init := orr.GetInitializeRsp()
	if init == nil || !init.GetSuccess() {
		return nil, fmt.Errorf("ubisoft: ownership service refused initialisation")
	}
	og := init.GetOwnedGames()
	if og == nil {
		return nil, fmt.Errorf("ubisoft: ownership initialisation returned no owned-games container")
	}
	out := make([]UbisoftOwnedGame, 0, len(og.GetOwnedGames()))
	for _, g := range og.GetOwnedGames() {
		config := g.GetConfiguration()
		title, launches := DeriveUbisoftLaunches(config)
		manifest := strings.ToUpper(strings.TrimSpace(g.GetLatestManifest()))
		if len(manifest) != 40 {
			manifest = ""
		}
		// validate hex
		if manifest != "" {
			if _, err := hex.DecodeString(manifest); err != nil {
				manifest = ""
			}
		}
		downloadID := g.GetDownloadId()
		if downloadID == 0 {
			downloadID = g.GetProductId()
		}
		out = append(out, UbisoftOwnedGame{
			ProductID:      g.GetProductId(),
			DownloadID:     downloadID,
			LatestManifest: manifest,
			BranchID:       g.GetActiveBranchId(),
			Configuration:  config,
			Title:          title,
			SpaceID:        g.GetUbiservicesSpaceId(),
			Launches:       launches,
			Owned:          g.GetOwned(),
		})
	}
	return out, nil
}

func boolPtr(b bool) *bool       { return &b }
func uint32Ptr(v uint32) *uint32 { return &v }

// GetDownloadCredentials mints the signature + token that unlock download_service.
func (a *UbisoftAcquisitionClient) GetDownloadCredentials(ctx context.Context, productID uint32) (UbisoftDownloadCredentials, error) {
	// signOwnership
	pid := productID
	upstream := &ownershippb.Upstream{
		Request: &ownershippb.Req{
			SignOwnershipReq: &ownershippb.SignOwnershipReq{ProductId: &pid},
		},
	}
	rsp, err := a.client.ServiceRequest(ctx, a.ownershipConnID, upstream)
	if err != nil {
		return UbisoftDownloadCredentials{}, err
	}
	orr := rsp.(*ownershippb.Rsp)
	sign := orr.GetSignOwnershipRsp()
	if sign == nil || !sign.GetSuccess() || len(sign.GetSignature()) == 0 {
		return UbisoftDownloadCredentials{}, fmt.Errorf("ubisoft: refused to sign ownership for product %d", productID)
	}
	// ownershipToken
	upstream2 := &ownershippb.Upstream{
		Request: &ownershippb.Req{
			OwnershipTokenReq: &ownershippb.OwnershipTokenReq{ProductId: &pid},
		},
	}
	rsp2, err := a.client.ServiceRequest(ctx, a.ownershipConnID, upstream2)
	if err != nil {
		return UbisoftDownloadCredentials{}, err
	}
	orr2 := rsp2.(*ownershippb.Rsp)
	token := orr2.GetOwnershipTokenRsp()
	if token == nil || !token.GetSuccess() || token.GetToken() == "" {
		return UbisoftDownloadCredentials{}, fmt.Errorf("ubisoft: refused an ownership token for product %d", productID)
	}
	sig := string(sign.GetSignature())
	return UbisoftDownloadCredentials{
		BranchID:       sign.GetBranchId(),
		Expiration:     sign.GetExpiration(),
		Signature:      sig,
		OwnershipToken: token.GetToken(),
	}, nil
}

// UbisoftDownloadSession mints signed CDN URLs.
type UbisoftDownloadSession struct {
	client    *DemuxClient
	connID    int
	productID uint32
}

// OpenDownloads opens download_service and initialises it for one product.
func (a *UbisoftAcquisitionClient) OpenDownloads(ctx context.Context, productID uint32, creds UbisoftDownloadCredentials) (*UbisoftDownloadSession, error) {
	connID, err := a.client.OpenConnection(ctx, ServiceDownload, func() proto.Message {
		return &downloadservicepb.Downstream{}
	})
	if err != nil {
		return nil, err
	}
	pid := productID
	upstream := &downloadservicepb.Upstream{
		Request: &downloadservicepb.Req{
			InitializeReq: &downloadservicepb.InitializeReq{
				ProductId:      pid,
				BranchId:       creds.BranchID,
				Expiration:     creds.Expiration,
				Signature:      creds.Signature,
				OwnershipToken: creds.OwnershipToken,
				NetworkId:      "LIVE",
			},
		},
	}
	rsp, err := a.client.ServiceRequest(ctx, connID, upstream)
	if err != nil {
		return nil, err
	}
	drsp := rsp.(*downloadservicepb.Rsp)
	if init := drsp.GetInitializeRsp(); init == nil || !init.GetOk() {
		return nil, fmt.Errorf("ubisoft: download service refused initialisation for product %d", productID)
	}
	return &UbisoftDownloadSession{client: a.client, connID: connID, productID: productID}, nil
}

// MintUrls returns signed URLs for the given CDN-relative paths (positional).
func (s *UbisoftDownloadSession) MintUrls(ctx context.Context, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	upstream := &downloadservicepb.Upstream{
		Request: &downloadservicepb.Req{
			UrlReq: &downloadservicepb.UrlReq{
				UrlRequests: []*downloadservicepb.UrlReq_Request{
					{ProductId: s.productID, RelativeFilePath: paths},
				},
			},
		},
	}
	rsp, err := s.client.ServiceRequest(ctx, s.connID, upstream)
	if err != nil {
		// one retry on stall
		time.Sleep(1500 * time.Millisecond)
		rsp, err = s.client.ServiceRequest(ctx, s.connID, upstream)
		if err != nil {
			return nil, err
		}
	}
	drsp := rsp.(*downloadservicepb.Rsp)
	urlRsp := drsp.GetUrlRsp()
	if urlRsp == nil {
		return nil, fmt.Errorf("ubisoft: url response missing")
	}
	out := make([]string, len(paths))
	for i, ur := range urlRsp.GetUrlResponses() {
		if i >= len(out) {
			break
		}
		dus := ur.GetDownloadUrls()
		if len(dus) > 0 && len(dus[0].GetUrls()) > 0 {
			out[i] = dus[0].GetUrls()[0]
		}
	}
	for i := range out {
		if out[i] == "" {
			return nil, fmt.Errorf("ubisoft: no url returned for path %s", paths[i])
		}
	}
	return out, nil
}
