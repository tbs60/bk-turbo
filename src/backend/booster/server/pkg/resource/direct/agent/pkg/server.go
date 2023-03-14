/*
 * Copyright (c) 2021 THL A29 Limited, a Tencent company. All rights reserved
 *
 * This source code file is licensed under the MIT License, you may obtain a copy of the License at
 *
 * http://opensource.org/licenses/MIT
 *
 */

package pkg

import (
	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/common"
	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/common/blog"
	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/server/pkg/resource/direct/agent/config"
	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/server/pkg/resource/direct/agent/pkg/api"
	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/server/pkg/resource/direct/agent/pkg/types"
)

// FbAgent : fast build agent
type FbAgent struct {
	conf *config.ServerConfig
	//httpServer *httpserver.HTTPServer
	handle *api.HTTPHandle

	websocketHandler *api.WebsocketHandler
}

// NewFbAgent : return fast build agent object
func NewFbAgent(conf *config.ServerConfig) (*FbAgent, error) {
	s := &FbAgent{conf: conf}

	var err error
	s.websocketHandler, err = api.NewWebsocketHandler(conf)
	if err != nil {
		blog.Errorf("new Fbagent failed, %v", err)
		return nil, err
	}
	// Http server
	/*kkk s.httpServer = httpserver.NewHTTPServer(s.conf.Port, s.conf.Address, "")
	if s.conf.ServerCert.IsSSL {
		s.httpServer.SetSSL(
			s.conf.ServerCert.CAFile, s.conf.ServerCert.CertFile, s.conf.ServerCert.KeyFile, s.conf.ServerCert.CertPwd)
	}*/

	s.initConfig()

	return s, nil
}

// init same inner config by ServerConfig
func (server *FbAgent) initConfig() {
	types.DistCCDaemonCPUPerUnit = server.conf.BcsCPUPerInstance
}

// Start : start listen
func (server *FbAgent) Start() error {
	// kkk
	/*var err error
	server.handle, err = api.NewHTTPHandle(server.conf)
	if server.handle == nil || err != nil {
		return types.ErrInitHTTPHandle
	}

	server.httpServer.RegisterWebServer(api.PathV1, nil, server.handle.GetActions())
	return server.httpServer.ListenAndServe()*/

	return server.websocketHandler.Start()
}

// Run brings up the server
func Run(conf *config.ServerConfig) error {
	if err := common.SavePid(conf.ProcessConfig); err != nil {
		blog.Errorf("save pid failed: %v", err)
		return err
	}

	server, err := NewFbAgent(conf)
	if err != nil {
		blog.Errorf("init distCC server failed: %v", err)
		return err
	}

	return server.Start()
}
