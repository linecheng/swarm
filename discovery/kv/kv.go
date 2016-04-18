package kv

import (
	"fmt"
	"path"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/docker/docker/pkg/tlsconfig"
	"github.com/docker/libkv"
	"github.com/docker/libkv/store"
	"github.com/docker/libkv/store/consul"
	"github.com/docker/libkv/store/etcd"
	"github.com/docker/libkv/store/zookeeper"
	"github.com/docker/swarm/discovery"
)

const (
	discoveryPath = "docker/swarm/nodes"
)

// Discovery is exported
type Discovery struct {
	backend   store.Backend
	store     store.Store
	heartbeat time.Duration
	ttl       time.Duration
	prefix    string
	path      string
}

func init() {
	Init()
}

// Init is exported
func Init() {
	// Register to libkv
	zookeeper.Register()
	consul.Register()
	etcd.Register()

	// Register to internal Swarm discovery service
	discovery.Register("zk", &Discovery{backend: store.ZK})
	discovery.Register("consul", &Discovery{backend: store.CONSUL})
	discovery.Register("etcd", &Discovery{backend: store.ETCD})
}

// Initialize is exported
func (s *Discovery) Initialize(uris string, heartbeat time.Duration, ttl time.Duration, discoveryOpt map[string]string) error {
	var (
		parts = strings.SplitN(uris, "/", 2)
		addrs = strings.Split(parts[0], ",")
		err   error
	)

	// A custom prefix to the path can be optionally used.
	if len(parts) == 2 {
		s.prefix = parts[1]
	}

	s.heartbeat = heartbeat
	s.ttl = ttl
	s.path = path.Join(s.prefix, discoveryPath)

	var config *store.Config
	if discoveryOpt["kv.cacertfile"] != "" && discoveryOpt["kv.certfile"] != "" && discoveryOpt["kv.keyfile"] != "" {
		log.Debug("Initializing discovery with TLS")
		tlsConfig, err := tlsconfig.Client(tlsconfig.Options{
			CAFile:   discoveryOpt["kv.cacertfile"],
			CertFile: discoveryOpt["kv.certfile"],
			KeyFile:  discoveryOpt["kv.keyfile"],
		})
		if err != nil {
			return err
		}
		config = &store.Config{
			// Set ClientTLS to trigger https (bug in libkv/etcd)
			ClientTLS: &store.ClientTLSConfig{
				CACertFile: discoveryOpt["kv.cacertfile"],
				CertFile:   discoveryOpt["kv.certfile"],
				KeyFile:    discoveryOpt["kv.keyfile"],
			},
			// The actual TLS config that will be used
			TLS: tlsConfig,
		}
	} else {
		log.Debug("Initializing discovery without TLS")
	}

	// Creates a new store, will ignore options given
	// if not supported by the chosen store
	s.store, err = libkv.NewStore(s.backend, addrs, config)
	return err
}

// Watch the store until either there's a store error or we receive a stop request.
// Returns false if we shouldn't attempt watching the store anymore (stop request received).
func (s *Discovery) watchOnce(stopCh <-chan struct{}, watchCh <-chan []*store.KVPair, discoveryCh chan discovery.Entries, errCh chan error) bool {
	for {
		select {
		case pairs := <-watchCh:
			if pairs == nil {
				return true
			}

			log.WithField("discovery", s.backend).Debugf("Watch triggered with %d nodes", len(pairs))

			// Convert `KVPair` into `discovery.Entry`.
			addrs := make([]string, len(pairs))
			for _, pair := range pairs {
				addrs = append(addrs, string(pair.Value))
			}

			entries, err := discovery.CreateEntries(addrs)
			if err != nil {
				errCh <- err
			} else {
				discoveryCh <- entries
			}
		case <-stopCh:
			// We were requested to stop watching.
			return false
		}
	}
}

// Watch is exported
func (s *Discovery) Watch(stopCh <-chan struct{}) (<-chan discovery.Entries, <-chan error) {
	ch := make(chan discovery.Entries)
	errCh := make(chan error)

	go func() {
		defer close(ch)
		defer close(errCh)

		// Forever: Create a store watch, watch until we get an error and then try again.
		// Will only stop if we receive a stopCh request.
		for {
			// Create the path to watch if it does not exist yet
			exists, err := s.store.Exists(s.path)
			if err != nil {
				errCh <- err
			}
			if !exists {
				if err := s.store.Put(s.path, []byte(""), &store.WriteOptions{IsDir: true}); err != nil {
					errCh <- err
				}
			}

			// Set up a watch.
			watchCh, err := s.store.WatchTree(s.path, stopCh)
			if err != nil {
				errCh <- err
			} else {
				if !s.watchOnce(stopCh, watchCh, ch, errCh) {
					return
				}
			}

			// If we get here it means the store watch channel was closed. This
			// is unexpected so let's retry later.
			errCh <- fmt.Errorf("Unexpected watch error")
			time.Sleep(s.heartbeat)
		}
	}()

	return ch, errCh
}

// Register is exported
func (s *Discovery) Register(addr string) error {
	opts := &store.WriteOptions{TTL: s.ttl}
	return s.store.Put(path.Join(s.path, addr), []byte(addr), opts)
}

// RegisterWithData is exported
func (s *Discovery) RegisterWithData(addr string,data map[string]string) error {
	opts := &store.WriteOptions{TTL: s.ttl}
    var q=""
    var i=0
    for k,v:=range data{
        i++
        if i==1{
            q+=k+"="+v
        }else{
            q+="&"+k+"="+v
        }
    }
    var addrQ=addr+"?"+q
    log.Info("register with data ->"+addrQ)
	return s.store.Put(path.Join(s.path, addr), []byte(addrQ), opts)
}

// Store returns the underlying store used by KV discovery.
func (s *Discovery) Store() store.Store {
	return s.store
}

// Prefix returns the store prefix
func (s *Discovery) Prefix() string {
	return s.prefix
}
