package rootd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// Client 是 frog 主进程连 rootd 用的客户端。
type Client struct {
	Socket string
}

// Installed 说明 rootd 的 socket 在不在（服务装了并且在运行）。
func (c *Client) Installed() bool {
	if c == nil || c.Socket == "" {
		return false
	}
	st, err := os.Stat(c.Socket)
	return err == nil && st.Mode()&os.ModeSocket != 0
}

// Do 发一个请求并等回复。run 可能跑很久，由 ctx 控制。
func (c *Client) Do(ctx context.Context, req Request) (Response, error) {
	if !c.Installed() {
		return Response{}, errors.New("这台服务器没有安装提权助手（frogbot-rootd）：请重新运行安装命令")
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return Response{}, fmt.Errorf("连不上提权助手：%w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	defer stop()
	line, _ := json.Marshal(req)
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return Response{}, fmt.Errorf("提权助手连接中断：%w", err)
	}
	out, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, fmt.Errorf("提权助手没有回复：%w", err)
	}
	var resp Response
	if err := json.Unmarshal(out, &resp); err != nil {
		return Response{}, errors.New("提权助手的回复看不懂")
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "提权助手拒绝了请求"
		}
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}
