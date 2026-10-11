package driver

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// blockingHealth is a health server whose Check never returns until released, standing
// in for a CSI RPC stuck on a hung mount or an unresponsive TrueNAS.
type blockingHealth struct {
	*health.Server
	entered chan struct{}
	release chan struct{}
}

func (b *blockingHealth) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	close(b.entered)
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return &healthpb.HealthCheckResponse{}, nil
}

// GracefulStop waits for every in-flight RPC, so one hung RPC would block shutdown
// forever and kubelet would SIGKILL the pod mid-operation anyway. stopGRPCServer must
// drain for at most the given timeout and then force-close.
func TestStopGRPCServerIsBoundedByTimeout(t *testing.T) {
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	h := &blockingHealth{Server: health.NewServer(), entered: make(chan struct{}), release: make(chan struct{})}
	healthpb.RegisterHealthServer(srv, h)
	go func() { _ = srv.Serve(lis) }()
	defer close(h.release)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	go func() {
		_, _ = healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{})
	}()
	<-h.entered

	start := time.Now()
	stopGRPCServer(srv, 200*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("stopGRPCServer() took %v with a hung RPC; want ~200ms then force stop", elapsed)
	}
}

func TestStopGRPCServerDrainsIdleServerImmediately(t *testing.T) {
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	go func() { _ = srv.Serve(lis) }()

	start := time.Now()
	stopGRPCServer(srv, 10*time.Second)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("stopGRPCServer() on an idle server took %v", elapsed)
	}
}
