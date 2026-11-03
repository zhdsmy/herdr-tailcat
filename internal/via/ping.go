package via

import (
	"bufio"
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/zhdsmy/herdr-tailcat/internal/daemon"
	"github.com/zhdsmy/herdr-tailcat/internal/peer"
	"github.com/zhdsmy/herdr-tailcat/internal/proto"
)

func Ping(ctx context.Context, home, id string) (*proto.Info, time.Duration, error) {
	start := time.Now()
	c, e := connectDaemon(ctx, filepath.Join(home, "profiles", id), id)
	if e != nil {
		return nil, 0, e
	}
	defer c.Close()
	if _, e = peer.UID(c); e != nil {
		return nil, 0, e
	}
	until, _ := ctx.Deadline()
	c.SetDeadline(until)
	if e = proto.Write(c, proto.LocalRequest{Request: proto.Request{Version: proto.Version, Op: "ping"}, DeadlineMS: until.UnixMilli()}); e != nil {
		return nil, 0, daemon.ErrNoAnswer
	}
	var r proto.Response
	if e = proto.Read(bufio.NewReaderSize(c, proto.MaxLine), &r); e != nil {
		return nil, 0, daemon.ErrNoAnswer
	}
	if !r.OK {
		return nil, 0, errors.New(r.Error)
	}
	if r.Version != proto.Version || r.Server == nil {
		return nil, 0, errors.New("tailcat: invalid server handshake")
	}
	return r.Server, time.Since(start), nil
}
