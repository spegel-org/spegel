package store

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/buraksezer/olric"
	"github.com/buraksezer/olric/config"
	"github.com/go-logr/logr"
	"github.com/xenitab/spegel/internal/discover"
	"go.uber.org/multierr"
)

type OlricStore struct {
	podIP    string
	d        discover.Discover
	readyCtx context.Context
	db       *olric.Olric
	dm       olric.DMap
}

func NewOlricLocalStore(ctx context.Context, d discover.Discover, podIP string) (Store, error) {
	return newOlricStore(ctx, "local", d, podIP, []string{})
}

func NewOlricLanStore(ctx context.Context, d discover.Discover, podIP string, memberlistService string) (Store, error) {
	return newOlricStore(ctx, "lan", d, podIP, []string{memberlistService})
}

func newOlricStore(ctx context.Context, env string, d discover.Discover, podIP string, peers []string) (Store, error) {
	cfg := config.New(env)
	cfg.LogOutput = io.Discard
	cfg.Peers = peers
	cfg.MaxJoinAttempts = 60
	cfg.DMaps.MaxInuse = 104857600 // 100MB
	readyCtx, cancel := context.WithCancel(ctx)
	cfg.Started = func() {
		defer cancel()
	}
	db, err := olric.New(cfg)
	if err != nil {
		return nil, err
	}
	return &OlricStore{
		podIP:    podIP,
		d:        d,
		readyCtx: readyCtx,
		db:       db,
	}, nil
}

func (o *OlricStore) Start() error {
	err := o.db.Start()
	if err != nil {
		return err
	}
	return nil
}

func (o *OlricStore) Ready(ctx context.Context) error {
	<-o.readyCtx.Done()
	e := o.db.NewEmbeddedClient()
	members, err := e.Members(ctx)
	if err != nil {
		return err
	}
	logr.FromContextOrDiscard(ctx).Info("olric store ready", "members", len(members))
	dm, err := e.NewDMap("data")
	if err != nil {
		return err
	}
	o.dm = dm
	return nil
}

func (o *OlricStore) Stop() error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return o.db.Shutdown(shutdownCtx)
}

func (o *OlricStore) Add(ctx context.Context, layers []string) error {
	errs := []error{}
	for _, layer := range layers {
		key := getKey(o.podIP, layer)
		err := o.dm.Put(ctx, key, o.podIP, olric.EX(KeyExpiration))
		if err != nil {
			errs = append(errs, err)
		}
	}
	return multierr.Combine(errs...)
}

func (o *OlricStore) Remove(ctx context.Context, layers []string) error {
	errs := []error{}
	for _, layer := range layers {
		key := getKey(o.podIP, layer)
		_, err := o.dm.Delete(ctx, key)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return multierr.Combine(errs...)
}

func (o *OlricStore) Get(ctx context.Context, layer string) ([]string, error) {
	peers, err := o.d.GetPeers(ctx)
	if err != nil {
		return nil, err
	}
	logr.FromContextOrDiscard(ctx).Info("looking for layers", "peers", peers)
	ips := []string{}
	for _, peer := range peers {
		// Skip self when lookip at peers
		if peer == o.podIP {
			continue
		}
		key := getKey(peer, layer)
		resp, err := o.dm.Get(ctx, key)
		if err != nil && errors.Is(err, olric.ErrKeyNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		ip, err := resp.String()
		if err != nil {
			return nil, err
		}
		ips = append(ips, ip)
	}
	return ips, nil
}

func (o *OlricStore) ResetExpiration(ctx context.Context, layers []string) error {
	errs := []error{}
	for _, layer := range layers {
		key := getKey(o.podIP, layer)
		err := o.dm.Expire(ctx, key, KeyExpiration)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return multierr.Combine(errs...)
}

func (o *OlricStore) Dump(ctx context.Context) ([]string, error) {
	iter, err := o.dm.Scan(ctx)
	if err != nil {
		return nil, err
	}
	data := []string{}
	for iter.Next() {
		data = append(data, iter.Key())
	}
	iter.Close()
	return data, nil
}
