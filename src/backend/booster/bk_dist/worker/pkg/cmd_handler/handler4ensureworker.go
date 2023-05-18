/*
 * Copyright (c) 2021 THL A29 Limited, a Tencent company. All rights reserved
 *
 * This source code file is licensed under the MIT License, you may obtain a copy of the License at
 *
 * http://opensource.org/licenses/MIT
 *
 */

package pbcmd

import (
	"time"

	dcConfig "github.com/TencentBlueKing/bk-turbo/src/backend/booster/bk_dist/common/config"
	dcProtocol "github.com/TencentBlueKing/bk-turbo/src/backend/booster/bk_dist/common/protocol"
	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/bk_dist/worker/pkg/protocol"
	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/common/blog"
)

var ()

// Handle4SyncTime handler for dispatch task request
type Handle4EnsureWorker struct {
}

// NewHandle4SyncTime return Handle4SyncTime
func NewHandle4EnsureWorker() *Handle4EnsureWorker {
	return &Handle4EnsureWorker{}
}

// ReceiveBody receive body for this cmd
func (h *Handle4EnsureWorker) ReceiveBody(client *protocol.TCPClient,
	head *dcProtocol.PBHead,
	basedir string,
	c chan<- string) (interface{}, error) {

	blog.Infof("not implement now")
	return nil, nil
}

// Handle to handle this cmd
func (h *Handle4EnsureWorker) Handle(client *protocol.TCPClient,
	head *dcProtocol.PBHead,
	body interface{},
	receivedtime time.Time,
	basedir string,
	cmdreplacerules []dcConfig.CmdReplaceRule,
	ensureRsp string) error {

	go h.handle(client, receivedtime, ensureRsp)
	return nil
}

// Handle to handle this cmd
func (h *Handle4EnsureWorker) handle(client *protocol.TCPClient,
	receivedtime time.Time, ensureRsp string) error {
	blog.Infof("handle to sync time")
	defer func() {
		blog.Infof("handle out for sync time")
		client.Close()
	}()

	// encode response to messages
	messages, err := protocol.EncodeEnsureWorkerReq(ensureRsp)
	if err != nil {
		blog.Errorf("failed to encode rsp to messages for error:%v", err)
	}
	blog.Infof("succeed to encode dispatch response to messages")

	// send response
	err = protocol.SendMessages(client, &messages)
	if err != nil {
		blog.Errorf("failed to send messages for error:%v", err)
	}
	blog.Infof("succeed to send messages")

	return nil
}
