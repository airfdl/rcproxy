package proxy

import (
	"net"
	"sync"
	"time"

	"github.com/fatih/pool"
	log "github.com/ngaut/logging"
)

type ConnPool struct {
	pools       map[string]pool.Pool
	maxIdle     int
	connTimeout time.Duration
	mu          sync.Mutex
}

func NewConnPool(maxIdle int, connTimeout time.Duration) *ConnPool {
	p := &ConnPool{
		pools:       make(map[string]pool.Pool),
		maxIdle:     maxIdle,
		connTimeout: connTimeout,
	}
	return p
}

func (cp *ConnPool) GetConn(server string) (net.Conn, error) {
	var err error
	cp.mu.Lock()
	p := cp.pools[server]
	// create a pool is quite cheap and will not triggered many times
	if p == nil {
		p, err = pool.NewChannelPool(0, cp.maxIdle, func() (net.Conn, error) {
			return net.DialTimeout("tcp", server, cp.connTimeout)
		})
		if err != nil {
			log.Fatal(err)
		}
		cp.pools[server] = p
	}
	cp.mu.Unlock()
	return p.Get()
}

func (cp *ConnPool) Remove(server string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	p := cp.pools[server]
	if p != nil {
		p.Close()
		delete(cp.pools, server)
	}
}
