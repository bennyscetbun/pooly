package pooly

import (
	"sync"
	"time"
)

type Computer interface {
	Compute(float64) float64
}

type Selecter interface {
	Select([]*host) *host
}

type ServiceConfig struct {
	*PoolConfig

	SeriesNum       uint
	CloseDeadline   time.Duration
	DecayDuration   time.Duration
	ScoreCalculator Computer
	BanditStrategy  Selecter
}

type Service struct {
	*ServiceConfig

	sync.RWMutex
	name  string
	hosts map[string]*host
	decay *time.Ticker
	// TODO channels
}

func NewService(name string, c *ServiceConfig) *Service {
	if c == nil {
		c = new(ServiceConfig)
	}
	if c.SeriesNum == 0 {
		c.SeriesNum = DefaultSeriesNum
	}
	if c.CloseDeadline == 0 {
		c.CloseDeadline = DefaultCloseDeadline
	}
	if c.DecayDuration == 0 {
		c.DecayDuration = DefaultDecayDuration
	}
	if c.BanditStrategy == nil {
		c.BanditStrategy = DefaultBandit
	}

	s := &Service{
		ServiceConfig: c,
		name:          name,
		hosts:         make(map[string]*host),
		decay:         time.NewTicker(c.DecayDuration),
	}

	go s.timeShift()
	return s
}

func (s *Service) timeShift() {
	for {
		// TODO stop
		<-s.decay.C
		s.Lock()
		for _, h := range s.hosts {
			h.shift()
		}
		s.Unlock()
	}
}

func (s *Service) newHost(a string) {
	s.Lock()
	if _, ok := s.hosts[a]; !ok {
		s.hosts[a] = &host{
			pool:       NewPool(a, s.PoolConfig),
			timeSeries: make([]serie, 1, s.SeriesNum),
		}
	}
	s.Unlock()
}

func (s *Service) deleteHost(a string) {
	s.Lock()
	h, ok := s.hosts[a]
	if !ok {
		s.Unlock()
		return
	}
	delete(s.hosts, a)
	s.Unlock()

	go func() {
		if err := h.pool.Close(); err != nil {
			f := func() { _ = h.pool.ForceClose() }
			time.AfterFunc(s.CloseDeadline, f)
		}
	}()
}

func (s *Service) GetConn() (*Conn, error) {
	s.RLock()
	// TODO cache scores and compute them periodically
	hosts := make([]*host, 0, len(s.hosts))

	for _, h := range s.hosts {
		if _, ok := s.BanditStrategy.(*RoundRobin); !ok {
			h.computeScore(s.ScoreCalculator)
		}
		hosts = append(hosts, h)
	}
	s.RUnlock()

	if len(hosts) == 0 {
		return nil, ErrNoHostAvailable
	}
	h := s.BanditStrategy.Select(hosts)

	c, err := h.pool.Get()
	if err != nil {
		// Pool is closed or timed out, demote the host and start over
		h.rate(HostDown)
		return s.GetConn()
	}

	c.setHost(h)
	return c, nil
}

func (s *Service) Status() map[string]int32 {
	s.RLock()
	m := make(map[string]int32, len(s.hosts))

	for a, h := range s.hosts {
		m[a] = h.pool.ActiveConns()
	}
	s.RUnlock()
	return m
}

func (s *Service) Name() string {
	return s.name
}