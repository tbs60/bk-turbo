/*
 * Copyright (c) 2021 THL A29 Limited, a Tencent company. All rights reserved
 *
 * This source code file is licensed under the MIT License, you may obtain a copy of the License at
 *
 * http://opensource.org/licenses/MIT
 *
 */

package longtcp

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/TencentBlueKing/bk-turbo/src/backend/booster/common/blog"
)

var (
	ErrorContextCanceled        = fmt.Errorf("session canceled by context")
	ErrorConnectionInvalid      = fmt.Errorf("connection is invalid")
	ErrorResponseLengthInvalid  = fmt.Errorf("response data length is invalid")
	ErrorAllConnectionInvalid   = fmt.Errorf("all connections are invalid")
	ErrorLessThanLongTCPHeadLen = fmt.Errorf("data length is less than long tcp head length")
	ErrorLongTCPHeadLenInvalid  = fmt.Errorf("data length of long tcp head length is invalid")
)

const (
	// 二进制中命令唯一标识的长度
	UniqIDLength = 32
	// 二进制中记录数据长度的字段的长度
	DataLengthInBinary     = 16
	TotalLongTCPHeadLength = 48
)

func longTCPHead2Byte(head *LongTCPHead) ([]byte, error) {
	if len(head.UniqID) != UniqIDLength {
		err := fmt.Errorf("token[%s] is invalid", head.UniqID)
		blog.Debugf("[longtcp] token error: [%v]", err)
		return nil, err
	}

	if head.DataLen < 0 {
		err := fmt.Errorf("data length[%d] is invalid", head.DataLen)
		blog.Debugf("[longtcp] data length error: [%v]", err)
		return nil, err
	}

	data := []byte(fmt.Sprintf("%32s%016x", head.UniqID, head.DataLen))
	return data, nil
}

func byte2LongTCPHead(data []byte) (*LongTCPHead, error) {
	if len(data) < TotalLongTCPHeadLength {
		blog.Errorf("[longtcp] long tcp head length is less than %d", TotalLongTCPHeadLength)
		return nil, ErrorLessThanLongTCPHeadLen
	}

	// check int value
	val, err := strconv.ParseInt(string(data[UniqIDLength:TotalLongTCPHeadLength]), 16, 64)
	if err != nil {
		blog.Errorf("[longtcp] read long tcp head length failed with error: [%v]", err)
		return nil, ErrorLongTCPHeadLenInvalid
	}

	return &LongTCPHead{
		UniqID:  MessageID(data[0:UniqIDLength]),
		DataLen: int(val),
	}, nil
}

// func receiveLongTCPHead(client *TCPClient) (*LongTCPHead, error) {
// 	blog.Debugf("[longtcp] receive long tcp head now")

// 	// receive head token
// 	data, datalen, err := client.ReadData(TotalLongTCPHeadLength)
// 	if err != nil {
// 		blog.Warnf("[longtcp] read data failed with error:%v", err)
// 		return nil, err
// 	}
// 	blog.Debugf("[longtcp] succeed to recieve long tcp head token %s", string(data))

// 	return byte2LongTCPHead(data[0:datalen])
// }

// 固定长度UniqIDLength，用于识别命令
type MessageID string

type LongTCPHead struct {
	UniqID  MessageID // 固定长度，用于识别命令，在二进制中的长度固定为32位
	DataLen int       // 记录data的长度，在二进制中的长度固定为16位
}

type MessageResult struct {
	TCPHead *LongTCPHead
	Data    []byte
	Err     error
}

// 约束条件：
// 返回结果的 UniqID 需要保持不变，方便收到结果后，找到对应的chan
type Message struct {
	TCPHead      *LongTCPHead
	Data         [][]byte
	WaitResponse bool // 发送成功后，是否还需要等待对方返回结果
	RetChan      chan *MessageResult
}

type autoInc struct {
	sync.Mutex
	id uint64
}

type Session struct {
	ctx    context.Context
	cancel context.CancelFunc

	ip     string // 远端的ip
	port   int32  // 远端的port
	client *TCPClient
	valid  bool // 连接是否可用

	// 收到数据后的回调函数
	callback OnReceivedFunc

	// 发送
	sendQueue      []*Message
	sendMutex      sync.RWMutex
	sendNotifyChan chan bool // 唤醒发送协程

	// 等待返回结果
	waitMap   map[MessageID]*Message
	waitMutex sync.RWMutex

	// 记录出现的error
	errorChan chan error

	// 生成唯一id
	idMutext sync.Mutex
	id       uint64
}

// 处理收到的消息，一般是流程是将data转成需要的格式，然后业务逻辑处理，处理完，再通过 Session发送回去
type OnReceivedFunc func(id MessageID, data []byte, s *Session) error

// server端创建session
func NewSessionWithConn(conn *net.TCPConn, callback OnReceivedFunc) *Session {
	client := NewTCPClientWithConn(conn)

	ctx, cancel := context.WithCancel(context.Background())
	remoteaddr := conn.RemoteAddr().String()
	ip := ""
	var port int
	// The port starts after the last colon.
	i := strings.LastIndex(remoteaddr, ":")
	if i > 0 && i < len(remoteaddr)-1 {
		ip = remoteaddr[:i]
		port, _ = strconv.Atoi(remoteaddr[i+1:])
	}

	sendNotifyChan := make(chan bool, 2)
	sendQueue := make([]*Message, 0, 10)
	errorChan := make(chan error, 2)

	s := &Session{
		ctx:            ctx,
		cancel:         cancel,
		ip:             ip,
		port:           int32(port),
		client:         client,
		sendNotifyChan: sendNotifyChan,
		sendQueue:      sendQueue,
		waitMap:        make(map[MessageID]*Message),
		errorChan:      errorChan,
		valid:          true,
		callback:       callback,
		id:             0,
	}

	s.serverStart()

	return s
}

// client端创建session，需要指定目标server的ip和端口
// handshakedata : 用于建立握手协议的数据，当前兼容需要这个，后续不考虑兼容性，可以传nil
// callback : 收到数据后的处理函数
func NewSession(ip string, port int32, timeout int, handshakedata []byte, callback OnReceivedFunc) *Session {
	ctx, cancel := context.WithCancel(context.Background())

	server := fmt.Sprintf("%s:%d", ip, port)
	client := NewTCPClient(timeout)
	if err := client.Connect(server); err != nil {
		blog.Warnf("error: %v", err)
		cancel()
		return nil
	}

	if handshakedata != nil {
		if err := client.WriteData(handshakedata); err != nil {
			_ = client.Close()
			cancel()
			return nil
		}
	}

	sendNotifyChan := make(chan bool, 2)
	sendQueue := make([]*Message, 0, 10)
	errorChan := make(chan error, 2)

	s := &Session{
		ctx:            ctx,
		cancel:         cancel,
		ip:             ip,
		port:           port,
		client:         client,
		sendNotifyChan: sendNotifyChan,
		sendQueue:      sendQueue,
		waitMap:        make(map[MessageID]*Message),
		errorChan:      errorChan,
		valid:          true,
		callback:       callback,
		id:             0,
	}

	s.clientStart()

	blog.Infof("[longtcp] Dial to :%s:%d succeed", ip, port)

	return s
}

func (s *Session) sendRoutine(wg *sync.WaitGroup) {
	wg.Done()
	blog.Debugf("[longtcp] start server internal send...")
	for {
		select {
		case <-s.ctx.Done():
			blog.Debugf("[longtcp] internal send canceled by context")
			return

		case <-s.sendNotifyChan:
			s.sendReal()
		}
	}
}

// copy all from send queue
func (s *Session) copyMessages() []*Message {
	s.sendMutex.Lock()
	defer s.sendMutex.Unlock()

	if len(s.sendQueue) > 0 {
		ret := make([]*Message, len(s.sendQueue), len(s.sendQueue))
		copy(ret, s.sendQueue)
		blog.Debugf("[longtcp] copied %d messages", len(ret))
		s.sendQueue = make([]*Message, 0, 10)
		return ret
	} else {
		return nil
	}
}

// 添加等待的任务
func (s *Session) putWait(msg *Message) error {
	s.waitMutex.Lock()
	defer s.waitMutex.Unlock()

	s.waitMap[msg.TCPHead.UniqID] = msg

	return nil
}

// 删除等待的任务
func (s *Session) removeWait(msg *Message) error {
	s.waitMutex.Lock()
	defer s.waitMutex.Unlock()

	delete(s.waitMap, msg.TCPHead.UniqID)

	return nil
}

// 发送结果
func (s *Session) returnWait(ret *MessageResult) error {
	s.waitMutex.Lock()
	defer s.waitMutex.Unlock()

	if m, ok := s.waitMap[ret.TCPHead.UniqID]; ok {
		m.RetChan <- ret
		delete(s.waitMap, ret.TCPHead.UniqID)
		return nil
	}

	return fmt.Errorf("not found wait message with key %s", ret.TCPHead.UniqID)
}

// 生成唯一id
func (s *Session) uniqid() uint64 {
	s.idMutext.Lock()
	defer s.idMutext.Unlock()

	id := s.id
	s.id++

	return id
}

func formatID(id uint64) MessageID {
	data := fmt.Sprintf("UNIQID%26x", id)
	return MessageID(data)
}

func (s *Session) encData2Message(data [][]byte, waitresponse bool) *Message {
	totallen := 0
	for _, v := range data {
		totallen += len(v)
	}
	return &Message{
		TCPHead: &LongTCPHead{
			UniqID:  formatID(s.uniqid()),
			DataLen: totallen,
		},
		Data:         data,
		WaitResponse: waitresponse,
		RetChan:      make(chan *MessageResult, 1),
	}
}

func (s *Session) encData2MessageWithID(id MessageID, data [][]byte, waitresponse bool) *Message {
	totallen := 0
	for _, v := range data {
		totallen += len(v)
	}
	return &Message{
		TCPHead: &LongTCPHead{
			UniqID:  id,
			DataLen: totallen,
		},
		Data:         data,
		WaitResponse: waitresponse,
		RetChan:      make(chan *MessageResult, 1),
	}
}

func (s *Session) onMessageError(m *Message, err error) {
	if m.WaitResponse {
		s.removeWait(m)
	}

	m.RetChan <- &MessageResult{
		Err:  err,
		Data: nil,
	}
}

// 取任务并依次发送
func (s *Session) sendReal() {
	msgs := s.copyMessages()

	for _, m := range msgs {
		if m.WaitResponse {
			s.putWait(m)
		}

		// encode long tcp head
		handshakedata, err := longTCPHead2Byte(m.TCPHead)
		if err != nil {
			s.onMessageError(m, err)
			continue
		}

		// send long tcp head
		err = s.client.WriteData(handshakedata)
		if err != nil {
			s.onMessageError(m, err)
			continue
		}

		// send real data
		for _, d := range m.Data {
			err = s.client.WriteData(d)
			if err != nil {
				s.onMessageError(m, err)
				continue
			}
		}

		if !m.WaitResponse {
			m.RetChan <- &MessageResult{
				Err:  nil,
				Data: nil,
			}
		}
	}
}

func (s *Session) receiveRoutine(wg *sync.WaitGroup) {
	wg.Done()
	for {
		// TODO : implement receive data here
		// receive long tcp head firstly
		data, _, err := s.client.ReadData(TotalLongTCPHeadLength)
		if err != nil {
			blog.Warnf("[longtcp] receive data failed with error: %v", err)
			s.errorChan <- err
			return
		}

		head, err := byte2LongTCPHead(data)
		if err != nil {
			blog.Errorf("[longtcp] decode long tcp head failed with error: %v", err)
			s.errorChan <- err
			return
		}

		// receive real data now
		data, _, err = s.client.ReadData(int(head.DataLen))
		if err != nil {
			blog.Errorf("[longtcp] receive data failed with error: %v", err)
			s.errorChan <- err
			return
		}

		blog.Debugf("[longtcp] received %d data", len(data))
		// TODO : decode msg, and call funtions to deal, and return response
		ret := &MessageResult{
			Err:     nil,
			TCPHead: head,
			Data:    data,
		}
		if ret.Err != nil {
			blog.Errorf("[longtcp] received data is invalid with error: %v", ret.Err)
			s.errorChan <- err
			return
		} else {
			blog.Debugf("[longtcp] received request with ID: %s", ret.TCPHead.UniqID)
			if s.callback != nil {
				go s.callback(ret.TCPHead.UniqID, ret.Data, s)
			} else {
				err = s.returnWait(ret)
				if err != nil {
					blog.Warnf("[longtcp] notify wait message failed with error: %v", err)
				}
			}
		}

		select {
		case <-s.ctx.Done():
			blog.Debugf("[longtcp] internal recieve canceled by context")
			return
		default:
		}
	}
}

//
func (s *Session) notifyAndWait(msg *Message) *MessageResult {
	blog.Debugf("[longtcp] notify send and wait for response now...")

	// TOOD : 拿锁并判断 s.Valid，避免这时连接已经失效
	s.sendMutex.Lock()

	if !s.valid {
		s.sendMutex.Unlock()
		return &MessageResult{
			Err:  ErrorConnectionInvalid,
			Data: nil,
		}
	}

	s.sendQueue = append(s.sendQueue, msg)
	s.sendMutex.Unlock()

	blog.Debugf("[longtcp] notify by chan now, total %d messages now", len(s.sendQueue))
	s.sendNotifyChan <- true

	msgresult := <-msg.RetChan
	return msgresult
}

// session 内部将data封装为Message发送，并通过chan接收发送结果，Message的id需要内部生成
// 如果 waitresponse为true，则需要等待返回的结果
func (s *Session) Send(data [][]byte, waitresponse bool) *MessageResult {
	if !s.valid {
		return &MessageResult{
			Err:  ErrorConnectionInvalid,
			Data: nil,
		}
	}

	// data 转到 message
	msg := s.encData2Message(data, waitresponse)

	return s.notifyAndWait(msg)
}

// session 内部将data封装为Message发送，并通过chan接收发送结果，这儿指定了id，无需自动生成
// 如果 waitresponse为true，则需要等待返回的结果
func (s *Session) SendWithID(id MessageID, data [][]byte, waitresponse bool) *MessageResult {
	if !s.valid {
		return &MessageResult{
			Err:  ErrorConnectionInvalid,
			Data: nil,
		}
	}

	// data 转到 message
	msg := s.encData2MessageWithID(id, data, waitresponse)

	return s.notifyAndWait(msg)
}

// 启动收发协程
func (s *Session) clientStart() {
	blog.Debugf("[longtcp] ready start client go routines")

	// 先启动接收协程
	var wg1 = sync.WaitGroup{}
	wg1.Add(1)
	go s.receiveRoutine(&wg1)
	wg1.Wait()
	blog.Infof("[longtcp] go routine of client receive started!")

	// 再启动发送协程
	var wg2 = sync.WaitGroup{}
	wg2.Add(1)
	go s.sendRoutine(&wg2)
	wg2.Wait()
	blog.Infof("[longtcp] go routine of client send started!")

	// 最后启动状态检查协程
	var wg3 = sync.WaitGroup{}
	wg3.Add(1)
	go s.check(&wg3)
	wg3.Wait()
	blog.Infof("[longtcp] go routine of client check started!")
}

// 启动收发协程
func (s *Session) serverStart() {
	blog.Debugf("[longtcp] ready start server go routines")

	// 先启动发送协程
	var wg1 = sync.WaitGroup{}
	wg1.Add(1)
	go s.sendRoutine(&wg1)
	wg1.Wait()
	blog.Infof("[longtcp] go routine of server send started!")

	// 再启动接收协程
	var wg2 = sync.WaitGroup{}
	wg2.Add(1)
	go s.receiveRoutine(&wg2)
	wg2.Wait()
	blog.Infof("[longtcp] go routine of server receive started!")

	// 最后启动状态检查协程
	var wg3 = sync.WaitGroup{}
	wg3.Add(1)
	go s.check(&wg3)
	wg3.Wait()
	blog.Infof("[longtcp] go routine of server check started!")
}

func (s *Session) check(wg *sync.WaitGroup) {
	wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			blog.Debugf("[longtcp] session check canceled by context")
			s.Clean(ErrorContextCanceled)
			return
		case err := <-s.errorChan:
			blog.Warnf("[longtcp] session check found error:%v", err)
			s.Clean(err)
			return
		}
	}
}

// 清理资源，包括关闭连接，停止协程等
func (s *Session) Clean(err error) {
	blog.Debugf("[longtcp] session clean now")
	s.client.Close()
	s.cancel()

	// 通知发送队列中的任务
	s.sendMutex.Lock()
	s.valid = false
	for _, m := range s.sendQueue {
		m.RetChan <- &MessageResult{
			Err:  err,
			Data: nil,
		}
	}
	s.sendMutex.Unlock()

	// 通知等待结果的队列中的任务
	s.waitMutex.Lock()
	for _, m := range s.waitMap {
		m.RetChan <- &MessageResult{
			Err:  err,
			Data: nil,
		}
	}
	s.waitMutex.Unlock()
}

func (s *Session) IsValid() bool {
	return s.valid
}

// 返回当前session的任务数
func (s *Session) Size() int {
	return len(s.sendQueue)
}

// ----------------------------------------------------
// 用于客户端的session pool
type ClientSessionPool struct {
	ctx    context.Context
	cancel context.CancelFunc

	ip            string // 远端的ip
	port          int32  // 远端的port
	timeout       int
	handshakedata []byte

	callback OnReceivedFunc
	size     int32
	sessions []*Session
	mutex    sync.RWMutex

	checkNotifyChan chan bool
}

const (
	checkSessionInterval = 3
)

var globalmutex sync.RWMutex
var globalSessionPool *ClientSessionPool

type HandshadeData func() []byte

// 用于初始化并返回全局客户端的session pool
func GetGlobalSessionPool(ip string, port int32, timeout int, callbackhandshake HandshadeData, size int32, callback OnReceivedFunc) *ClientSessionPool {
	// 初始化全局pool，并返回一个可用的
	if globalSessionPool != nil {
		return globalSessionPool
	}

	globalmutex.Lock()
	defer globalmutex.Unlock()

	if globalSessionPool != nil {
		return globalSessionPool
	}

	ctx, cancel := context.WithCancel(context.Background())

	handshakedata := callbackhandshake()
	tempSessionPool := &ClientSessionPool{
		ctx:             ctx,
		cancel:          cancel,
		ip:              ip,
		port:            port,
		timeout:         timeout,
		handshakedata:   callbackhandshake(),
		callback:        callback,
		size:            size,
		sessions:        make([]*Session, size, size),
		checkNotifyChan: make(chan bool, size*2),
	}

	blog.Infof("[longtcp] client pool ready new client sessions")
	for i := 0; i < int(size); i++ {
		client := NewSession(ip, port, timeout, handshakedata, callback)
		if client != nil {
			tempSessionPool.sessions[i] = client
		} else {
			blog.Warnf("[longtcp] client pool new client session failed with %s:%d", ip, port)
			tempSessionPool.sessions[i] = nil
		}
	}

	// 启动检查 session的协程，用于检查和恢复
	blog.Infof("[longtcp] client pool ready start check go routine")
	var wg = sync.WaitGroup{}
	wg.Add(1)
	go tempSessionPool.check(&wg)
	wg.Wait()

	globalSessionPool = tempSessionPool
	return globalSessionPool
}

// 获取可用session
func (sp *ClientSessionPool) GetSession() (*Session, error) {
	blog.Debugf("[longtcp] ready get session now")

	sp.mutex.RLock()
	// select most free
	minsize := 99999
	targetindex := -1
	needchecksession := false
	for i, s := range sp.sessions {
		if s != nil && s.IsValid() {
			if s.Size() < minsize {
				targetindex = i
				minsize = s.Size()
			}
		} else {
			needchecksession = true
		}
	}
	sp.mutex.RUnlock()

	// 注意，如果chan满了，则下面通知会阻塞，所以这时不要持锁，避免其它地方需要锁，导致死锁
	if needchecksession {
		sp.checkNotifyChan <- true
	}

	if targetindex >= 0 {
		return sp.sessions[targetindex], nil
	}

	// TODO : whether allowd dynamic growth?
	return nil, ErrorAllConnectionInvalid
}

func (sp *ClientSessionPool) Clean(err error) error {
	sp.cancel()

	sp.mutex.Lock()
	defer sp.mutex.Unlock()

	for _, s := range sp.sessions {
		if s != nil && s.IsValid() {
			s.Clean(err)
		}
	}

	return nil
}

func (sp *ClientSessionPool) check(wg *sync.WaitGroup) {
	wg.Done()
	for {
		select {
		case <-sp.ctx.Done():
			blog.Debugf("[longtcp] session pool check routine canceled by context")
			return
		case <-sp.checkNotifyChan:
			blog.Debugf("[longtcp] session check triggled")
			sp.checkSessions()
		}
	}
}

func (sp *ClientSessionPool) checkSessions() {
	sp.mutex.Lock()
	defer sp.mutex.Unlock()

	invalidIndex := make([]int, 0)
	for i, s := range sp.sessions {
		if s == nil || !s.IsValid() {
			invalidIndex = append(invalidIndex, i)
		}
	}

	if len(invalidIndex) > 0 {
		blog.Debugf("[longtcp] session check found %d invalid sessions", len(invalidIndex))

		for i := 0; i < len(invalidIndex); i++ {
			client := NewSession(sp.ip, sp.port, sp.timeout, sp.handshakedata, sp.callback)
			if client != nil {
				blog.Debugf("[longtcp] got Lock")
				sessionid := invalidIndex[i]
				if sp.sessions[sessionid] != nil {
					sp.sessions[sessionid].Clean(ErrorConnectionInvalid)
				}
				sp.sessions[sessionid] = client
				blog.Debugf("[longtcp] update %dth session to new", sessionid)
			} else {
				// TODO : if failed ,we should try later?
				blog.Debugf("[longtcp] check sessions failed to NewSession")
				time.Sleep(time.Duration(checkSessionInterval) * time.Second)
				go func() {
					sp.checkNotifyChan <- true
				}()
				return
			}
		}
	}
}
