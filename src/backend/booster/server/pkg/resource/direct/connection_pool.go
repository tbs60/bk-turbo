package direct

import (
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/common/blog"
)

type socketConnPools struct {
	poolsMap map[string]*connPool
}

func NewSocketConnPools(usages []string) *socketConnPools {
	if len(usages) < 1 {
		blog.Errorf("new socket conn pools failed,no usages list")
		return nil
	}
	scp := make(map[string]*connPool)
	for _, usage := range usages {
		scp[usage] = &connPool{
			usage: usage,
			pool:  make(map[string]*net.Conn),
		}
	}
	return &socketConnPools{poolsMap: scp}
}

func (p *socketConnPools) getConnPool(usage string) (*connPool, error) {
	cp, ok := p.poolsMap[usage]
	if !ok {
		return nil, errors.New(fmt.Sprintf("get conn map failed, usage(%s) not support", usage))
	}
	return cp, nil
}

type connPool struct {
	usage    string
	pool     map[string]*net.Conn
	poolLock sync.RWMutex
}

func (cp *connPool) Add(agentIP string, conn *net.Conn) error {
	if conn == nil {
		return errors.New(fmt.Sprintf("add %s map got nil conn (%s)", cp.usage, agentIP))
	}
	cp.poolLock.Lock()
	defer cp.poolLock.Unlock()

	_, ok := cp.pool[agentIP]
	if ok {
		return errors.New(fmt.Sprintf("add %s map already had conn for (%s)", cp.usage, agentIP))
	}
	cp.pool[agentIP] = conn
	return nil
}

func (cp *connPool) Remove(agentIP string) error {
	cp.poolLock.Lock()
	defer cp.poolLock.Unlock()

	_, ok := cp.pool[agentIP]
	if !ok {
		return errors.New(fmt.Sprintf("%s map has no conn for (%s)", cp.usage, agentIP))
	}
	delete(cp.pool, agentIP)
	return nil
}
