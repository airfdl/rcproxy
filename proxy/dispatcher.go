package proxy

import (
	"errors"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/walu/resp"
)

// dispatcher routes requests from all clients to the right backend
// it also maintains the slot table

var (
	CLUSTER_SLOTS        []byte
	ERR_ALL_NODES_FAILED = errors.New("all startup nodes are failed to get cluster slots")
)

func init() {
	cmd, _ := resp.NewCommand("CLUSTER", "SLOTS")
	CLUSTER_SLOTS = cmd.Format()
}

type Dispatcher struct {
	startupNodes       []string
	slotTable          *SlotTable
	slotReloadInterval time.Duration
	reqCh              chan *PipelineRequest
	connPool           *ConnPool
	taskRunners        map[string]*TaskRunner
	// notify slots changed
	slotInfoChan   chan interface{}
	slotReloadChan chan struct{}
}

func NewDispatcher(startupNodes []string, slotReloadInterval time.Duration, connPool *ConnPool) *Dispatcher {
	d := &Dispatcher{
		startupNodes:       startupNodes,
		slotTable:          NewSlotTable(),
		slotReloadInterval: slotReloadInterval,
		reqCh:              make(chan *PipelineRequest, 10000),
		connPool:           connPool,
		taskRunners:        make(map[string]*TaskRunner),
		slotInfoChan:       make(chan interface{}),
		slotReloadChan:     make(chan struct{}, 1),
	}
	return d
}

func (d *Dispatcher) InitSlotTable() error {
	if slotInfos, err := d.doReload(); err != nil {
		return err
	} else {
		for _, si := range slotInfos {
			d.slotTable.SetSlotInfo(si)
		}
	}
	return nil
}

func (d *Dispatcher) Run() {
	var err error
	go d.slotsReloadLoop()
	for {
		select {
		case req, ok := <-d.reqCh:
			// dispatch req
			if !ok {
				log.Infof("exit dispatch loop")
				return
			}
			server := d.slotTable.Get(req.slot)
			taskRunner, ok := d.taskRunners[server]
			if !ok {
				log.Infof("create task runner, server=%s", server)
				taskRunner, err = NewTaskRunner(server, d.connPool)
				if err != nil {
					// TODO
					log.Errorf("create task runner failed")
				} else {
					d.taskRunners[server] = taskRunner
				}
			}
			taskRunner.in <- req
		case info := <-d.slotInfoChan:
			d.handleSlotInfoChanged(info)
		}
	}
}

// handle single slot update and batch update
// remove unused task runner
func (d *Dispatcher) handleSlotInfoChanged(info interface{}) {
	switch info.(type) {
	case *SlotInfo:
		d.slotTable.SetSlotInfo(info.(*SlotInfo))
	case []*SlotInfo:
		newServers := make(map[string]bool)
		for _, si := range info.([]*SlotInfo) {
			d.slotTable.SetSlotInfo(si)
			newServers[si.master] = true
		}
		for server, tr := range d.taskRunners {
			if _, ok := newServers[server]; !ok {
				tr.Exit()
				delete(d.taskRunners, server)
			}
		}
	}
}

func (d *Dispatcher) Schedule(req *PipelineRequest) {
	d.reqCh <- req
}

func (d *Dispatcher) UpdateSlotInfo(si *SlotInfo) {
	log.Infof("update slot info: %#v", si)
	d.slotInfoChan <- si
}

// wait for the slot reload chan and reload cluster topology
// at most every slotReloadInterval
func (d *Dispatcher) slotsReloadLoop() {
	var fails int
	for {
		select {
		case <-time.After(d.slotReloadInterval):
			if _, ok := <-d.slotReloadChan; !ok {
				log.Infof("exit reload slot table loop")
				return
			}
			log.Warnf("reload slot table")
			if slotInfos, err := d.doReload(); err != nil {
				log.Errorf("reload slot table failed")
				fails++
				if fails > 3 {
					log.Panic("reload slot table failed")
				}
			} else {
				fails = 0
				d.slotInfoChan <- slotInfos
			}
		}
	}
}

// request "CLUSTER SLOTS" to retrieve the cluster topology
// try each start up nodes until the first success one
func (d *Dispatcher) doReload() ([]*SlotInfo, error) {
	for _, server := range d.startupNodes {
		cs, err := d.connPool.GetConn(server)
		if err != nil {
			log.Error(server, err)
			continue
		}
		defer cs.Close()
		_, err = cs.Write(CLUSTER_SLOTS)
		if err != nil {
			log.Error(server, err)
			continue
		}
		data, err := resp.ReadData(cs)
		if err != nil {
			log.Error(err)
			continue
		}
		slotInfos := make([]*SlotInfo, 0, len(data.Array))
		for _, info := range data.Array {
			if si, err := NewSlotInfo(info); err != nil {
				return nil, err
			} else {
				slotInfos = append(slotInfos, si)
			}
		}
		return slotInfos, nil
	}
	return nil, ERR_ALL_NODES_FAILED
}

// schedule a reload task
// this call is inherently throttled, so that multiple clients can call it at
// the same time and it will only actually occur once
func (d *Dispatcher) TriggerReloadSlots() {
	select {
	case d.slotReloadChan <- struct{}{}:
	default:
	}
}
func (d *Dispatcher) Exit() {
	close(d.reqCh)
}
