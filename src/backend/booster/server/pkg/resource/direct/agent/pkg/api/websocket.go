/*
 * Copyright (c) 2021 THL A29 Limited, a Tencent company. All rights reserved
 *
 * This source code file is licensed under the MIT License, you may obtain a copy of the License at
 *
 * http://opensource.org/licenses/MIT
 *
 */

package api

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/bk_dist/booster/command"
	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/bk_dist/booster/pkg"
	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/common/blog"
	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/common/codec"
	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/server/pkg/resource/direct/agent/config"
	processManager "github.com/TencentBlueKing/bk-turbo/src/backend/booster/server/pkg/resource/direct/agent/pkg/manager"
	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/server/pkg/resource/direct/agent/pkg/types"
	"github.com/shirou/gopsutil/process"

	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/server/pkg/resource/direct/agent/pkg/common"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// define vars
var (
	TbsServerHost     = command.ProdBuildBoosterServerHost
	TestTbsServerHost = command.TestBuildBoosterServerHost

	BaseUrl = fmt.Sprintf("ws://%s:%s/api/",
		command.ProdBuildBoosterServerHost, command.ProdBuildBoosterServerPort)

	TestBaseUrl = fmt.Sprintf("ws://%s:%s/api/",
		"test.bkdistcc.ied.com", "30312")
)

// WebsocketHandler : websocket handle
type WebsocketHandler struct {
	conf          *config.ServerConfig
	ConnectionMap map[string]*net.Conn
	connLock      sync.RWMutex

	usage []string
	mgr   processManager.Manager
}

// WebsocketHandler : return new websocket handle
func NewWebsocketHandler(conf *config.ServerConfig) (*WebsocketHandler, error) {
	h := &WebsocketHandler{
		conf:          conf,
		ConnectionMap: make(map[string]*net.Conn),
	}

	err := h.init()
	if err != nil {
		blog.Infof("failed to init HttpHandle,return nil")
		return nil, err
	}

	return h, nil
}

func (h *WebsocketHandler) init() error {
	h.usage = []string{
		common.ExecuteCommand,
		common.ReportResource,
	}

	err := h.initConnection()
	if err != nil {
		return err
	}

	err = h.initMgr()
	if err != nil {
		blog.Errorf("failed to init manager with err: %v", err)
		return err
	}

	return nil
}

func (h *WebsocketHandler) initConnection() error {
	fmt.Printf("connecting server......")
	for _, usage := range h.usage {
		url := TestBaseUrl + usage
		conn, _, _, err := ws.Dial(context.Background(), url)
		if err != nil {
			blog.Errorf("failed to create execute connection with error:(%v)", err)
			return err
		}
		fmt.Printf("%s connection complete", usage)

		h.connLock.Lock()
		h.ConnectionMap[usage] = &conn
		h.connLock.Unlock()
	}

	fmt.Printf("server connected")
	return nil
}

func (h *WebsocketHandler) initMgr() error {
	var err error
	h.mgr, err = processManager.NewManager(h.conf, nil, h.ConnectionMap)
	if err != nil {
		blog.Errorf("init process manager failed: %v", err)
		return err
	}

	go h.mgr.Run()
	return nil
}

func (h *WebsocketHandler) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	go sysSignalHandler(cancel, h)

	for usage, conn := range h.ConnectionMap {
		if conn != nil {
			go h.handle(ctx, usage)
		}
	}

	connCheckTick := time.NewTicker(types.AgentConnCheckTime)
	defer connCheckTick.Stop()

	for {
		select {
		case <-ctx.Done():
			break
		case <-connCheckTick.C:
			h.connCheck(ctx)
		}

	}

	return nil
}

func (h *WebsocketHandler) connCheck(ctx context.Context) {
	blog.Infof("start checking conn")

	needReconnect := false
	for usage, conn := range h.ConnectionMap {
		// check if conn is closed
		if err := (*conn).SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			blog.Errorf("% connection closed: %s, ready to reconnect", usage, err)
			needReconnect = true
			break
		}
	}

	if needReconnect {
		blog.Infof("ready to reconnect server")
		h.initConnection()
	}

}

func (h *WebsocketHandler) handle(ctx context.Context, usage string) {
	switch usage {
	case common.ReportResource:
		h.handleReportResource(ctx, usage)
	case common.ExecuteCommand:
		h.handleExecuteCommand(ctx, usage)
	default:
		blog.Errorf("unknown conn usage : %s", usage)
	}

}

func (h *WebsocketHandler) handleReportResource(ctx context.Context, usage string) {
	resourceReportTick := time.NewTicker(types.AgentReportIntervalTime)
	defer resourceReportTick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-resourceReportTick.C:
			conn := h.getConn(usage)
			if conn == nil {
				blog.Errorf("%s usage conn is not ready", usage)
				continue
			}

			h.mgr.ReportResource(conn)

		}

	}
}

func (h *WebsocketHandler) handleExecuteCommand(ctx context.Context, usage string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch := make(chan *types.NotifyAgentData, 1)
	go h.listenExecuteCommand(ctx, usage, ch)

	for {
		select {
		case <-ctx.Done():
			return
		case cmd := <-ch:
			h.mgr.ExecuteCommand(cmd)

			if cmd.CmdType == types.CmdLaunch {
				conn := h.getConn(common.ReportResource)
				if conn != nil {
					go h.mgr.ReportResource(conn)
				}
			}
		}
	}
}

func (h *WebsocketHandler) clean() {
	// close conn
	for usage, conn := range h.ConnectionMap {
		blog.Infof("agent quit , %s conn ready closed", usage)
		(*conn).Close()
	}

	// release resouce
	err := h.mgr.Clean()
	if err != nil {
		blog.Errorf("clean failed before agent quit: %v", err)
	}
}

func (h *WebsocketHandler) listenExecuteCommand(ctx context.Context, usage string, ch chan<- *types.NotifyAgentData) {
	for {
		conn := h.getConn(usage)
		if conn == nil {
			blog.Errorf("%s usage conn is not ready", usage)
			time.Sleep(2 * time.Second)
			continue
		}

		select {
		case <-ctx.Done():
			return
		default:
			data, op, err := wsutil.ReadServerData(*conn)

			if op == ws.OpClose || err == io.EOF {
				blog.Errorf("execute handler: conn between (%s) is closed", (*conn).RemoteAddr().(*net.TCPAddr).IP)
				return
			}
			if op == ws.OpContinuation {
				//blog.Errorf("drm: executeHandler quit with :%v", op)
				time.Sleep(2 * time.Second)
				continue
			}
			if err != nil {
				blog.Errorf("execute command : get server data failed with err: %v", err)
				continue
			}

			var cmd types.NotifyAgentData
			if err = codec.DecJSON(data, &cmd); err != nil {
				blog.Errorf("execute command: decode failed with err:%v", err)
				continue
			}

			ch <- &cmd
		}
	}
}

func (h *WebsocketHandler) getConn(usage string) *net.Conn {
	h.connLock.RLock()
	defer h.connLock.RUnlock()

	conn, ok := h.ConnectionMap[usage]
	if !ok {
		return nil
	}
	return conn
}

func sysSignalHandler(cancel context.CancelFunc, h *WebsocketHandler) {
	interrupt := make(chan os.Signal)
	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-interrupt:
		blog.Warnf("get system signal %s, going to exit", sig.String())

		cancel()
		// TODO : release local resource, kill remote execute
		h.clean()

		p, err := process.NewProcess(int32(os.Getpid()))
		if err == nil {
			// kill children
			pkg.KillChildren(p)
		}
		blog.CloseLogs()

		// catch control-C and should return code 130(128+0x2)
		if sig == syscall.SIGINT {
			os.Exit(130)
		}

		// catch kill and should return code 143(128+0xf)
		if sig == syscall.SIGTERM {
			os.Exit(143)
		}

		os.Exit(1)
	}
}
