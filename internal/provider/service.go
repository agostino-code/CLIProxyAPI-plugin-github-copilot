package provider

import (
	"context"
	"reflect"
	"sync"
	"time"

	"cliproxyapi-github-copilot/internal/transport"
)

const providerID = "copilot"

type Service struct {
	host          transport.Host
	now           func() time.Time
	ctx           context.Context
	cancel        context.CancelFunc
	workMu        sync.Mutex
	closed        bool
	wg            sync.WaitGroup
	startOnce     sync.Once
	configMu      sync.RWMutex
	config        Config
	oauthMu       sync.Mutex
	oauthSession  map[string]*deviceSession
	tokenMu       sync.Mutex
	tokenEntries  map[string]copilotTokenEntry
	tokenRetries  map[string]time.Time
	tokenInflight map[string]*tokenFlight
	modelMu       sync.Mutex
	modelEntries  map[string]modelCacheEntry
	modelInflight map[string]*modelFlight
	syncMu        sync.Mutex
	accountsMu    sync.Mutex
	accounts      map[string]AccountStatus
	syncError     string
}

func New(host transport.Host) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{host: host, now: time.Now, ctx: ctx, cancel: cancel, config: DefaultConfig(), oauthSession: map[string]*deviceSession{}, tokenEntries: map[string]copilotTokenEntry{}, tokenRetries: map[string]time.Time{}, tokenInflight: map[string]*tokenFlight{}, modelEntries: map[string]modelCacheEntry{}, modelInflight: map[string]*modelFlight{}, accounts: map[string]AccountStatus{}}
}
func (s *Service) Context() context.Context { return s.ctx }

// Spawn registers every background worker before shutdown may wait for it.
func (s *Service) spawn(f func()) bool {
	s.workMu.Lock()
	defer s.workMu.Unlock()
	if s.closed {
		return false
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if recover() != nil {
				s.cancel()
				s.accountsMu.Lock()
				s.syncError = "background worker failed; reload the plugin"
				s.accountsMu.Unlock()
			}
		}()
		f()
	}()
	return true
}
func (s *Service) Configure(raw []byte) error {
	cfg, err := ParseConfig(raw)
	if err != nil {
		return err
	}
	s.configMu.Lock()
	changed := !reflect.DeepEqual(s.config, cfg)
	s.config = cfg
	s.configMu.Unlock()
	if changed {
		s.modelMu.Lock()
		clear(s.modelEntries)
		s.modelMu.Unlock()
		s.tokenMu.Lock()
		clear(s.tokenEntries)
		s.tokenMu.Unlock()
	}
	return nil
}
func (s *Service) Config() Config {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	c := s.config
	c.Models = append([]ModelConfig(nil), c.Models...)
	c.ModelsExcluded = append([]string(nil), c.ModelsExcluded...)
	return c
}
func (s *Service) Shutdown() {
	s.workMu.Lock()
	s.closed = true
	s.cancel()
	s.workMu.Unlock()
	s.wg.Wait()
	s.oauthMu.Lock()
	clear(s.oauthSession)
	s.oauthMu.Unlock()
	s.tokenMu.Lock()
	clear(s.tokenEntries)
	s.tokenMu.Unlock()
	s.modelMu.Lock()
	clear(s.modelEntries)
	s.modelMu.Unlock()
}
func (s *Service) Start() {
	s.startOnce.Do(func() {
		s.spawn(func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-s.ctx.Done():
					return
				case <-ticker.C:
					_ = s.SyncAccounts(s.ctx, false)
				}
			}
		})
	})
}
