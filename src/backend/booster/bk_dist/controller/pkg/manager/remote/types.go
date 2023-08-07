/*
 * Copyright (c) 2021 THL A29 Limited, a Tencent company. All rights reserved
 *
 * This source code file is licensed under the MIT License, you may obtain a copy of the License at
 *
 * http://opensource.org/licenses/MIT
 *
 */

package remote

import (
	"sync"

	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/bk_dist/common/protocol"
	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/bk_dist/common/sdk"
	dcSDK "github.com/TencentBlueKing/bk-turbo/src/backend/booster/bk_dist/common/sdk"
	dcSyscall "github.com/TencentBlueKing/bk-turbo/src/backend/booster/bk_dist/common/syscall"
	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/bk_dist/worker/pkg/client"
)

func newFileMessageBank() *fileMessageBank {
	return &fileMessageBank{
		bank: make(map[string]*messageItem),
	}
}

type fileMessageBank struct {
	sync.RWMutex
	bank map[string]*messageItem
}

func (fmb *fileMessageBank) ensure(sender *dcSDK.BKDistFileSender, sandbox *dcSyscall.Sandbox) error {
	fmb.Lock()
	defer fmb.Unlock()

	for _, f := range sender.Files {
		if _, ok := fmb.bank[f.UniqueKey()]; ok {
			continue
		}

		msg, err := client.EncodeSendFileReq(sender, sandbox)
		if err != nil {
			return err
		}
		fmb.bank[f.UniqueKey()] = &messageItem{
			desc: f,
			msg:  msg,
		}
	}

	return nil
}

func (fmb *fileMessageBank) get(f dcSDK.FileDesc) []protocol.Message {
	fmb.RLock()
	defer fmb.RUnlock()

	if msg, ok := fmb.bank[f.UniqueKey()]; ok {
		return msg.msg
	}

	return nil
}

func (fmb *fileMessageBank) clean(f dcSDK.FileDesc) {
	fmb.Lock()
	defer fmb.Unlock()

	if msg, ok := fmb.bank[f.UniqueKey()]; ok {
		msg.msg = nil
	}
}

type messageItem struct {
	desc sdk.FileDesc
	msg  []protocol.Message
}

func getCheckCmd(os string) *dcSDK.BKDistCommand {
	cmd := &dcSDK.BKDistCommand{}
	if os == "linux" {
		cmd.Commands = append(cmd.Commands, dcSDK.BKCommand{
			WorkDir: "/root",
			ExePath: "",
			ExeName: "env",
			Params:  []string{},
		})
	} else if os == "windows" {
		cmd.Commands = append(cmd.Commands, dcSDK.BKCommand{
			WorkDir: "C:\\",
			ExePath: "",
			ExeName: "echo",
			Params:  []string{"%COMPUTERNAME%"},
		})
	} else {
		return nil
	}
	return cmd
}
