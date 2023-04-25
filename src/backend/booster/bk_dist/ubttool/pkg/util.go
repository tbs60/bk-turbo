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
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"

	dcFile "github.com/TencentBlueKing/bk-turbo/src/backend/booster/bk_dist/common/file"
	dcUtil "github.com/TencentBlueKing/bk-turbo/src/backend/booster/bk_dist/common/util"
	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/bk_dist/ubttool/common"
	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/common/blog"
	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/common/codec"
)

const (
	historyFile = "bk_ubt_tool_actions_hitory.json"
)

func defaultCPULimit(custom int) int {
	if custom > 0 {
		return custom
	}
	return runtime.NumCPU() - 2
}

func resolveActionJSON(filename string) (*common.UE4Action, error) {
	blog.Debugf("resolve action json file %s", filename)

	data, err := ioutil.ReadFile(filename)
	if err != nil {
		blog.Errorf("failed to read tool chain json file %s with error %v", filename, err)
		return nil, err
	}

	var t common.UE4Action
	if err = codec.DecJSON(data, &t); err != nil {
		blog.Errorf("failed to decode json content[%s] failed: %v", string(data), err)
		return nil, err
	}

	_ = t.UniqDeps()
	_ = t.GenFolloweIndex()

	// for debug
	blog.Debugf("%+v", t)

	return &t, nil
}

func loadHistory() (*common.UE4Action, error) {
	dir := dcUtil.GetHistoryDir()
	if dcFile.Stat(dir).Exist() {
		filename := filepath.Join(dir, historyFile)
		if dcFile.Stat(filename).Exist() {
			return resolveActionJSON(filename)
		} else {
			return nil, ErrorHistoryFileNotExisted
		}
	} else {
		return nil, ErrorHistoryDirNotExisted
	}
}

func saveToHistory(current *common.UE4Action) error {
	if current == nil {
		return nil
	}

	// 如果有多个tool同时修改，有可能会写乱数据，但这个概率很小，先忽略
	dir := dcUtil.GetHistoryDir()
	if !dcFile.Stat(dir).Exist() {
		return ErrorHistoryDirNotExisted
	}

	var history *common.UE4Action
	filename := filepath.Join(dir, historyFile)
	history, _ = loadHistory()

	if history == nil {
		for i := range current.Actions {
			duration := current.Actions[i].EndTime.Unix() - current.Actions[i].StartTime.Unix()
			current.Actions[i].HistoryDuration = []int64{duration}
		}
		history = current
	} else {
		newActions := make([]common.Action, 0, 0)
		for _, c := range current.Actions {
			found := false
			for j, h := range history.Actions {
				if c.Equal(h) {
					if len(h.HistoryDuration) >= 10 {
						history.Actions[j].HistoryDuration = history.Actions[j].HistoryDuration[len(h.HistoryDuration)-9:]
					}
					duration := c.EndTime.Unix() - c.StartTime.Unix()
					history.Actions[j].HistoryDuration = append(history.Actions[j].HistoryDuration, duration)
					found = true
					break
				}
			}
			if !found {
				duration := c.EndTime.Unix() - c.StartTime.Unix()
				c.HistoryDuration = []int64{duration}
				newActions = append(newActions, c)
			}
		}

		history.Actions = append(history.Actions, newActions...)
	}

	sort.Sort(ByDuration(history.Actions))

	// save to file
	temp := filename + ".bak"
	var data []byte
	codec.EncJSON(history, &data)
	err := ioutil.WriteFile(temp, data, os.ModePerm)
	if err != nil {
		blog.Infof("failed to save history data to %s with error:%v", temp, err)
		return err
	}
	blog.Infof("succeed to save history data to %s ", temp)

	// rm and rename
	os.Remove(filename)
	os.Rename(temp, filename)
	blog.Infof("finished to rename %s to %s ", temp, filename)

	return nil
}

func remove(s []string, i int) []string {
	s[len(s)-1], s[i] = s[i], s[len(s)-1]
	return s[:len(s)-1]
}

func removeaction(s []common.Action, i int) []common.Action {
	s[len(s)-1], s[i] = s[i], s[len(s)-1]
	return s[:len(s)-1]
}

type count32 int32

func (c *count32) inc() int32 {
	return atomic.AddInt32((*int32)(c), 1)
}

func (c *count32) get() int32 {
	return atomic.LoadInt32((*int32)(c))
}

// replace which next is not in nextExcludes
func replaceWithNextExclude(s string, old byte, new string, nextExcludes []byte) string {
	if s == "" {
		return ""
	}

	if len(nextExcludes) == 0 {
		return strings.Replace(s, string(old), new, -1)
	}

	targetslice := make([]byte, 0, 0)
	nextexclude := false
	totallen := len(s)
	for i := 0; i < totallen; i++ {
		c := s[i]
		if c == old {
			nextexclude = false
			if i < totallen-1 {
				next := s[i+1]
				for _, e := range nextExcludes {
					if next == e {
						nextexclude = true
						break
					}
				}
			}
			if nextexclude {
				targetslice = append(targetslice, c)
				targetslice = append(targetslice, s[i+1])
				i++
			} else {
				targetslice = append(targetslice, []byte(new)...)
			}
		} else {
			targetslice = append(targetslice, c)
		}
	}

	return string(targetslice)
}

type ByDuration []common.Action

func (fis ByDuration) Len() int {
	return len(fis)
}

func (fis ByDuration) Swap(i, j int) {
	fis[i], fis[j] = fis[j], fis[i]
}

// 按最后一次执行时间排序，执行时间长的放在前面
func (fis ByDuration) Less(i, j int) bool {
	leni := len(fis[i].HistoryDuration)
	lenj := len(fis[j].HistoryDuration)
	if leni > 0 && lenj > 0 {
		return fis[i].HistoryDuration[leni-1] > fis[j].HistoryDuration[lenj-1]
	}

	return false
}
