// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package turnrs

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestProtoRoundTrips(t *testing.T) {
	t.Parallel()

	allocatedPort := int32(49152)
	id := &Identifier{
		Source:    "192.0.2.10:5000",
		External:  "198.51.100.2:3478",
		Interface: "eth0",
		Transport: Transport_TRANSPORT_UDP,
	}

	messages := []proto.Message{
		&Interface{
			Address:   "10.0.0.5:3478",
			External:  "203.0.113.9:3478",
			Transport: Transport_TRANSPORT_TCP,
		},
		id,
		&BindAddress{
			Key:   16384,
			Value: id,
		},
		&TurnServerInfo{
			Software: "turn-rs/0.3",
			Uptime:   3600,
			Interfaces: []*Interface{
				{Address: "0.0.0.0:3478", External: "203.0.113.1:3478", Transport: Transport_TRANSPORT_UDP},
			},
			PortCapacity:  16384,
			PortAllocated: 12,
		},
		&TurnSession{
			Username:      "alice",
			AllocatedPort: &allocatedPort,
			Expires:       600,
			Permissions:   []*BindAddress{{Key: 5001, Value: id}},
			Channels:      []*BindAddress{{Key: 0x4000, Value: id}},
		},
		&TurnSessionStatistics{
			ReceivedBytes: 1024,
			SendBytes:     2048,
			ReceivedPkts:  10,
			SendPkts:      20,
			ErrorPkts:     1,
		},
		&TurnAllocatedEvent{Id: id, Username: "alice", Port: 49152},
		&TurnChannelBindEvent{Id: id, Username: "alice", Channel: 0x4001},
		&TurnCreatePermissionEvent{Id: id, Username: "alice", Ports: []int32{5000, 5001}},
		&TurnRefreshEvent{Id: id, Username: "alice", Lifetime: 600},
		&TurnDestroyEvent{Id: id, Username: "alice"},
		&GetTurnPasswordRequest{
			Id:        id,
			Algorithm: PasswordAlgorithm_PASSWORD_ALGORITHM_SHA256,
			Username:  "alice",
			Realm:     "example.com",
		},
		&GetTurnPasswordResponse{Password: []byte("digest-bytes")},
	}

	for _, orig := range messages {
		wire, err := proto.Marshal(orig)
		if err != nil {
			t.Fatalf("proto.Marshal(%T) failed: %v", orig, err)
		}
		got := orig.ProtoReflect().New().Interface()
		if err := proto.Unmarshal(wire, got); err != nil {
			t.Fatalf("proto.Unmarshal(%T) failed: %v", orig, err)
		}
		if !proto.Equal(orig, got) {
			t.Fatalf("round-trip mismatch for %T: got %v, want %v", orig, got, orig)
		}
	}

	if got := Transport_TRANSPORT_UDP.String(); got != "TRANSPORT_UDP" {
		t.Fatalf("Transport.String() = %q, want TRANSPORT_UDP", got)
	}
	if got := PasswordAlgorithm_PASSWORD_ALGORITHM_MD5.String(); got != "PASSWORD_ALGORITHM_MD5" {
		t.Fatalf("PasswordAlgorithm.String() = %q, want PASSWORD_ALGORITHM_MD5", got)
	}
}

type recordingHooksServer struct {
	UnimplementedTurnHooksServiceServer
	password []byte
	gotReq   *GetTurnPasswordRequest
	allocated *TurnAllocatedEvent
	bound     *TurnChannelBindEvent
	perm      *TurnCreatePermissionEvent
	refresh   *TurnRefreshEvent
	destroy   *TurnDestroyEvent
}

func (s *recordingHooksServer) GetPassword(_ context.Context, req *GetTurnPasswordRequest) (*GetTurnPasswordResponse, error) {
	s.gotReq = proto.Clone(req).(*GetTurnPasswordRequest)
	return &GetTurnPasswordResponse{Password: s.password}, nil
}

func (s *recordingHooksServer) OnAllocatedEvent(_ context.Context, ev *TurnAllocatedEvent) (*emptypb.Empty, error) {
	s.allocated = proto.Clone(ev).(*TurnAllocatedEvent)
	return &emptypb.Empty{}, nil
}

func (s *recordingHooksServer) OnChannelBindEvent(_ context.Context, ev *TurnChannelBindEvent) (*emptypb.Empty, error) {
	s.bound = proto.Clone(ev).(*TurnChannelBindEvent)
	return &emptypb.Empty{}, nil
}

func (s *recordingHooksServer) OnCreatePermissionEvent(_ context.Context, ev *TurnCreatePermissionEvent) (*emptypb.Empty, error) {
	s.perm = proto.Clone(ev).(*TurnCreatePermissionEvent)
	return &emptypb.Empty{}, nil
}

func (s *recordingHooksServer) OnRefreshEvent(_ context.Context, ev *TurnRefreshEvent) (*emptypb.Empty, error) {
	s.refresh = proto.Clone(ev).(*TurnRefreshEvent)
	return &emptypb.Empty{}, nil
}

func (s *recordingHooksServer) OnDestroyEvent(_ context.Context, ev *TurnDestroyEvent) (*emptypb.Empty, error) {
	s.destroy = proto.Clone(ev).(*TurnDestroyEvent)
	return &emptypb.Empty{}, nil
}

type stubTurnServiceServer struct {
	UnimplementedTurnServiceServer
	destroyedID *Identifier
}

func (s *stubTurnServiceServer) GetInfo(context.Context, *emptypb.Empty) (*TurnServerInfo, error) {
	return &TurnServerInfo{
		Software:      "turn-rs",
		Uptime:        42,
		PortCapacity:  1000,
		PortAllocated: 5,
	}, nil
}

func (s *stubTurnServiceServer) GetSession(_ context.Context, id *Identifier) (*TurnSession, error) {
	port := int32(49200)
	return &TurnSession{
		Username:      id.GetSource(),
		AllocatedPort: &port,
		Expires:       300,
	}, nil
}

func (s *stubTurnServiceServer) GetSessionStatistics(_ context.Context, _ *Identifier) (*TurnSessionStatistics, error) {
	return &TurnSessionStatistics{
		ReceivedBytes: 4096,
		SendBytes:     8192,
		ReceivedPkts:  40,
		SendPkts:      80,
		ErrorPkts:     2,
	}, nil
}

func (s *stubTurnServiceServer) DestroySession(_ context.Context, id *Identifier) (*emptypb.Empty, error) {
	s.destroyedID = proto.Clone(id).(*Identifier)
	return &emptypb.Empty{}, nil
}

func startLoopbackGRPC(t *testing.T, register func(*grpc.Server)) grpc.ClientConnInterface {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen failed: %v", err)
	}
	srv := grpc.NewServer()
	register(srv)
	go func() {
		_ = srv.Serve(lis)
	}()
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient failed: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	return conn
}

func TestLoopbackGRPC_HooksAndTurnServices(t *testing.T) {
	t.Parallel()

	hooksImpl := &recordingHooksServer{password: []byte{0xde, 0xad, 0xbe, 0xef}}
	turnImpl := &stubTurnServiceServer{}
	conn := startLoopbackGRPC(t, func(srv *grpc.Server) {
		RegisterTurnHooksServiceServer(srv, hooksImpl)
		RegisterTurnServiceServer(srv, turnImpl)
	})

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	id := &Identifier{
		Source:    "192.0.2.15:44000",
		External:  "198.51.100.8:3478",
		Interface: "ens4",
		Transport: Transport_TRANSPORT_UDP,
	}

	hooksClient := NewTurnHooksServiceClient(conn)
	pwReq := &GetTurnPasswordRequest{
		Id:        id,
		Algorithm: PasswordAlgorithm_PASSWORD_ALGORITHM_MD5,
		Username:  "u1",
		Realm:     "relay.internal",
	}
	pwResp, err := hooksClient.GetPassword(ctx, pwReq)
	if err != nil {
		t.Fatalf("GetPassword failed: %v", err)
	}
	if string(pwResp.GetPassword()) != string(hooksImpl.password) {
		t.Fatalf("GetPassword = %x, want %x", pwResp.GetPassword(), hooksImpl.password)
	}
	if !proto.Equal(hooksImpl.gotReq, pwReq) {
		t.Fatalf("server received %v, want %v", hooksImpl.gotReq, pwReq)
	}

	allocEv := &TurnAllocatedEvent{Id: id, Username: "u1", Port: 49200}
	if _, err := hooksClient.OnAllocatedEvent(ctx, allocEv); err != nil {
		t.Fatalf("OnAllocatedEvent failed: %v", err)
	}
	if !proto.Equal(hooksImpl.allocated, allocEv) {
		t.Fatalf("allocated event mismatch: %v vs %v", hooksImpl.allocated, allocEv)
	}

	bindEv := &TurnChannelBindEvent{Id: id, Username: "u1", Channel: 0x4002}
	if _, err := hooksClient.OnChannelBindEvent(ctx, bindEv); err != nil {
		t.Fatalf("OnChannelBindEvent failed: %v", err)
	}
	if !proto.Equal(hooksImpl.bound, bindEv) {
		t.Fatalf("channel bind event mismatch: %v vs %v", hooksImpl.bound, bindEv)
	}

	permEv := &TurnCreatePermissionEvent{Id: id, Username: "u1", Ports: []int32{10000, 10001}}
	if _, err := hooksClient.OnCreatePermissionEvent(ctx, permEv); err != nil {
		t.Fatalf("OnCreatePermissionEvent failed: %v", err)
	}
	if !proto.Equal(hooksImpl.perm, permEv) {
		t.Fatalf("permission event mismatch: %v vs %v", hooksImpl.perm, permEv)
	}

	refreshEv := &TurnRefreshEvent{Id: id, Username: "u1", Lifetime: 900}
	if _, err := hooksClient.OnRefreshEvent(ctx, refreshEv); err != nil {
		t.Fatalf("OnRefreshEvent failed: %v", err)
	}
	if !proto.Equal(hooksImpl.refresh, refreshEv) {
		t.Fatalf("refresh event mismatch: %v vs %v", hooksImpl.refresh, refreshEv)
	}

	destroyEv := &TurnDestroyEvent{Id: id, Username: "u1"}
	if _, err := hooksClient.OnDestroyEvent(ctx, destroyEv); err != nil {
		t.Fatalf("OnDestroyEvent failed: %v", err)
	}
	if !proto.Equal(hooksImpl.destroy, destroyEv) {
		t.Fatalf("destroy event mismatch: %v vs %v", hooksImpl.destroy, destroyEv)
	}

	turnClient := NewTurnServiceClient(conn)
	info, err := turnClient.GetInfo(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("GetInfo failed: %v", err)
	}
	if info.GetSoftware() != "turn-rs" || info.GetPortAllocated() != 5 {
		t.Fatalf("unexpected TurnServerInfo: %v", info)
	}

	sess, err := turnClient.GetSession(ctx, id)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if sess.GetAllocatedPort() != 49200 || sess.GetUsername() != id.GetSource() {
		t.Fatalf("unexpected TurnSession: %v", sess)
	}

	stats, err := turnClient.GetSessionStatistics(ctx, id)
	if err != nil {
		t.Fatalf("GetSessionStatistics failed: %v", err)
	}
	if stats.GetReceivedBytes() != 4096 || stats.GetSendPkts() != 80 {
		t.Fatalf("unexpected TurnSessionStatistics: %v", stats)
	}

	if _, err := turnClient.DestroySession(ctx, id); err != nil {
		t.Fatalf("DestroySession failed: %v", err)
	}
	if !proto.Equal(turnImpl.destroyedID, id) {
		t.Fatalf("DestroySession received %v, want %v", turnImpl.destroyedID, id)
	}
}

func TestUnimplementedServersReturnUnimplementedStatus(t *testing.T) {
	t.Parallel()

	conn := startLoopbackGRPC(t, func(srv *grpc.Server) {
		RegisterTurnHooksServiceServer(srv, UnimplementedTurnHooksServiceServer{})
		RegisterTurnServiceServer(srv, UnimplementedTurnServiceServer{})
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	hooksClient := NewTurnHooksServiceClient(conn)
	if _, err := hooksClient.GetPassword(ctx, &GetTurnPasswordRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("GetPassword status = %v, want Unimplemented", status.Code(err))
	}
	if _, err := hooksClient.OnAllocatedEvent(ctx, &TurnAllocatedEvent{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("OnAllocatedEvent status = %v, want Unimplemented", status.Code(err))
	}

	turnClient := NewTurnServiceClient(conn)
	if _, err := turnClient.GetInfo(ctx, &emptypb.Empty{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("GetInfo status = %v, want Unimplemented", status.Code(err))
	}
	if _, err := turnClient.DestroySession(ctx, &Identifier{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("DestroySession status = %v, want Unimplemented", status.Code(err))
	}
}
