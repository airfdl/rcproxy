package proxy

import (
	"github.com/CodisLabs/codis/pkg/proxy/redis"
	log "github.com/ngaut/logging"
)

const (
	BACKEND_REQUEST_QUEUE_SIZE = 5000
)

var (
	errBackendQueueOverflow = "backend queue overflow"
)

// Backend represent a redis server, it maintains BackendSessions
type Backend struct {
	addr         string
	connPool     *ConnPool
	connections  int
	requestQueue chan *PipelineRequest
	sessionExit  chan struct{}
	exit         chan struct{}
}

func NewBackend(addr string, connPool *ConnPool, connections int) *Backend {
	b := &Backend{
		addr:         addr,
		connPool:     connPool,
		connections:  connections,
		requestQueue: make(chan *PipelineRequest, BACKEND_REQUEST_QUEUE_SIZE),
		sessionExit:  make(chan struct{}, connections),
		exit:         make(chan struct{}),
	}
	return b
}

func (b *Backend) Start() {
	go b.run()
}

func (b *Backend) Schedule(plReq *PipelineRequest) {
	select {
	case b.requestQueue <- plReq:
	default:
		plReq.backQ <- &PipelineResponse{
			req: plReq,
			obj: redis.NewError([]byte(errBackendQueueOverflow)),
		}
		log.Info(errBackendQueueOverflow, b.addr)
	}
}

func (b *Backend) Stop() {
	close(b.requestQueue)
}

func (b *Backend) run() {
	for i := 0; i < b.connections; i++ {
		b.sessionExit <- struct{}{}
	}
	for {
		select {
		case <-b.exit:
			close(b.requestQueue)
			return
		case <-b.sessionExit:
			b.startBackendSession()
		}
	}
}

func (b *Backend) startBackendSession() {
	log.Info("start backend session to", b.addr)
	conn, err := b.connPool.GetConn(b.addr)
	if err != nil {
		// can not connect to backend, clear pending requests to avoid blocking dispatcher
		log.Error("connect to backend", err, b.addr)
		for {
			select {
			case req := <-b.requestQueue:
				plRsp := &PipelineResponse{
					req: req,
					err: err,
				}
				req.backQ <- plRsp
			default:
				b.sessionExit <- struct{}{}
				return
			}
		}
	}
	session := NewBackendSession(conn, b.requestQueue, b.sessionExit)
	session.Start()
}